#!/usr/bin/env python3
"""Disposable-runner OAuth proxy diagnosis with a local server and dummy tokens."""
import argparse
import gzip
import http.server
import importlib.util
import json
import os
from pathlib import Path
import ssl
import subprocess
import sys
import threading

spec = importlib.util.spec_from_file_location('proxy', Path(__file__).with_name('sbx-oauth-proxy.py'))
proxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)
HOST = 'radar-oauth-fixture.test'
SERVICE = 'radar-oauth-fixture'
CLIENT_ID = 'fixture-public-client'
PROFILES = ('plain', 'gzip', 'escaped', 'combined')


def token(kind, generation, profile='plain'):
    suffix = ('+/=%&' if kind == 'refresh' else '.+/_~==') if profile in ('escaped', 'combined') else ''
    return f'fixture-host-{kind}-{generation}' + suffix


class Provider:
    def __init__(self):
        self.generation = 0
        self.profile = 'plain'
        self.lock = threading.Lock()

    def exchange(self, fields, encoding, authorization=None):
        with self.lock:
            grant = fields.get('grant_type')
            grant = grant if grant in ('authorization_code', 'refresh_token') else 'other'
            refresh = fields.get('refresh_token')
            event = {'operation': grant, 'encoding': encoding,
                     'client_matches': fields.get('client_id') == CLIENT_ID,
                     'refresh_kind': ('fixture_actual' if refresh == token('refresh', self.generation, self.profile)
                                      else 'sentinel' if refresh == proxy.REFRESH else 'other'),
                     'authorization_kind': ('none' if authorization is None else 'fixture_actual'
                         if authorization == 'Bearer ' + token('access', self.generation, self.profile)
                         else 'sentinel' if authorization == 'Bearer ' + proxy.ACCESS else 'other')}
            code_profiles = {'fixture-code-' + profile: profile for profile in PROFILES}
            if not event['client_matches']:
                status, data = 400, {'error': 'invalid_client'}
            elif grant == 'authorization_code' and isinstance(fields.get('code'), str) and fields['code'] in code_profiles:
                self.profile = code_profiles[fields['code']]
                self.generation = 0
                status, data = 200, self.tokens()
            elif grant == 'refresh_token' and event['refresh_kind'] == 'fixture_actual':
                self.generation += 1
                status, data = 200, self.tokens()
            else:
                status, data = 400, {'error': 'invalid_grant'}
            return status, data, dict(event, http_status=status, profile=self.profile)

    def tokens(self):
        return {'access_token': token('access', self.generation, self.profile),
                'refresh_token': token('refresh', self.generation, self.profile),
                'token_type': 'Bearer', 'expires_in': 3600, 'scope': 'monitors_read'}

    def resource(self, authorization):
        with self.lock:
            valid = authorization == 'Bearer ' + token('access', self.generation, self.profile)
            return (200 if valid else 401), {'fixture_read': valid}, {
                'operation': 'resource', 'authorization_matches': valid,
                'http_status': 200 if valid else 401, 'profile': self.profile}


