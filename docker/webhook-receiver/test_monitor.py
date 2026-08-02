#!/usr/bin/env python3
import importlib.util
import http.server
import json
import os
import tempfile
import threading
import unittest
import urllib.error

MONITOR_PATH = os.path.join(os.path.dirname(__file__), "monitor.py")
SPEC = importlib.util.spec_from_file_location("webhook_monitor", MONITOR_PATH)
MONITOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MONITOR)


class MonitorTests(unittest.TestCase):
    def responses(self, missing=0, invalid=0, stale=0, egress="203.0.113.10"):
        def fetch(url, **_):
            if url.endswith("/healthz") or url.endswith("/server/ok"):
                return '{"status":"ok"}'
            if url.endswith("/stats"):
                return json.dumps({
                    "signatureMode": "require",
                    "signatureMissing": missing,
                    "signatureInvalid": invalid,
                    "signatureStale": stale,
                })
            return egress
        return fetch

    def environment(self):
        return {
            "OMNIWA_MONITOR_RECEIVER_URL": "http://receiver",
            "OMNIWA_MONITOR_API_URLS": "http://staging,http://production",
            "OMNIWA_MONITOR_EXPECTED_EGRESS_IP": "203.0.113.10",
        }

    def test_healthy_initial_baseline(self):
        errors, state = MONITOR.check(
            self.environment(), {}, fetch=self.responses(), ntp_check=lambda: True
        )
        self.assertEqual(errors, [])
        self.assertEqual(state["signatureInvalid"], 0)

    def test_counter_delta_egress_and_ntp_fail_closed(self):
        errors, _ = MONITOR.check(
            self.environment(),
            {"signatureMissing": 0, "signatureInvalid": 0, "signatureStale": 0},
            fetch=self.responses(invalid=1, egress="203.0.113.11"),
            ntp_check=lambda: False,
        )
        self.assertEqual(
            errors,
            ["egress_ip_changed", "ntp_unsynchronized", "receiver_invalid_increased"],
        )

    def test_state_is_private_and_atomic(self):
        with tempfile.TemporaryDirectory() as directory:
            path = os.path.join(directory, "state.json")
            MONITOR.write_state(path, {"signatureInvalid": 2})
            self.assertEqual(MONITOR.load_state(path), {"signatureInvalid": 2})
            self.assertEqual(os.stat(path).st_mode & 0o777, 0o600)

    def test_counter_reset_is_reported(self):
        errors, _ = MONITOR.check(
            self.environment(),
            {"signatureMissing": 2, "signatureInvalid": 2, "signatureStale": 2},
            fetch=self.responses(),
            ntp_check=lambda: True,
        )
        self.assertEqual(errors, ["receiver_counter_reset"])

    def test_authenticated_outbox_metrics_detect_new_dead_letter_and_old_pending(self):
        with tempfile.TemporaryDirectory() as directory:
            with open(os.path.join(directory, "staging_key"), "w", encoding="utf-8") as key_file:
                key_file.write("test-api-key")
            environment = self.environment()
            environment["CREDENTIALS_DIRECTORY"] = directory
            metrics_url = "http://127.0.0.1:4180/metrics"
            environment["OMNIWA_MONITOR_METRICS_TARGETS"] = metrics_url + "=staging_key"

            def fetch(url, **kwargs):
                if url.endswith("/metrics"):
                    self.assertEqual(kwargs["headers"], {"apikey": "test-api-key"})
                    return (
                        'omniwa_external_event_outbox_rows{status="dead_letter"} 3\n'
                        "omniwa_external_event_outbox_oldest_pending_age_seconds 301\n"
                    )
                return self.responses()(url)

            target = MONITOR.hashlib.sha256(metrics_url.encode()).hexdigest()[:16]
            previous = {
                "signatureMissing": 0,
                "signatureInvalid": 0,
                "signatureStale": 0,
                "outbox_dead_letter:" + target: 2.0,
            }
            errors, _ = MONITOR.check(environment, previous, fetch=fetch, ntp_check=lambda: True)
            self.assertEqual(errors, ["outbox_dead_letter_increased", "outbox_pending_too_old"])

    def test_metrics_credentials_are_restricted_to_loopback(self):
        with tempfile.TemporaryDirectory() as directory:
            with open(os.path.join(directory, "key"), "w", encoding="utf-8") as key_file:
                key_file.write("test-api-key")
            environment = self.environment()
            environment["CREDENTIALS_DIRECTORY"] = directory
            environment["OMNIWA_MONITOR_METRICS_TARGETS"] = "https://example.com/metrics=key"
            errors, _ = MONITOR.check(
                environment, {}, fetch=self.responses(), ntp_check=lambda: True
            )
            self.assertEqual(errors, ["outbox_metrics_unavailable"])

    def test_fetch_text_does_not_follow_redirects(self):
        redirected_requests = []

        class TargetHandler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                redirected_requests.append(self.headers.get("apikey"))
                self.send_response(200)
                self.end_headers()

            def log_message(self, *_):
                pass

        target = http.server.ThreadingHTTPServer(("127.0.0.1", 0), TargetHandler)

        class RedirectHandler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(302)
                self.send_header("Location", f"http://127.0.0.1:{target.server_port}/metrics")
                self.end_headers()

            def log_message(self, *_):
                pass

        redirect = http.server.ThreadingHTTPServer(("127.0.0.1", 0), RedirectHandler)
        threads = [
            threading.Thread(target=server.serve_forever, daemon=True)
            for server in (target, redirect)
        ]
        for thread in threads:
            thread.start()
        self.addCleanup(target.server_close)
        self.addCleanup(target.shutdown)
        self.addCleanup(redirect.server_close)
        self.addCleanup(redirect.shutdown)

        with self.assertRaises(urllib.error.HTTPError) as rejected:
            MONITOR.fetch_text(
                f"http://127.0.0.1:{redirect.server_port}/metrics",
                headers={"apikey": "must-not-leak"},
            )
        self.assertEqual(rejected.exception.code, 302)
        self.assertEqual(redirected_requests, [])


if __name__ == "__main__":
    unittest.main()
