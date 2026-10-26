#!/usr/bin/env python3
"""Disposable Herdr/mosh experiment. No existing sessions or agents are used."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import shutil
import signal
import struct
import subprocess
import tempfile
import termios
import time

ANSI = re.compile(rb'\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)')


class Terminal:
    def __init__(self, argv, env, cwd, cols=80, rows=24):
        self.fd, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
        def setup():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        self.process = subprocess.Popen(argv, env=env, cwd=cwd, stdin=slave,
                                        stdout=slave, stderr=slave, preexec_fn=setup)
        os.close(slave)
        self.output = bytearray()

    def pump(self, seconds=.2):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if select.select([self.fd], [], [], max(0, end-time.monotonic()))[0]:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                self.output.extend(data)
                # Answer terminal capability probes on this artificial PTY.
                if b'\x1b[6n' in data:
                    os.write(self.fd, b'\x1b[1;1R')
                if b'\x1b[c' in data:
                    os.write(self.fd, b'\x1b[?1;2c')

    def wait_text(self, text, timeout=8):
        end = time.monotonic()+timeout
        while time.monotonic() < end:
            self.pump(.1)
            if text.encode().replace(b' ', b'') in ANSI.sub(b'', self.output).replace(b' ', b''):
                return
        raise AssertionError(f'missing {text!r}; terminal tail={bytes(self.output[-1200:])!r}')

    def send(self, text):
        os.write(self.fd, text.encode())

    def resize(self, cols, rows):
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
        os.killpg(self.process.pid, signal.SIGWINCH)
        self.pump(.4)

    def close(self):
        try:
            if self.process.poll() is None:
                for keys in ('\x02', 'q', '\x1e.'):
                    try:
                        self.send(keys)
                    except OSError:
                        break
                    self.pump(.25)
                    if self.process.poll() is not None:
                        break
                for _ in range(30):
                    self.pump(.1)
                    if self.process.poll() is not None:
                        break
                if self.process.poll() is None:
                    self.process.terminate()
                self.process.wait(timeout=3)
        finally:
            os.close(self.fd)


def read_shell_size(cli, pane, output_file):
    # Only this disposable shell is targeted. Reading an owned file avoids
    # guessing at dimensions from incrementally rendered desktop ANSI frames.
    cli('pane', 'run', pane, 'stty size > ' + shlex.quote(str(output_file)))
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        try:
            dimensions = tuple(map(int, output_file.read_text().split()))
            if len(dimensions) == 2 and all(value > 0 for value in dimensions):
                return dimensions
        except (FileNotFoundError, ValueError):
            pass
        time.sleep(.1)
    raise AssertionError('missing synthetic shell size: ' + str(output_file))


def run(binary, output):
    binary = str(Path(binary).resolve(strict=True))
    version = subprocess.check_output([binary, '--version'], text=True).strip()
    report = {'binary': binary, 'version': version,
              'sha256': hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
              'checks': [], 'scope': 'isolated shell; no coding agent or iOS device'}
    root = Path(tempfile.mkdtemp(prefix='trill-h-', dir='/tmp'))
    report['temporaryRoot'] = str(root)
    env = {k:v for k,v in os.environ.items()
           if not k.startswith(('HERDR_', 'CHIRP_', 'TMUX', 'XDG_', 'MOSH_'))
           and k not in ('ENV', 'BASH_ENV', 'ZDOTDIR')}
    env.update(XDG_CONFIG_HOME=str(root/'config'), XDG_STATE_HOME=str(root/'state'),
               TERM='xterm-256color', SHELL='/bin/sh', LANG='en_US.UTF-8')
    config = root/'config/herdr'
    config.mkdir(parents=True)
    (config/'config.toml').write_text('onboarding = false\n[terminal]\n'
                                    'default_shell = "/bin/sh"\nshell_mode = "non_login"\n'
                                    '[update]\nversion_check = false\nmanifest_check = false\n')
    base = [binary, '--session', 'trill-prototype']
    def cli(*args):
        result = subprocess.run(base+list(args), env=env, cwd=root,
                                capture_output=True, text=True, timeout=10)
        if result.returncode:
            raise RuntimeError(f'{args}: {result.stderr}')
        return json.loads(result.stdout) if result.stdout.strip() else {}
    def passed(name, **details):
        report['checks'].append({'check':name, 'passed':True, **details})
        print('PASS', name, flush=True)
    clients = []
    servers = []
    log = open(root/'server.log', 'wb')
    server = subprocess.Popen(base+['server'], env=env, cwd=root, stdout=log, stderr=log)
    try:
        deadline = time.monotonic()+10
        while True:
            try:
                cli('workspace','list')
                break
            except RuntimeError:
                if time.monotonic()>deadline:
                    raise
                time.sleep(.1)
        created = cli('workspace','create','--cwd',str(root),'--label','Trill probe','--no-focus')['result']
        pane = created['root_pane']['pane_id']
        snapshot = cli('api','snapshot')
        (root/'snapshot.json').write_text(json.dumps(snapshot, indent=2)+'\n')
        panes = cli('pane','list')['result']['panes']
        terminal = next(p for p in panes if p['pane_id']==pane)['terminal_id']
        report.update(paneId=pane, terminalId=terminal)
        passed('metadata', paneId=pane, terminalId=terminal)
        desktop = Terminal(base, env, root, cols=100, rows=30)
        clients.append(desktop)
        desktop.pump(2)
        if desktop.process.poll() is not None:
            raise AssertionError('desktop TUI exited')
        passed('desktop TUI attached to same server')
        desktop_size = read_shell_size(cli, pane, root/'desktop-before.size')
        attach = base+['terminal','attach',terminal]
        direct = Terminal(attach, env, root)
        clients.append(direct)
        direct.pump(1)
        direct.send("printf 'TRILL_%s\\n' DIRECT_OK\r")
        direct.wait_text('TRILL_DIRECT_OK')
        passed('direct attach input/output')
        desktop.send("printf 'DESKTOP_%s\\n' SHARED\r")
        desktop.pump(.3)
        direct.wait_text('DESKTOP_SHARED')
        passed('desktop can type while direct attach is connected')
        direct.resize(62,18)
        direct.send("printf 'SIZE_%s_' ONE; stty size\r")
        direct.wait_text('SIZE_ONE_18 62')
        passed('direct attach resize')
        # A second writable client must fail without taking ownership.
        second = Terminal(attach, env, root)
        clients.append(second)
        second.pump(1)
        if second.process.poll() is None or second.process.returncode == 0:
            raise AssertionError('second controller was not rejected')
        if b'already has an attached client' not in ANSI.sub(b'', second.output):
            raise AssertionError('second controller failed for an unexpected reason')
        passed('second direct controller rejected', exitCode=second.process.returncode)
        direct.send('TRILL_PERSIST=kept\r')
        direct.pump(.2)
        direct.send('\x02')
        direct.pump(.15)
        direct.send('q')
        for _ in range(50):
            direct.pump(.1)
            if direct.process.poll() is not None:
                break
        if direct.process.poll() is None:
            (root/'detach-output.bin').write_bytes(direct.output)
            raise AssertionError('Ctrl+B q did not detach the direct client')
        passed('direct detach exits client')
        restored_size = read_shell_size(cli, pane, root/'desktop-after.size')
        if restored_size != desktop_size:
            raise AssertionError(f'desktop size was not restored: {desktop_size} -> {restored_size}')
        passed('desktop size restored automatically after controller release', rows=restored_size[0], columns=restored_size[1])
        again = Terminal(attach, env, root)
        clients.append(again)
        again.pump(.7)
        again.send("printf 'PERSIST_%s\\n' \"$TRILL_PERSIST\"\r")
        again.wait_text('PERSIST_kept')
        passed('reattach preserves shell process state')
        again.close()
        clients.remove(again)
        time.sleep(.3)
        # Same executable/argv architecture as Trill Bootstrap; no tmux wrapper.
        mosh = shutil.which('mosh-server')
        mosh_client = shutil.which('mosh-client')
        if not mosh or not mosh_client:
            raise RuntimeError('mosh-server and mosh-client must be on PATH')
        mosh_argv = [mosh,'new','-i','127.0.0.1','-p','60120:60130','-c','256',
                     '-l','LANG=en_US.UTF-8','--']+attach
        launch_env = dict(env, MOSH_SERVER_NETWORK_TMOUT='30', MOSH_SERVER_SIGNAL_TMOUT='30')
        def connect_mosh():
            boot = subprocess.run(mosh_argv, env=launch_env, cwd=root,
                                  capture_output=True, timeout=10)
            match = re.search(rb'MOSH CONNECT (\d+) ([A-Za-z0-9+/=]+)',boot.stdout)
            pid = re.search(rb'detached, pid = (\d+)',boot.stderr)
            if pid:
                servers.append(int(pid[1]))
            if boot.returncode or not match or not pid:
                raise RuntimeError(f'mosh bootstrap failed: {boot.stderr!r}')
            client_env = dict(env, MOSH_KEY=match[2].decode())
            client = Terminal([mosh_client,'127.0.0.1',match[1].decode()],client_env,root)
            clients.append(client)
            client.pump(1)
            return client
        phone = connect_mosh()
        phone.send("printf 'MOSH_%s\\n' WORKS\r")
        phone.wait_text('MOSH_WORKS')
        passed('mosh to Herdr direct attach input/output')
        desktop.send("printf 'DESKTOP_%s\\n' MOSH_SHARED\r")
        desktop.pump(.3)
        phone.wait_text('DESKTOP_MOSH_SHARED')
        passed('desktop can type while mosh is connected')
        phone.resize(55,16)
        phone.send("printf 'SIZE_%s_' MOSH; stty size\r")
        phone.wait_text('SIZE_MOSH_16 55')
        passed('mosh resize reaches original shell')
        report['moshCommand'] = mosh_argv
        # Simulate releasing an old Trill lease before obtaining a new lease.
        os.kill(servers[-1], signal.SIGTERM)
        phone.pump(.7)
        phone.close()
        clients.remove(phone)
        time.sleep(.5)
        after = cli('pane','get',pane)
        (root/'pane-after.json').write_text(json.dumps(after,indent=2)+'\n')
        passed('terminal still exists after mosh lease termination')
        recovered = connect_mosh()
        recovered.send("printf 'AFTER_%s_%s\\n' MOSH \"$TRILL_PERSIST\"\r")
        recovered.wait_text('AFTER_MOSH_kept')
        passed('fresh mosh lease preserves shell state without takeover')
        report['success'] = True
    except Exception as error:
        report.update(success=False, error=str(error))
        raise
    finally:
        for client in reversed(clients):
            try:
                client.close()
            except Exception as error:
                report.setdefault('cleanupErrors', []).append(str(error))
        for pid in servers:
            try:
                os.kill(pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
        # This command is scoped to the fresh XDG directory and named server.
        subprocess.run(base+['server','stop'],env=env,cwd=root,capture_output=True,timeout=8)
        try:
            server.wait(timeout=5)
        except subprocess.TimeoutExpired:
            server.terminate()
            server.wait(timeout=5)
        log.close()
        output.parent.mkdir(parents=True,exist_ok=True)
        output.write_text(json.dumps(report,indent=2)+'\n')
        print('Report:',output,flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--herdr',required=True,help='absolute path to a downloaded Herdr binary')
    parser.add_argument('--output',type=Path,default=Path('/tmp/trill-herdr-probe.json'))
    args=parser.parse_args()
    run(args.herdr,args.output)