def handler(provider, record):
    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def send(self, status, data, event):
            body = json.dumps(data).encode()
            compressed = provider.profile in ('gzip', 'combined')
            if compressed:
                body = gzip.compress(body)
            record(dict(event, response_encoding='gzip' if compressed else 'identity'))
            self.send_response(status)
            self.send_header('Content-Type', 'application/json; charset=utf-8')
            if compressed:
                self.send_header('Content-Encoding', 'gzip')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def read_body(self):
            if self.headers.get('Transfer-Encoding', '').lower() == 'chunked':
                body = bytearray()
                while True:
                    size = int(self.rfile.readline(80).split(b';')[0], 16)
                    if size == 0:
                        if self.rfile.readline(80) != b'\r\n':
                            raise ValueError()
                        return bytes(body)
                    if size < 0 or len(body) + size > 16384:
                        raise ValueError()
                    body.extend(self.rfile.read(size))
                    if self.rfile.read(2) != b'\r\n':
                        raise ValueError()
            length = int(self.headers.get('Content-Length', '0'))
            if not 0 < length <= 16384:
                raise ValueError()
            return self.rfile.read(length)

        def do_POST(self):
            if self.path != '/oauth/token':
                self.send(404, {}, {'operation': 'other', 'http_status': 404})
                return
            from urllib.parse import parse_qs
            encoding = 'form' if self.headers.get_content_type() == 'application/x-www-form-urlencoded' else 'json'
            try:
                body = self.read_body()
                fields = ({key: values[0] for key, values in parse_qs(body.decode()).items()}
                          if encoding == 'form' else json.loads(body))
                if not isinstance(fields, dict):
                    raise ValueError()
            except (ValueError, UnicodeError):
                self.send(400, {}, {'operation': 'malformed', 'http_status': 400})
                return
            self.send(*provider.exchange(fields, encoding, self.headers.get('Authorization')))

        def do_GET(self):
            if self.path == '/resource':
                self.send(*provider.resource(self.headers.get('Authorization')))
            else:
                self.send(404, {}, {'operation': 'other', 'http_status': 404})
    return Handler


# The same client runs on the host and in SBX. Only assertions leave the process.
CLIENT = r'''
import gzip,json,ssl,sys,urllib.request,urllib.parse,urllib.error
c=json.loads(sys.argv[1]); headers={'Accept-Encoding':'gzip'}
if c['operation']=='resource':
 body=None; path='/resource'; headers['Authorization']='Bearer '+c['access']
else:
 path='/oauth/token'; fields={'grant_type':c['operation'],'client_id':c['client_id']}
 if c['operation']=='authorization_code': fields['code']='fixture-code-'+c.get('profile','plain')
 else: fields['refresh_token']=c['refresh']
 form=c['encoding']=='form'
 body=urllib.parse.urlencode(fields).encode() if form else json.dumps(fields).encode()
 headers['Content-Type']='application/x-www-form-urlencoded' if form else 'application/json'
try:
 request=urllib.request.Request(c['base']+path,data=body,headers=headers)
 with urllib.request.urlopen(request,timeout=20) as response:
  raw=response.read()
  data=json.loads(gzip.decompress(raw) if response.headers.get('Content-Encoding')=='gzip' else raw)
 result={'http_status':response.status}
 if c['operation']=='resource': result['fixture_read']=data.get('fixture_read') is True
 else: result.update(expected_access=data.get('access_token')==c['expected_access'],expected_refresh=data.get('refresh_token')==c['expected_refresh'])
 print(json.dumps(result))
except urllib.error.HTTPError as error:
 result={'http_status':error.code}
 try:
  raw=error.read(16384)
  code=json.loads(gzip.decompress(raw) if error.headers.get('Content-Encoding')=='gzip' else raw).get('error')
  if code in ('invalid_client','invalid_grant','invalid_request','unsupported_grant_type'):result['oauth_error']=code
 except Exception:pass
 print(json.dumps(result))
except urllib.error.URLError as error:
 print(json.dumps({'failure':'tls' if isinstance(error.reason,ssl.SSLError) else 'network'}))
except Exception:print(json.dumps({'failure':'response_or_request'}))
'''


def fixture_kit(image):
    kit = proxy.kit('pup', {}, image)
    kit['name'] = SERVICE
    kit['permissions']['network']['allow'] = [HOST]
    credential = kit['credentials'][0]
    credential['service'] = SERVICE
    credential['oauth']['tokenEndpoint'] = {'host': HOST, 'path': '/oauth/token'}
    credential['oauth']['resourceHosts'] = [HOST]
    return kit


def guard(directory):
    proxy.require(os.environ.get('GITHUB_ACTIONS') == 'true'
                  and os.environ.get('RADAR_SBX_PROXY_DISPOSABLE') == '1',
                  'Use the dedicated disposable GitHub workflow')
    proxy.require(directory.is_relative_to(Path(os.environ['RUNNER_TEMP']).resolve()),
                  "Use the runner's private temporary directory")


