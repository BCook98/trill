#!/usr/bin/env python3
"""Run SwiftTerm over the HTTPS terminal stream through the real Go sidecar against a disposable Herdr server."""
import argparse
import base64
import json
import os
from pathlib import Path
import secrets
import shlex
import socket
import subprocess
import tempfile
import time
import urllib.request
import urllib.error

REPO = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--herdr', required=True)
    parser.add_argument('--output', type=Path, default=Path('/tmp/trill-herdr-sidecar-results.json'))
    args = parser.parse_args()
    binary = str(Path(args.herdr).resolve(strict=True))
    root = Path(tempfile.mkdtemp(prefix='trill-hs-', dir='/tmp'))
    report = {'temporaryRoot': str(root), 'checks': [], 'success': False}
    env = {k:v for k,v in os.environ.items()
           if not k.startswith(('HERDR_', 'CHIRP_', 'XDG_', 'CLAUDE_CODE_'))
           and k not in ('ENV', 'BASH_ENV', 'ZDOTDIR', 'CLAUDECODE')
           and (not k.startswith('CODEX_') or k == 'CODEX_HOME')}
    env.update(XDG_CONFIG_HOME=str(root/'config'), XDG_STATE_HOME=str(root/'state'),
               TERM='xterm-256color', SHELL='/bin/sh', LANG='en_US.UTF-8')
    config = root/'config/herdr'
    config.mkdir(parents=True)
    (config/'config.toml').write_text('onboarding = false\n[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n[update]\nversion_check = false\nmanifest_check = false\n')
    base = [binary, '--session', 'trill-prototype']
    def cli(*argv, check=True):
        result = subprocess.run(base+list(argv), env=env, cwd=root, capture_output=True, text=True, timeout=12)
        if check and result.returncode:
            raise RuntimeError(f'Herdr command failed: {argv}; see {root}')
        if result.returncode != 0:
            return None
        try:
            return json.loads(result.stdout) if result.stdout.strip() else {}
        except json.JSONDecodeError:
            return {"text": result.stdout}
    def passed(name):
        report['checks'].append(name)
        print('PASS',name,flush=True)
    processes = []
    logs = []
    def start(argv, name):
        log = open(root/name, 'wb')
        logs.append(log)
        child_env = dict(env, XDG_CONFIG_HOME=str(root/'unrelated-companion-config')) if '--herdr-config-home' in argv else env
        child = subprocess.Popen(argv, env=child_env, cwd=root, stdout=log, stderr=log)
        processes.append(child)
        return child
    def wait_until(fn, timeout=15):
        deadline = time.monotonic()+timeout
        while time.monotonic()<deadline:
            try:
                value = fn()
                if value:
                    return value
            except (RuntimeError, OSError):
                pass
            time.sleep(.2)
        raise RuntimeError(f'timed out waiting for isolated service; diagnostics: {root}')
    start(base+['server'], 'herdr-server.log')
    try:
        wait_until(lambda: cli('workspace','list',check=False))
        created = cli('workspace','create','--cwd',str(root),'--label','Trill Swift probe','--no-focus')['result']
        pane = created['root_pane']['pane_id']
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0))
            port=sock.getsockname()[1]
        token=secrets.token_hex(48)
        token_file=root/'token'
        token_file.write_text(token)
        token_file.chmod(0o600)
        address=f'http://127.0.0.1:{port}'
        sidecar = REPO/'.local/herdr-prototype/trilld'
        start([str(sidecar),'--listen',f'127.0.0.1:{port}','--insecure-loopback',
               '--token-file',str(token_file),'--pairing-store',str(root/'devices.json'),'--allow-control','--backends','herdr','--herdr-path',binary,'--herdr-config-home',str(root/'config'),
               '--herdr-sessions','trill-prototype','--herdr-lab-root',str(root)],'sidecar.log')
        def get(path):
            request=urllib.request.Request(address+path,headers={'Authorization':'Bearer '+token})
            with urllib.request.urlopen(request,timeout=20) as response:
                return json.load(response)
        wait_until(lambda: get('/v1/health'))
        session = get('/v1/sessions')['sessions'][0]
        session_id = session['id']
        report.update(sessionId=session_id, herdrVersion=cli('api','snapshot')['result']['snapshot']['version'])
        probe=REPO/'tools/TransportProbe/.build/debug/HerdrTransportProbe'
        result = subprocess.run([str(probe), address, str(token_file), session_id], env=env, cwd=root, capture_output=True, text=True, timeout=180)
        (root / 'stream-results.json').write_text(result.stdout + result.stderr)
        if result.returncode:
            raise RuntimeError(f'Stream probe failed; diagnostics: {root}')
        report['stream'] = json.loads(result.stdout)
        print(result.stdout, end='', flush=True)
        passed('SwiftTerm input, resize, history scrolling, labeled links and retained shell state')
        from concurrent.futures import ThreadPoolExecutor
        def wait_for_change():
            return get('/v1/changes')['changed']
        with ThreadPoolExecutor(max_workers=1) as executor:
            change = executor.submit(wait_for_change)
            time.sleep(1)
            cli('workspace', 'rename', created['workspace']['workspace_id'], 'Stream event probe')
            if not change.result(timeout=20):
                raise RuntimeError('Native workspace event did not invalidate the catalog')
        passed('Real socket event invalidates the companion catalog')
        assert cli('pane', 'get', pane)['result']
        passed('Herdr terminal survives stream release')
        # Exercise native creation and destructive closure only on this owned
        # server. The lab's ordinary controller remains alive throughout.
        folder = root / "Selected folder 'quotes' $(no-shell)"
        folder.mkdir()
        (folder / 'selected-folder.txt').write_text('owned folder selection proof')
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            mutation_port = listener.getsockname()[1]
        mutation_address = f'http://127.0.0.1:{mutation_port}'
        start([str(sidecar), '--listen', f'127.0.0.1:{mutation_port}', '--insecure-loopback',
               '--token-file', str(token_file), '--allow-control', '--herdr-path', binary,
               '--herdr-config-home', str(root/'config'), '--herdr-sessions', 'trill-prototype'], 'native-mutations.log')
        def native_request(method, path, value=None):
            request = urllib.request.Request(mutation_address + path, method=method,
                headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'},
                data=json.dumps(value).encode() if value is not None else None)
            with urllib.request.urlopen(request, timeout=20) as response:
                return json.load(response)
        wait_until(lambda: native_request('GET', '/v1/health'))
        from urllib.parse import quote
        listing = native_request('GET', '/v1/folders?path=' + quote(str(root), safe=''))
        assert any(item['path'] == str(folder) for item in listing['folders'])
        before_rows = native_request('GET', '/v1/sessions')['sessions']
        before = {row['id'] for row in before_rows}
        production_session = next(row for row in before_rows if row['id'] == session_id)
        native_request('POST', '/v1/sessions', {'projectId': production_session['projectId'], 'kind': 'terminal', 'directory': str(folder)})
        made = [row for row in native_request('GET', '/v1/sessions')['sessions'] if row['id'] not in before]
        assert len(made) == 1 and made[0]['capabilities']['closeTerminal']
        files = native_request('GET', '/v1/sessions/' + made[0]['id'] + '/files')
        assert any(item['name'] == 'selected-folder.txt' for item in files['entries'])
        passed('Native terminal starts in the selected Mac folder; spaces and shell syntax remain literal')
        native_request('DELETE', '/v1/sessions/' + made[0]['id'])
        remaining = {row['id'] for row in native_request('GET', '/v1/sessions')['sessions']}
        assert made[0]['id'] not in remaining and before <= remaining
        passed('Native terminal closure removes only the newly created owned tab')
        report['success'] = True
    except Exception as error:
        report['error']=str(error)
        raise
    finally:
        # The only server stopped is the one created with this isolated XDG root.
        cli('server','stop',check=False)
        for child in reversed(processes):
            if child.poll() is None:
                child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
        for log in logs:
            log.close()
        args.output.parent.mkdir(parents=True,exist_ok=True)
        args.output.write_text(json.dumps(report,indent=2)+'\n')
        print('Report:',args.output,flush=True)


if __name__=='__main__':
    main()
