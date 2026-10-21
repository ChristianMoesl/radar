"""Offline checks for the real proxy test's boundaries and callback channel."""
import base64
from contextlib import redirect_stdout
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
import urllib.error
import urllib.parse
import zipfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('proxy_test', ROOT / 'scripts/sbx-oauth-proxy.py')
proxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)
fixture_spec = importlib.util.spec_from_file_location('oauth_fixture', ROOT / 'scripts/sbx-oauth-fixture.py')
fixture = importlib.util.module_from_spec(fixture_spec)
fixture_spec.loader.exec_module(fixture)


class Boundaries(unittest.TestCase):
    def test_fixture_uses_only_its_local_host_and_retains_masking(self):
        kit = fixture.fixture_kit('docker.io/christianmoesl/radar-sandbox:test')
        self.assertEqual(kit['permissions']['network']['allow'], [fixture.HOST])
        credential = kit['credentials'][0]
        self.assertEqual(credential['service'], fixture.SERVICE)
        self.assertEqual(credential['oauth']['resourceHosts'], [fixture.HOST])
        self.assertEqual(credential['oauth']['tokenEndpoint']['host'], fixture.HOST)
        self.assertFalse(credential['oauth']['passthrough'])
        self.assertNotIn('credentialFile', credential['oauth'])
        self.assertNotIn('apiKey', credential)
        implicit = fixture.fixture_kit('docker.io/christianmoesl/radar-sandbox:test', 'implicit')
        self.assertNotIn('resourceHosts', implicit['credentials'][0]['oauth'])
        separate = fixture.fixture_kit('docker.io/christianmoesl/radar-sandbox:test', 'separate')
        self.assertEqual(separate['credentials'][0]['oauth']['resourceHosts'], [fixture.RESOURCE_HOST])
        self.assertEqual(set(separate['permissions']['network']['allow']), {fixture.HOST, fixture.RESOURCE_HOST})

    def test_public_token_fixture_rejects_resource_bearer_without_rotating(self):
        provider = fixture.Provider()
        fields = {'client_id': fixture.CLIENT_ID, 'grant_type': 'authorization_code', 'code': 'fixture-code-public'}
        self.assertEqual(provider.exchange(fields, 'form')[0], 200)
        refresh = dict(fields, grant_type='refresh_token', refresh_token=fixture.token('refresh', 0))
        status, data, event = provider.exchange(refresh, 'form', authorization='Bearer ' + fixture.token('access', 0))
        self.assertEqual(status, 403)
        self.assertEqual(data, {'error': 'unexpected_authorization'})
        self.assertEqual(event['authorization_kind'], 'fixture_actual')
        self.assertEqual(provider.generation, 0)
        self.assertEqual(provider.exchange(refresh, 'form')[0], 200)

    def test_fixture_rotation_rejects_old_refresh_and_unknown_values_stay_private(self):
        provider = fixture.Provider()
        fields = {'client_id': fixture.CLIENT_ID, 'grant_type': 'refresh_token',
                  'refresh_token': fixture.token('refresh', 0)}
        status, data, event = provider.exchange(fields, 'form')
        self.assertEqual(status, 200)
        self.assertEqual(data['refresh_token'], fixture.token('refresh', 1))
        self.assertEqual(provider.exchange(fields, 'form')[0], 400)
        self.assertEqual(provider.resource('Bearer ' + fixture.token('access', 0))[0], 401)
        self.assertEqual(provider.resource('Bearer ' + fixture.token('access', 1))[0], 200)
        status, data, event = provider.exchange(dict(fields, client_id='private-client', refresh_token='private-token'),
                                                'json', authorization='Bearer private-header')
        self.assertEqual(status, 400)
        self.assertNotIn('private', json.dumps(event))
        self.assertEqual(event['refresh_kind'], 'other')
        self.assertEqual(event['authorization_kind'], 'other')

    def test_fixture_client_exercises_both_encodings_without_printing_tokens(self):
        import http.server
        events = []
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), fixture.handler(fixture.Provider(), events.append))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            for encoding in ('form', 'json'):
                cfg = {'base': 'http://127.0.0.1:' + str(server.server_port), 'operation': 'authorization_code',
                       'client_id': fixture.CLIENT_ID, 'encoding': encoding, 'profile': 'combined',
                       'expected_access': fixture.token('access', 0, 'combined'),
                       'expected_refresh': fixture.token('refresh', 0, 'combined')}
                environment = dict(proxy.os.environ)
                for key in ('NO_PROXY', 'no_proxy'):
                    environment[key] = environment.get(key, '') + ',127.0.0.1'
                result = subprocess.run(['python3', '-c', fixture.CLIENT, json.dumps(cfg)],
                                        env=environment, text=True, capture_output=True, check=True, timeout=5)
                self.assertEqual(json.loads(result.stdout), {'http_status': 200, 'expected_access': True, 'expected_refresh': True})
                self.assertNotIn('fixture-host', result.stdout + result.stderr)
                cfg.update(operation='refresh_token', refresh=fixture.token('refresh', 0, 'combined'),
                           expected_access=fixture.token('access', 1, 'combined'),
                           expected_refresh=fixture.token('refresh', 1, 'combined'))
                result = subprocess.run(['python3', '-c', fixture.CLIENT, json.dumps(cfg)],
                                        env=environment, text=True, capture_output=True, check=True, timeout=5)
                self.assertEqual(json.loads(result.stdout), {'http_status': 200, 'expected_access': True, 'expected_refresh': True})
                self.assertNotIn('fixture-host', result.stdout + result.stderr)
            self.assertEqual([event['encoding'] for event in events], ['form', 'form', 'json', 'json'])
            self.assertTrue(all(event['authorization_kind'] == 'none' for event in events))
            # HTTP proxies may forward a rewritten request using chunked framing.
            import http.client
            connection = http.client.HTTPConnection('127.0.0.1', server.server_port, timeout=5)
            fields = urllib.parse.urlencode({'client_id': fixture.CLIENT_ID,
                'grant_type': 'authorization_code', 'code': 'fixture-code-plain'}).encode()
            connection.request('POST', '/oauth/token', body=iter([fields]),
                               headers={'Content-Type': 'application/x-www-form-urlencoded'}, encode_chunked=True)
            response = connection.getresponse()
            self.assertEqual(response.status, 200)
            response.read()
            connection.close()
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_control_isolates_native_credentials_and_preserves_network_settings(self):
        environment = {'DD_ACCESS_TOKEN': 'private-access', 'DD_API_KEY': 'private-key',
                       'DD_APP_KEY': 'private-key', 'GH_TOKEN': 'private-github',
                       'PUP_MOCK_SERVER': 'https://private.example', 'PUP_TRUST_SITE': '1', 'PUP_CONFIG_DIR': '/old/config',
                       'DD_TOKEN_STORAGE': 'keychain', 'HTTPS_PROXY': 'http://proxy.example:8080',
                       'SSL_CERT_FILE': '/fixture/ca.pem'}
        with patch.dict(proxy.os.environ, environment, clear=True):
            actual = proxy.control_environment(Path('/new/control'))
        self.assertEqual(actual['PUP_CONFIG_DIR'], '/new/control/native-config')
        self.assertEqual(actual['DD_TOKEN_STORAGE'], 'file')
        self.assertEqual(actual['HTTPS_PROXY'], environment['HTTPS_PROXY'])
        self.assertEqual(actual['SSL_CERT_FILE'], environment['SSL_CERT_FILE'])
        self.assertEqual(actual['DD_API_KEY'], '\n')
        self.assertEqual(actual['DD_APP_KEY'], '\n')
        self.assertNotIn('PUP_TRUST_SITE', actual)
        self.assertNotIn('private', json.dumps(actual))

    def test_control_refreshes_before_and_after_the_negative_probe_without_qualifying_sbx(self):
        events = []
        def execute(*argv, **kwargs):
            if argv[0] == 'python3':
                events.append('negative_probe')
                return {'sentinel_response': False, 'failure': 'http', 'http_status': 403}
            if argv[-1] == 'refresh':
                events.append('refresh')
                return ''
            if argv[-1] == 'status':
                return {'authenticated': True, 'has_refresh': True, 'token_type': 'bearer',
                        'site': 'datadoghq.eu', 'org': proxy.PROFILE, 'scopes': ['monitors_read']}
            events.append('read')
            return {'monitors': [{'tags': ['radar-oauth-e2e']}]}
        cfg = {'site': 'datadoghq.eu', 'monitor_query': 'tag:radar-oauth-e2e'}
        kit = proxy.kit('pup', cfg, 'docker.io/christianmoesl/radar-sandbox:test')
        report = {'status': 'failed', 'checks': ['native_cli_login']}
        proxy.verify_control(execute, cfg, kit, report, 'public-client')
        self.assertEqual(events, ['read', 'refresh', 'read', 'negative_probe', 'refresh', 'read'])
        self.assertEqual(report['status'], 'control_passed')
        self.assertEqual(report['checks'], ['native_cli_login', 'native_oauth_identity_and_read',
                         'native_refresh_before_negative_probe', 'native_refresh_after_negative_probe'])
        self.assertFalse(report['verified']['sentinel_token_response'])

    def test_control_retains_native_refresh_failure_and_stops(self):
        report = {'status': 'failed', 'checks': []}
        with patch.object(proxy, 'verify_pup'), patch.object(proxy, 'run') as fallback:
            with self.assertRaises(proxy.Failure):
                proxy.verify_control(lambda *args: (_ for _ in ()).throw(proxy.Failure('HTTP 403')),
                                     {}, {}, report, 'public-client')
        fallback.assert_not_called()
        self.assertEqual(report['status'], 'failed')
        self.assertEqual(report['phase'], 'outside_sbx_refresh_before_negative_probe')
        self.assertEqual(report['verification_step'], 'native_refresh')
        self.assertEqual(report['checks'], ['native_oauth_identity_and_read'])
        self.assertNotIn('negative_probe', report)

    def test_routing_summary_keeps_decisions_and_drops_private_fields(self):
        logs = {'logs': [
            {'host': 'api.example:443', 'proxy': 'forward-bypass', 'allowed': True,
             'headers': {'Authorization': 'private-fixture'}, 'body': 'private-provider-body'},
            {'host': 'private.example', 'proxy': 'forward', 'rule': 'private-rule'}]}
        summary = proxy.routing_summary(logs, 'api.example')
        self.assertEqual(summary, {'routes': ['forward-bypass'], 'decisions': ['allow'], 'matching_entries': 1})
        self.assertNotIn('private', json.dumps(summary))
        self.assertEqual(proxy.routing_summary({'body': {'type': 'forward'}, 'response': {'allowed': True}})['routes'], [])
        self.assertEqual(proxy.routing_summary({'allowed': False})['decisions'], ['deny'])

    def test_masking_probe_uses_the_public_client_and_withholds_provider_errors(self):
        fixture = {'endpoint': 'https://api.example/token', 'client_id': 'public-client',
                   'access': proxy.ACCESS, 'refresh': proxy.REFRESH}

        def provider(request, timeout):
            fields = urllib.parse.parse_qs(request.data.decode())
            if fields.get('client_id') != ['public-client']:
                raise urllib.error.HTTPError(request.full_url, 400, 'private-fixture', {}, None)
            return io.BytesIO(json.dumps({'access_token': proxy.ACCESS, 'refresh_token': proxy.REFRESH}).encode())

        output = io.StringIO()
        with patch.object(proxy.sys, 'argv', ['probe', json.dumps(fixture)]), \
                patch('urllib.request.urlopen', side_effect=provider), redirect_stdout(output):
            exec(proxy.MASK_PROBE, {})
        self.assertTrue(json.loads(output.getvalue())['sentinel_response'])
        output = io.StringIO()
        with patch.object(proxy.sys, 'argv', ['probe', json.dumps(fixture)]), \
                patch('urllib.request.urlopen', side_effect=urllib.error.HTTPError(fixture['endpoint'], 403, 'private-fixture', {}, None)), \
                redirect_stdout(output):
            exec(proxy.MASK_PROBE, {})
        self.assertEqual(json.loads(output.getvalue()), {'sentinel_response': False, 'failure': 'http', 'http_status': 403})
        self.assertNotIn('private-fixture', output.getvalue())

    def test_partial_verification_does_not_claim_masking_passed(self):
        status = {'authenticated': True, 'has_refresh': True, 'token_type': 'bearer',
                  'site': 'datadoghq.eu', 'org': proxy.PROFILE, 'scopes': ['monitors_read']}
        report = {}
        cfg = {'site': 'datadoghq.eu', 'monitor_query': 'tag:radar-oauth-e2e'}
        kit = proxy.kit('pup', cfg, 'docker.io/christianmoesl/radar-sandbox:test')
        with patch.object(proxy, 'sbx', side_effect=[status, {'monitors': [{'tags': ['radar-oauth-e2e']}]},
                {'sentinel_response': False, 'failure': 'http', 'http_status': 400}]):
            with self.assertRaises(proxy.Failure):
                proxy.verify('pup', cfg, 'sandbox', kit, report, 'public-client')
        self.assertEqual(report['verification_step'], 'token_response_masking')
        self.assertEqual(report['verified'], {'oauth_identity': True, 'approved_fixture_read': True, 'sentinel_token_response': False})

    def test_characterization_retains_an_earlier_masking_failure(self):
        report = {'phase': 'identity_read_and_masking'}
        def baseline(*args):
            report['verification_step'] = 'token_response_masking'
            raise proxy.Failure('Proxy token-response masking is unverified (http; HTTP 403)')
        with patch.object(proxy, 'verify', side_effect=baseline):
            proxy.verify_phase('pup', {}, 'sandbox', {}, report, 'public-client')
        report['phase'] = 'refresh'
        with patch.object(proxy, 'verify'):
            proxy.verify_phase('pup', {}, 'sandbox', {}, report, 'public-client')
        self.assertEqual(report['masking_failures'], [{'phase': 'identity_read_and_masking',
                         'error': 'Proxy token-response masking is unverified (http; HTTP 403)'}])

    def test_only_authorization_link_escapes_native_diagnostics(self):
        url = 'https://auth.atlassian.com/oauth/activate?user_code=ABC-123'
        self.assertEqual(proxy.challenge('twg', 'private-token\n' + url + '\nprivate diagnostic'), url)
        self.assertEqual(proxy.challenge('twg', json.dumps({'url': url, 'secret': 'private-fixture'})), url)
        self.assertIsNone(proxy.challenge('twg', 'https://evil.example/oauth/activate?user_code=abc'))

    def test_kit_masks_tokens_and_never_writes_cli_credential_files(self):
        for provider, cfg in [('twg', {'base_url': 'https://example.atlassian.net'}), ('pup', {})]:
            kit = proxy.kit(provider, cfg, 'docker.io/christianmoesl/radar-sandbox:test')
            oauth = kit['credentials'][0]['oauth']
            self.assertFalse(oauth['passthrough'])
            self.assertNotIn('credentialFile', oauth)
            self.assertNotIn('apiKey', kit['credentials'][0])
            self.assertTrue(set(oauth['resourceHosts']) <= set(kit['permissions']['network']['allow']))
        with self.assertRaises(proxy.Failure):
            proxy.kit('pup', {}, 'docker.io/untrusted/image:latest')

    def test_callback_is_bound_to_the_original_login_and_local_listener(self):
        authorization = 'https://app.datadoghq.eu/oauth2/v1/authorize?state=expected'
        good = 'http://127.0.0.1:8000/oauth/callback?code=fixture&state=expected'
        self.assertEqual(proxy.callback_url(good, authorization), good)
        for url in [good.replace('expected', 'other'), good.replace('127.0.0.1', 'evil.example'),
                    good.replace('8000', '9000'), good + '\nprivate-value']:
            with self.assertRaises(proxy.Failure):
                proxy.callback_url(url, authorization)

    def test_private_cli_errors_are_not_returned(self):
        result = subprocess.CompletedProcess(['twg'], 1, 'private-provider-output', 'private-credential')
        with patch.object(proxy.subprocess, 'run', return_value=result):
            with self.assertRaises(proxy.Failure) as caught:
                proxy.run(['twg'])
        self.assertNotIn('private-provider-output', str(caught.exception))
        self.assertNotIn('private-credential', str(caught.exception))

    def test_diagnostics_return_only_fixed_labels(self):
        labels = proxy.diagnostics('AUTH_SITE_UNRESOLVED private-fixture credential unavailable https://private.example')
        self.assertEqual(labels, ['credential_binding', 'auth_site'])
        self.assertNotIn('private-fixture', str(labels))
        self.assertEqual(proxy.diagnostics('OAuth login waiting for approval'), [])
        self.assertEqual(proxy.diagnostics('gh: temporary failure (HTTP 503) private-fixture'), ['http_5xx'])
        self.assertEqual(proxy.diagnostics('Login timed out after 5 minutes'), ['login_timeout'])
        self.assertTrue(proxy.login_progress('Login timed\r\nout after 5 minutes')['native_timeout'])
        self.assertEqual(proxy.approval_window_seconds('Waiting (300s remaining)\rWaiting (290s remaining)'), 300)
        self.assertIsNone(proxy.approval_window_seconds('private-fixture'))
        self.assertEqual(proxy.login_progress('Authentication successful! private-fixture\nSaved profile example.'),
                         {'polling_started': False, 'token_exchange_completed': True, 'profile_saved': True, 'native_timeout': False})

    def test_native_driver_declines_only_the_twg_local_browser_prompt(self):
        with tempfile.TemporaryDirectory() as directory:
            native = Path(directory) / 'twg'
            native.write_text('#!/usr/bin/env python3\nimport os,tty,select\n'
                              'assert "DOCKER_AGENT_NAME" not in os.environ\n'
                              'assert os.environ["HTTPS_PROXY"]=="http://proxy.example:8080"\n'
                              'assert os.environ["SSL_CERT_FILE"]=="/fixture/ca.pem"\n'
                              'assert os.environ["SSH_AUTH_SOCK"]=="/fixture/agent.sock"\n'
                              'tty.setraw(0)\nprint("Open in browser?", flush=True)\n'
                              'answer=os.read(0,1).decode()\n'
                              'print("Unrelated confirmation?", flush=True)\nassert not select.select([0],[],[],.1)[0]\n'
                              'print("Waiting for authentication in browser" if answer=="n" else "wrong answer", flush=True)\n')
            native.chmod(0o700)
            environment = dict(proxy.os.environ, DOCKER_AGENT_NAME='fixture', HTTPS_PROXY='http://proxy.example:8080',
                               SSL_CERT_FILE='/fixture/ca.pem', SSH_AUTH_SOCK='/fixture/agent.sock')
            result = subprocess.run(['python3', str(proxy.LOGIN), str(native)], env=environment, capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0)
            self.assertTrue(proxy.login_progress(result.stdout)['polling_started'])

    def test_native_pup_driver_closes_unused_input_only_after_its_listener_starts(self):
        with tempfile.TemporaryDirectory() as directory:
            native = Path(directory) / 'pup'
            native.write_text('#!/usr/bin/env python3\nimport select,sys\n'
                              'print("Unrelated confirmation?", flush=True)\n'
                              'assert not select.select([0],[],[],.1)[0]\n'
                              'print("Waiting for authorization...", flush=True)\n'
                              'assert sys.stdin.readline()==""\n'
                              'print("Login successful (fixture)!", flush=True)\n')
            native.chmod(0o700)
            result = subprocess.run(['python3', str(proxy.LOGIN), str(native)],
                                    capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0)
            progress = proxy.pup_login_progress(result.stdout)
            self.assertTrue(progress['callback_listener_ready'])
            self.assertTrue(progress['native_login_reported_success'])
            self.assertFalse(progress['code_exchange_started'])
            self.assertNotIn('fixture', json.dumps(progress))

    def test_callback_relay_preserves_the_sandbox_destination(self):
        binary = io.BytesIO()
        with zipfile.ZipFile(binary, 'w') as archive:
            archive.writestr('callback.txt', 'sealed-fixture')
        authorization = 'https://app.datadoghq.eu/oauth2/v1/authorize?state=expected'
        callback = 'http://127.0.0.1:8000/oauth/callback?code=fixture&state=expected'
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(proxy.os.environ, GITHUB_REPOSITORY='example/repo', GITHUB_RUN_ID='123'), \
                patch.object(proxy, 'run', side_effect=[{'artifacts': [{'id': 1, 'size_in_bytes': 200}]}, callback]), \
                patch.object(proxy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, binary.getvalue())), \
                patch.object(proxy, 'sbx') as execute:
            self.assertTrue(proxy.relay(Path(directory), authorization, 'radar-proxy-123'))
            self.assertEqual(execute.call_args.args[0], 'radar-proxy-123')
            self.assertEqual(execute.call_args.kwargs['body'], callback)

    def test_explicit_control_relays_only_to_the_host_listener(self):
        binary = io.BytesIO()
        with zipfile.ZipFile(binary, 'w') as archive:
            archive.writestr('callback.txt', 'sealed-fixture')
        authorization = 'https://app.datadoghq.eu/oauth2/v1/authorize?state=expected'
        callback = 'http://127.0.0.1:8000/oauth/callback?code=fixture&state=expected'
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(proxy.os.environ, GITHUB_REPOSITORY='example/repo', GITHUB_RUN_ID='123'), \
                patch.object(proxy, 'run', side_effect=[{'artifacts': [{'id': 1, 'size_in_bytes': 200}]}, callback, '']) as execute, \
                patch.object(proxy.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, binary.getvalue())), \
                patch.object(proxy, 'sbx') as sandbox:
            self.assertTrue(proxy.relay(Path(directory), authorization, 'radar-proxy-123', host_control=True))
            sandbox.assert_not_called()
            self.assertEqual(execute.call_args.kwargs['body'], callback)
            self.assertNotIn(callback, execute.call_args.args[0])

    def test_encrypted_callback_rejects_another_run_and_tampering(self):
        with tempfile.TemporaryDirectory() as directory:
            key = Path(directory) / 'callback.key'
            bridge = ROOT / 'scripts/sbx-oauth-callback.mjs'
            subprocess.run(['node', str(bridge), 'keygen', str(key), '123'], check=True, capture_output=True)
            plain = 'http://127.0.0.1:8000/oauth/callback?code=private-fixture&state=fixture'
            sealed = subprocess.run(['node', str(bridge), 'seal', str(key) + '.pub', '123'],
                                    input=plain, text=True, capture_output=True, check=True).stdout
            self.assertNotIn('private-fixture', sealed)
            opened = subprocess.run(['node', str(bridge), 'open', str(key), '123'],
                                    input=sealed, text=True, capture_output=True, check=True).stdout
            self.assertEqual(opened, plain)
            wrong = subprocess.run(['node', str(bridge), 'open', str(key), '456'],
                                   input=sealed, text=True, capture_output=True)
            self.assertNotEqual(wrong.returncode, 0)
            packet = json.loads(base64.urlsafe_b64decode(sealed + '=' * (-len(sealed) % 4)))
            packet['data'] = base64.b64encode(b'tampered').decode()
            damaged = base64.urlsafe_b64encode(json.dumps(packet).encode()).decode().rstrip('=')
            wrong = subprocess.run(['node', str(bridge), 'open', str(key), '123'],
                                   input=damaged, text=True, capture_output=True)
            self.assertNotEqual(wrong.returncode, 0)


if __name__ == '__main__':
    unittest.main()
