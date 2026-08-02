#!/usr/bin/env python3
import base64
import hashlib
import hmac
import http.client
import importlib.util
import os
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
import uuid
from unittest import mock

RECEIVER_PATH = os.path.join(os.path.dirname(__file__), "receiver.py")


def load_receiver(environment):
    with mock.patch.dict(os.environ, environment, clear=True):
        spec = importlib.util.spec_from_file_location("receiver_test_module", RECEIVER_PATH)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module


class ReceiverTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.state = os.path.join(self.directory.name, "state.db")
        self.secrets = {
            "staging-key": bytes(range(32)),
            "production-key": bytes(range(1, 33)),
        }
        for credential, secret in (("staging_secret", self.secrets["staging-key"]),
                                   ("production_secret", self.secrets["production-key"])):
            with open(os.path.join(self.directory.name, credential), "wb") as secret_file:
                secret_file.write(base64.b64encode(secret))
        self.environment = {
            "OMNIWA_WEBHOOK_SIGNATURE_MODE": "require",
            "OMNIWA_WEBHOOK_SIGNATURE_KEYS": (
                "staging-key=staging_secret,production-key=production_secret"
            ),
            "CREDENTIALS_DIRECTORY": self.directory.name,
            "OMNIWA_WEBHOOK_STATE": self.state,
        }
        self.receiver = load_receiver(self.environment)
        self.receiver.initialize()

    def tearDown(self):
        self.directory.cleanup()

    def signed_headers(self, body, key_id="staging-key", timestamp="1000", delivery_id=None):
        delivery_id = delivery_id or str(uuid.uuid4())
        canonical = timestamp.encode() + b"." + delivery_id.encode() + b"." + body
        signature = "v1=" + hmac.new(
            self.secrets[key_id], canonical, hashlib.sha256
        ).hexdigest()
        return {
            "X-Omniwa-Delivery-ID": delivery_id,
            "X-Omniwa-Signature-Timestamp": timestamp,
            "X-Omniwa-Signature-Key-ID": key_id,
            "X-Omniwa-Signature": signature,
        }

    def test_each_configured_key_verifies(self):
        body = b'{"event":"test"}'
        for key_id in self.secrets:
            self.assertEqual(
                self.receiver.verify_signature(self.signed_headers(body, key_id), body, now=1000),
                (True, "valid", key_id),
            )

    def test_unknown_key_tamper_stale_and_missing_are_rejected(self):
        body = b"{}"
        unknown = self.signed_headers(body)
        unknown["X-Omniwa-Signature-Key-ID"] = "unknown"
        self.assertEqual(self.receiver.verify_signature(unknown, body, now=1000), (False, "invalid", ""))
        self.assertEqual(
            self.receiver.verify_signature(self.signed_headers(body), body + b" ", now=1000),
            (False, "invalid", ""),
        )
        self.assertEqual(
            self.receiver.verify_signature(self.signed_headers(body), body, now=2000),
            (False, "stale", ""),
        )
        self.assertEqual(self.receiver.verify_signature({}, body, now=1000), (False, "missing", ""))

    def test_duplicate_delivery_is_an_idempotent_noop(self):
        delivery_id = str(uuid.uuid4())
        payload = {"event": "test", "data": {}}
        self.assertTrue(self.receiver.record_delivery(delivery_id, payload))
        self.assertFalse(self.receiver.record_delivery(delivery_id, payload))
        snapshot = self.receiver.stats()
        self.assertEqual(snapshot["total"], 1)
        self.assertEqual(snapshot["duplicates"], 1)

    def test_metrics_have_only_configured_bounded_key_labels(self):
        metrics = self.receiver.prometheus_metrics()
        self.assertIn('key_id="staging-key"', metrics)
        self.assertIn('key_id="production-key"', metrics)
        self.assertNotIn("credential", metrics)

    def test_observe_mode_accepts_only_fully_unsigned_legacy_requests(self):
        environment = dict(self.environment)
        environment["OMNIWA_WEBHOOK_SIGNATURE_MODE"] = "observe"
        receiver = load_receiver(environment)
        self.assertEqual(receiver.verify_signature({}, b"{}", now=1000), (True, "missing", ""))
        self.assertEqual(
            receiver.verify_signature({"X-Omniwa-Signature": "v1=" + "0" * 64}, b"{}", now=1000),
            (False, "invalid", ""),
        )

    def test_legacy_single_key_configuration_remains_supported(self):
        environment = {
            "OMNIWA_WEBHOOK_SIGNATURE_MODE": "require",
            "OMNIWA_WEBHOOK_SIGNATURE_KEY_ID": "legacy-key",
            "OMNIWA_WEBHOOK_SIGNATURE_SECRET_FILE": os.path.join(self.directory.name, "staging_secret"),
            "OMNIWA_WEBHOOK_STATE": self.state,
        }
        receiver = load_receiver(environment)
        self.assertEqual(list(receiver.SIGNATURE_KEYS), ["legacy-key"])

    def test_invalid_or_duplicate_keyring_fails_closed(self):
        cases = [
            "bad/key=staging_secret",
            "one=..",
            "one=staging_secret,one=production_secret",
            "one=staging_secret,two=staging_secret",
            "missing-separator",
            "one=staging_secret,",
        ]
        for mapping in cases:
            environment = dict(self.environment)
            environment["OMNIWA_WEBHOOK_SIGNATURE_KEYS"] = mapping
            with self.assertRaises(RuntimeError):
                load_receiver(environment)

    def test_http_contract_rejects_unsigned_and_accepts_duplicate_signed_delivery(self):
        server = self.receiver.BoundedThreadingHTTPServer(("127.0.0.1", 0), self.receiver.Handler)
        server.daemon_threads = True
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        body = b'{"event":"test","data":{}}'
        url = f"http://127.0.0.1:{server.server_port}/omniwa"

        unsigned = urllib.request.Request(
            url,
            data=body,
            method="POST",
            headers={"X-Omniwa-Delivery-ID": str(uuid.uuid4())},
        )
        with self.assertRaises(urllib.error.HTTPError) as rejected:
            urllib.request.urlopen(unsigned)
        self.assertEqual(rejected.exception.code, 401)

        delivery_id = str(uuid.uuid4())
        headers = self.signed_headers(
            body, timestamp=str(int(self.receiver.time.time())), delivery_id=delivery_id
        )
        for _ in range(2):
            request = urllib.request.Request(url, data=body, method="POST", headers=headers)
            with urllib.request.urlopen(request) as response:
                self.assertEqual(response.status, 200)
        self.assertEqual(self.receiver.stats()["total"], 1)
        self.assertEqual(self.receiver.stats()["duplicates"], 1)

    def test_http_contract_rejects_duplicate_protected_headers(self):
        server = self.receiver.BoundedThreadingHTTPServer(("127.0.0.1", 0), self.receiver.Handler)
        server.daemon_threads = True
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        body = b"{}"
        connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
        self.addCleanup(connection.close)
        connection.putrequest("POST", "/omniwa")
        connection.putheader("Content-Length", str(len(body)))
        connection.putheader("X-Omniwa-Delivery-ID", str(uuid.uuid4()))
        connection.putheader("X-Omniwa-Delivery-ID", str(uuid.uuid4()))
        connection.endheaders(body)
        self.assertEqual(connection.getresponse().status, 401)


if __name__ == "__main__":
    unittest.main()
