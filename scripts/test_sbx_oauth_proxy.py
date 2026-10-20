"""Offline checks for the real proxy test's boundaries and callback channel."""
import base64
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import zipfile
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('proxy_test', ROOT / 'scripts/sbx-oauth-proxy.py')
proxy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proxy)


class Boundaries(unittest.TestCase):
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
        self.assertEqual(proxy.login_progress('Authentication successful! private-fixture\nSaved profile example.'),
                         {'polling_started': False, 'token_exchange_completed': True, 'profile_saved': True, 'native_timeout': False})

    def test_native_driver_declines_only_the_twg_local_browser_prompt(self):
        with tempfile.TemporaryDirectory() as directory:
            native = Path(directory) / 'twg'
            native.write_text('#!/usr/bin/env python3\nimport os,tty,select\ntty.setraw(0)\nprint("Open in browser?", flush=True)\n'
                              'answer=os.read(0,1).decode()\n'
                              'print("Unrelated confirmation?", flush=True)\nassert not select.select([0],[],[],.1)[0]\n'
                              'print("Waiting for authentication in browser" if answer=="n" else "wrong answer", flush=True)\n')
            native.chmod(0o700)
            result = subprocess.run(['python3', str(proxy.LOGIN), str(native)], capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0)
            self.assertTrue(proxy.login_progress(result.stdout)['polling_started'])

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