def prepare(directory):
    proxy.require(not directory.exists(), 'Fixture directory must be new')
    directory.mkdir(mode=0o700)
    def openssl(*args):
        proxy.run(['openssl', *args])
    openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(directory / 'ca.key'),
            '-out', str(directory / 'ca.crt'), '-days', '1', '-subj', '/CN=Radar local OAuth fixture CA')
    openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(directory / 'server.key'),
            '-out', str(directory / 'server.csr'), '-subj', '/CN=' + HOST)
    (directory / 'extensions').write_text('basicConstraints=critical,CA:FALSE\n'
        'keyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:' + HOST + '\n')
    openssl('x509', '-req', '-in', str(directory / 'server.csr'), '-CA', str(directory / 'ca.crt'),
            '-CAkey', str(directory / 'ca.key'), '-CAcreateserial', '-out', str(directory / 'server.crt'),
            '-days', '1', '-extfile', str(directory / 'extensions'))
    destination = '/usr/local/share/ca-certificates/radar-oauth-fixture-' + os.environ['GITHUB_RUN_ID'] + '.crt'
    proxy.run(['sudo', 'install', '-m', '0644', str(directory / 'ca.crt'), destination])
    proxy.run(['sudo', 'update-ca-certificates'])
    proxy.run(['sudo', 'tee', '-a', '/etc/hosts'], body='\n127.0.0.1 ' + HOST + '\n')
    with (directory / 'server.log').open('w') as output:
        child = subprocess.Popen(['sudo', '--preserve-env=GITHUB_ACTIONS,RADAR_SBX_PROXY_DISPOSABLE,RUNNER_TEMP',
                                 sys.executable, str(Path(__file__).resolve()), 'serve', '--directory', str(directory)],
                                 stdin=subprocess.DEVNULL, stdout=output, stderr=output, start_new_session=True)
    (directory / 'server.pid').write_text(str(child.pid))
    print('Local dummy OAuth fixture prepared; no provider login is started.')


def serve(directory):
    def record(event):
        with (directory / 'events.jsonl').open('a') as output:
            output.write(json.dumps(event) + '\n')
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 443), handler(Provider(), record))
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(directory / 'server.crt', directory / 'server.key')
    server.socket = context.wrap_socket(server.socket, server_side=True)
    # Only binding port 443 requires root; keep fixture files owned by the runner.
    os.setgroups([])
    os.setgid(int(os.environ['SUDO_GID']))
    os.setuid(int(os.environ['SUDO_UID']))
    server.serve_forever()


