#!/usr/bin/env python3
"""Run Swift MoshCore through the real Go sidecar against a disposable Herdr server."""
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
    parser.add_argument('--agent', choices=['claude','codex'])
    parser.add_argument('--output', type=Path, default=Path('/tmp/trill-herdr-sidecar-results.json'))
    args = parser.parse_args()
    binary = str(Path(args.herdr).resolve(strict=True))
    root = Path(tempfile.mkdtemp(prefix='trill-hs-', dir='/tmp'))
    report = {'temporaryRoot': str(root), 'checks': [], 'success': False}
    env = {k:v for k,v in os.environ.items()
           if not k.startswith(('HERDR_', 'CHIRP_', 'TMUX', 'XDG_', 'MOSH_', 'CLAUDE_CODE_'))
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
               '--token-file',str(token_file),'--pairing-store',str(root/'devices.json'),'--allow-control','--mosh-host','127.0.0.1',
               '--udp-ports','60140:60150','--backends','herdr','--herdr-path',binary,'--herdr-config-home',str(root/'config'),
               '--herdr-sessions','trill-prototype','--herdr-lab-root',str(root)],'sidecar.log')
        def get(path):
            request=urllib.request.Request(address+path,headers={'Authorization':'Bearer '+token})
            with urllib.request.urlopen(request,timeout=3) as response:
                return json.load(response)
        wait_until(lambda: get('/v1/health'))
        session = get('/v1/sessions')['sessions'][0]
        session_id = session['id']
        report.update(sessionId=session_id, herdrVersion=cli('api','snapshot')['result']['snapshot']['version'])
        probe=REPO/'tools/TransportProbe/.build/debug/HerdrTransportProbe'
        def run_probe(kind):
            result=subprocess.run([str(probe),address,str(token_file),session_id,kind],
                                  env=env,cwd=root,capture_output=True,text=True,timeout=210)
            (root/f'swift-{kind}.log').write_text(result.stdout+result.stderr)
            if result.returncode:
                raise RuntimeError(f'Swift {kind} probe failed (exit {result.returncode}); diagnostics: {root}')
            report.setdefault('transportChecks', {})[kind] = result.stdout.splitlines()
            print(result.stdout,end='',flush=True)
            passed('Swift transport through Go sidecar: '+kind)
        # Exercise production aggregation and mutations against only our Herdr
        # workspace. Existing Xirp sessions are read as metadata, never mutated.
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0)); mixed_port=sock.getsockname()[1]
        mixed_url=f'http://127.0.0.1:{mixed_port}'
        start([str(sidecar),'--listen',f'127.0.0.1:{mixed_port}','--insecure-loopback',
               '--token-file',str(token_file),'--allow-control','--mosh-host','127.0.0.1',
               '--udp-ports','60160:60170','--backends','xirp,herdr','--herdr-path',binary,'--herdr-config-home',str(root/'config'),
               '--herdr-sessions','trill-prototype,trill-missing-probe'],'mixed-sidecar.log')
        def mixed_request(method,path,body=None):
            request=urllib.request.Request(mixed_url+path,method=method,
                       headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'},
                       data=json.dumps(body).encode() if body is not None else None)
            with urllib.request.urlopen(request,timeout=20) as response:
                return json.load(response)
        wait_until(lambda: mixed_request('GET','/v1/health'))
        listing=mixed_request('GET','/v1/sessions')
        owned=next(row for row in listing['sessions'] if row['id']==session_id and row['provider']=='herdr')
        assert any(not status['available'] for status in listing['providers'])
        passed('Combined listings retain healthy providers when a Herdr server is offline')
        passed('Pinned Herdr config home survives a different companion XDG environment')
        mixed_request('PATCH','/v1/sessions/'+session_id,{'name':'Managed by Trill'})
        renamed=next(row for row in mixed_request('GET','/v1/sessions')['sessions'] if row['id']==session_id)
        assert renamed['name']=='Managed by Trill'
        passed('Native Herdr rename through the mixed-provider API')
        before={row['terminal_id'] for row in cli('pane','list')['result']['panes']}
        mixed_request('POST','/v1/sessions',{'projectId':owned['projectId'],'kind':'terminal','useWorktree':False})
        created=[row for row in cli('pane','list')['result']['panes'] if row['terminal_id'] not in before]
        assert len(created)==1
        passed('Native Herdr terminal creation with verified readback')
        cli('pane','close',created[0]['pane_id'])
        # An external direct controller must receive no implicit takeover.
        from probe import Terminal
        terminal=next(row['terminal_id'] for row in cli('pane','list')['result']['panes'] if row['pane_id']==pane)
        owner=Terminal(base+['terminal','attach',terminal],env,root)
        try:
            owner.pump(1)
            request=urllib.request.Request(address+'/v1/sessions/'+session_id+'/mosh',method='POST',
                       headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'},
                       data=json.dumps({'cols':80,'rows':24}).encode())
            try:
                urllib.request.urlopen(request,timeout=10)
                raise AssertionError('occupied direct terminal was not rejected')
            except urllib.error.HTTPError as error:
                assert error.code==409
            passed('Occupied direct terminal returns HTTP 409 without takeover')
        finally:
            owner.close()
        run_probe('shell')
        # Reproduce an app restart/lost DELETE: leave the old real connection
        # alive, then attach again using only the same paired device token.
        (root/'owner.json').write_text(json.dumps({'Kind':'trill-demo-lab-v1',
            'Address':address,'Name':'Recovery probe','Fingerprint':''}))
        def paired_request(method, path, credential, body=None):
            request=urllib.request.Request(address+path, method=method,
                headers={'Authorization':'Bearer '+credential,'Content-Type':'application/json'},
                data=json.dumps(body).encode() if body is not None else None)
            with urllib.request.urlopen(request,timeout=20) as response:
                return json.load(response)
        def pair_device(name):
            invitation=root/(name+'-invite')
            subprocess.run(['go','run','./tools/demo-invite','--state-dir',str(root),'--output',str(invitation)],
                cwd=REPO/'sidecar', check=True, capture_output=True, timeout=45)
            encoded=invitation.read_text().strip().split('data=',1)[1]
            payload=json.loads(base64.urlsafe_b64decode(encoded+'='*((-len(encoded))%4)))
            return paired_request('POST','/v1/pair','',{'code':payload['code'],'name':name})['token']
        phone=pair_device('RecoveryPhone')
        other=pair_device('OtherPhone')
        route='/v1/sessions/'+session_id+'/mosh'
        first=paired_request('POST',route,phone,{'cols':80,'rows':24})
        first_client=Terminal(['mosh-client',first['host'],str(first['port'])],dict(env,MOSH_KEY=first['key']),root)
        second_client=None
        replacement=None
        try:
            first_client.pump(1)
            first_client.send("TRILL_RECOVERY_MARKER=alive_after_reconnect; printf 'RECOVERY_READY\\n'\n")
            first_client.wait_text('RECOVERY_READY')
            try:
                paired_request('POST',route,other,{'cols':80,'rows':24})
                raise AssertionError('another phone replaced the active connection')
            except urllib.error.HTTPError as error:
                assert error.code==409
            replacement=paired_request('POST',route,phone,{'cols':80,'rows':24})
            assert replacement['leaseId'] != first['leaseId']
            second_client=Terminal(['mosh-client',replacement['host'],str(replacement['port'])],
                dict(env,MOSH_KEY=replacement['key']),root)
            second_client.pump(1)
            second_client.send("printf 'RECOVERED:%s\\n' \"$TRILL_RECOVERY_MARKER\"\n")
            second_client.wait_text('RECOVERED:alive_after_reconnect')
            passed('Same paired phone recovers an unreleased real Mosh connection; shell state survives')
            passed('Another paired phone cannot replace the active Herdr connection')
        finally:
            for lease in (replacement, first):
                if lease:
                    try: paired_request('DELETE','/v1/leases/'+lease['leaseId'],phone)
                    except urllib.error.HTTPError as error:
                        if error.code!=404: raise
            if second_client: second_client.close()
            first_client.close()
        # Check the authoritative terminal still exists after Swift releases its lease.
        assert cli('pane','get',pane)['result']
        if args.agent:
            import shutil
            agent=shutil.which(args.agent)
            if not agent:
                raise RuntimeError(args.agent+' is not on PATH')
            command=shlex.join([agent,'--safe-mode','--tools','','--permission-mode','plan','--model','haiku'] if args.agent=='claude' else [agent,'--no-daemon','--sandbox','read-only','--ask-for-approval','on-request'])
            cli('pane','run',pane,command)
            # Only this fresh disposable directory can be trusted by this probe.
            trusted=False
            skipped_update=False
            deadline=time.monotonic()+45
            while time.monotonic()<deadline:
                view=json.dumps(cli('pane','read',pane,'--source','visible','--lines','60'))
                if args.agent=='codex' and not skipped_update and 'Update available' in view:
                    cli('pane','send-keys',pane,'esc')
                    skipped_update=True
                if not trusted and any(text in view.lower() for text in ('trust this folder', 'trust this directory', 'do you trust the contents', 'allow codex to work')):
                    cli('pane','send-keys',pane,'enter')
                    trusted=True
                rows=get('/v1/sessions')['sessions']
                if any(r['id']==session_id and r['agent'].lower()==args.agent and r['activityState'] in ('idle','done') for r in rows):
                    break
                time.sleep(.5)
            else:
                (root/(args.agent+'-startup.json')).write_text(view)
                raise RuntimeError(f'{args.agent} not ready in disposable pane; diagnostics: {root}')
            passed('Herdr detects disposable full-screen '+args.agent)
            run_probe(args.agent)
        report['success']=True
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
