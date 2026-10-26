import hashlib
import importlib.util
from pathlib import Path
import ssl
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('demo_api_smoke', Path(__file__).parents[2] / 'scripts/demo-api-smoke.py')
MOD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MOD)


class PinTests(unittest.TestCase):
    def test_mismatch_closes_connection_before_request(self):
        connection = MOD.PinnedHTTPSConnection('localhost', expected_pin='0' * 64)
        fake_socket = mock.Mock()
        fake_socket.getpeercert.return_value = b'untrusted peer'
        def handshake(value):
            value.sock = fake_socket
        with mock.patch.object(MOD.http.client.HTTPSConnection, 'connect', handshake):
            with self.assertRaises(ssl.SSLError):
                connection.connect()
        fake_socket.close.assert_called_once()
        fake_socket.sendall.assert_not_called()

    def test_matching_pin_uses_verified_socket(self):
        der = b'exact fixture certificate'
        connection = MOD.PinnedHTTPSConnection('localhost', expected_pin=hashlib.sha256(der).hexdigest())
        fake_socket = mock.Mock()
        fake_socket.getpeercert.return_value = der
        def handshake(value):
            value.sock = fake_socket
        with mock.patch.object(MOD.http.client.HTTPSConnection, 'connect', handshake):
            connection.connect()
        self.assertIs(connection.sock, fake_socket)
        fake_socket.close.assert_not_called()

    def test_redirects_cannot_forward_device_token(self):
        handler = MOD.NoRedirects()
        request = MOD.urllib.request.Request('https://127.0.0.1/v1/sessions', headers={'Authorization': 'Bearer synthetic-test'})
        self.assertIsNone(handler.redirect_request(request, None, 302, 'Moved', {}, 'https://outside.example/'))


if __name__ == '__main__':
    unittest.main()