def check(directory, image):
    name = 'radar-proxy-' + os.environ['GITHUB_RUN_ID']
    report = {'provider': 'local_fixture', 'status': 'fixture_failed', 'authentication_attempted': False,
              'sbx_version': '0.49.1', 'cases': {}}
    try:
        proxy.require(proxy.run(['sbx', 'ls', '--json'], parse=True) in ([], {'sandboxes': []}),
                      'Use a host without existing sandboxes')
        binding = Path.home() / '.config/sbx/credentials.yaml'
        proxy.require(not binding.exists(), 'Use a host without existing credential bindings')
        binding.parent.mkdir(parents=True, exist_ok=True)
        proxy.write(binding, {'bindings': {SERVICE: {'oauth': {'domains': [HOST]}}}})
        (directory / 'kit').mkdir()
        proxy.write(directory / 'kit/spec.yaml', fixture_kit(image))
        proxy.run(['sbx', 'kit', 'validate', '--offline', str(directory / 'kit')])

        def request(execute, operation, encoding, *, profile='plain', outside=False, generation=0):
            data = {'operation': operation, 'encoding': encoding, 'base': 'https://' + HOST,
                    'client_id': CLIENT_ID, 'profile': profile,
                    'access': token('access', generation, profile) if outside else proxy.ACCESS,
                    'refresh': token('refresh', generation, profile) if outside else proxy.REFRESH,
                    'expected_access': token('access', generation + (operation == 'refresh_token'), profile) if outside else proxy.ACCESS,
                    'expected_refresh': token('refresh', generation + (operation == 'refresh_token'), profile) if outside else proxy.REFRESH}
            return execute('python3', '-c', CLIENT, json.dumps(data), parse=True)

        def host(*args, **kwargs):
            return proxy.run(list(args), **kwargs)

        report['phase'] = 'host_fixture_control'
        direct = report['host_control'] = {}
        for profile in PROFILES:
            direct[profile] = {}
            for encoding in ('form', 'json'):
                direct[profile][encoding] = {
                    'exchange': request(host, 'authorization_code', encoding, profile=profile, outside=True),
                    'refresh': request(host, 'refresh_token', encoding, profile=profile, outside=True),
                    'read': request(host, 'resource', encoding, profile=profile, outside=True, generation=1)}
        proxy.require(all(result.get('http_status') == 200 and all(v is True for k, v in result.items() if k != 'http_status')
                          for profile in direct.values() for case in profile.values() for result in case.values()),
                      'Local fixture control failed')
        report['phase'] = 'create_sandbox'
        proxy.run(['sbx', 'create', '--name', name, '--no-progress', str(directory / 'kit')], timeout=300)
        policy = proxy.run(['sbx', 'policy', 'check', 'network', '--sandbox', name, '--json', HOST], parse=True)
        report['policy'] = proxy.routing_summary(policy)
        execute = lambda *args, **kwargs: proxy.sbx(name, *args, **kwargs)
        for profile in PROFILES:
            report['cases'][profile] = {}
            for encoding in ('form', 'json'):
                report['phase'] = profile + '_' + encoding
                report['cases'][profile][encoding] = {
                    'exchange': request(execute, 'authorization_code', encoding, profile=profile),
                    'initial_read': request(execute, 'resource', encoding, profile=profile),
                    'refresh': request(execute, 'refresh_token', encoding, profile=profile),
                    'refreshed_read': request(execute, 'resource', encoding, profile=profile)}
        report['phase'] = 'sandbox_restart'
        proxy.run(['sbx', 'stop', name])
        report['restart_read'] = request(execute, 'resource', 'form')
        results = [result for profile in report['cases'].values() for case in profile.values() for result in case.values()]
        results.append(report['restart_read'])
        passed = all(result.get('http_status') == 200 and all(v is True for k, v in result.items() if k != 'http_status')
                     for result in results)
        report.update(status='fixture_passed' if passed else 'fixture_failed', phase='completed')
    except Exception as error:
        report['error'] = str(error) if isinstance(error, proxy.Failure) else 'Unexpected fixture failure; private details withheld'
    finally:
        try:
            proxy.run(['sbx', 'rm', '--force', name])
        except proxy.Failure:
            report.update(status='fixture_failed', cleanup='Disposable sandbox cleanup failed')
        events = directory / 'events.jsonl'
        if events.exists():
            report['server_events'] = [json.loads(line) for line in events.read_text().splitlines()][:60]
        proxy.write(directory / 'result.json', report)
        proxy.publish(directory, report)
        print(json.dumps(report))
    return 0 if report['status'] == 'fixture_passed' else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=['prepare', 'serve', 'check', 'stop'])
    parser.add_argument('--directory', required=True, type=Path)
    parser.add_argument('--image')
    args = parser.parse_args()
    directory = args.directory.resolve()
    os.umask(0o077)
    guard(directory)
    if args.operation == 'prepare':
        prepare(directory)
    elif args.operation == 'serve':
        serve(directory)
    elif args.operation == 'check':
        return check(directory, args.image)
    elif (directory / 'server.pid').exists():
        pid = int((directory / 'server.pid').read_text())
        cmdline = Path(f'/proc/{pid}/cmdline')
        if cmdline.exists() and str(Path(__file__).resolve()).encode() in cmdline.read_bytes().split(b'\0'):
            proxy.run(['sudo', 'kill', '--', str(pid)])
    return 0


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except proxy.Failure as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(1)
