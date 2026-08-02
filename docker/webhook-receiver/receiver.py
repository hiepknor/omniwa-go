#!/usr/bin/env python3
"""Minimal operator-owned webhook receiver with HMAC verification."""

import base64
import binascii
import hashlib
import hmac
import json
import os
import re
import sqlite3
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HOST = os.environ.get("OMNIWA_WEBHOOK_HOST", "127.0.0.1")
PORT = int(os.environ.get("OMNIWA_WEBHOOK_PORT", "9000"))
MAX_BODY = 4 * 1024 * 1024
RETENTION_SECONDS = 7 * 24 * 60 * 60
MAX_CLOCK_SKEW_SECONDS = 5 * 60
MAX_KEYS = 8
MAX_CONCURRENT_REQUESTS = 32
REQUEST_TIMEOUT_SECONDS = 10
DB_PATH = os.environ.get("OMNIWA_WEBHOOK_STATE", "/var/lib/omniwa-webhook/state.db")
SIGNATURE_MODE = os.environ.get("OMNIWA_WEBHOOK_SIGNATURE_MODE", "off").strip().lower()
CREDENTIALS_DIRECTORY = os.environ.get("CREDENTIALS_DIRECTORY", "").strip()
KEY_ID_PATTERN = re.compile(r"[A-Za-z0-9._-]{1,64}")
CREDENTIAL_PATTERN = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")
DB_LOCK = threading.Lock()


def decode_secret(path):
    with open(path, "rb") as secret_file:
        raw = secret_file.read(1025)
    if len(raw) > 1024:
        raise RuntimeError("signature credential is too large")
    encoded = raw.strip()
    try:
        secret = base64.b64decode(encoded, validate=True)
    except (binascii.Error, ValueError) as error:
        raise RuntimeError("signature secret must be standard base64") from error
    if len(secret) != 32:
        raise RuntimeError("signature secret must decode to exactly 32 bytes")
    return secret


def load_signing_keys():
    if SIGNATURE_MODE not in {"off", "observe", "require"}:
        raise RuntimeError("invalid signature mode")
    if SIGNATURE_MODE == "off":
        return {}

    mapping = os.environ.get("OMNIWA_WEBHOOK_SIGNATURE_KEYS", "").strip()
    if mapping:
        if not CREDENTIALS_DIRECTORY:
            raise RuntimeError("systemd credentials directory is required for a keyring")
        entries = [entry.strip() for entry in mapping.split(",")]
        if not entries or any(not entry for entry in entries) or len(entries) > MAX_KEYS:
            raise RuntimeError("signature keyring must contain between 1 and 8 keys")
        result = {}
        credentials = set()
        for entry in entries:
            if entry.count("=") != 1:
                raise RuntimeError("invalid signature keyring entry")
            key_id, credential = (part.strip() for part in entry.split("=", 1))
            if not KEY_ID_PATTERN.fullmatch(key_id) or not CREDENTIAL_PATTERN.fullmatch(credential):
                raise RuntimeError("invalid signature key ID or credential name")
            if key_id in result or credential in credentials:
                raise RuntimeError("duplicate signature key ID or credential name")
            path = os.path.join(CREDENTIALS_DIRECTORY, credential)
            result[key_id] = decode_secret(path)
            credentials.add(credential)
        return result

    # Backward-compatible single-key configuration for a staged keyring rollout.
    key_id = os.environ.get("OMNIWA_WEBHOOK_SIGNATURE_KEY_ID", "").strip()
    secret_path = os.environ.get("OMNIWA_WEBHOOK_SIGNATURE_SECRET_FILE", "").strip()
    if not secret_path and CREDENTIALS_DIRECTORY:
        secret_path = os.path.join(CREDENTIALS_DIRECTORY, "webhook_signature_secret")
    if not KEY_ID_PATTERN.fullmatch(key_id):
        raise RuntimeError("invalid signature key ID")
    if not secret_path:
        raise RuntimeError("signature secret file is required")
    return {key_id: decode_secret(secret_path)}


SIGNATURE_KEYS = load_signing_keys()


def connect_db():
    connection = sqlite3.connect(DB_PATH, timeout=5)
    connection.execute("PRAGMA journal_mode=WAL")
    connection.execute("PRAGMA synchronous=FULL")
    return connection


def initialize():
    with connect_db() as connection:
        connection.execute(
            "CREATE TABLE IF NOT EXISTS deliveries ("
            "id_hash TEXT PRIMARY KEY, received_at INTEGER NOT NULL)"
        )
        connection.execute(
            "CREATE TABLE IF NOT EXISTS counters ("
            "name TEXT PRIMARY KEY, value INTEGER NOT NULL)"
        )


def increment_counter(name):
    with DB_LOCK, connect_db() as connection:
        connection.execute(
            "INSERT INTO counters(name, value) VALUES (?, 1) "
            "ON CONFLICT(name) DO UPDATE SET value = value + 1",
            (name,),
        )


def verify_signature(headers, body, now=None):
    signature = headers.get("X-Omniwa-Signature", "")
    timestamp = headers.get("X-Omniwa-Signature-Timestamp", "")
    key_id = headers.get("X-Omniwa-Signature-Key-ID", "")
    delivery_id = headers.get("X-Omniwa-Delivery-ID", "")
    presented = any((signature, timestamp, key_id))

    if SIGNATURE_MODE == "off":
        return True, "off", ""
    if not presented and SIGNATURE_MODE == "observe":
        return True, "missing", ""
    if not presented:
        return False, "missing", ""
    if not signature or not timestamp or not key_id:
        return False, "invalid", ""
    secret = SIGNATURE_KEYS.get(key_id)
    if secret is None or not re.fullmatch(r"v1=[0-9a-f]{64}", signature):
        return False, "invalid", ""
    if not re.fullmatch(r"[0-9]{1,20}", timestamp):
        return False, "invalid", ""
    attempt_time = int(timestamp)
    current_time = int(time.time()) if now is None else int(now)
    if abs(current_time - attempt_time) > MAX_CLOCK_SKEW_SECONDS:
        return False, "stale", ""

    canonical = timestamp.encode("ascii") + b"." + delivery_id.encode("ascii") + b"." + body
    expected = "v1=" + hmac.new(secret, canonical, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature, expected):
        return False, "invalid", ""
    return True, "valid", key_id


def record_delivery(delivery_id, payload):
    digest = hashlib.sha256(delivery_id.encode("utf-8")).hexdigest()
    now = int(time.time())
    data = payload.get("data") if isinstance(payload, dict) else None
    if not isinstance(data, dict):
        data = {}
    event = payload.get("event") if isinstance(payload, dict) else None
    phone = data.get("recipientPhoneNumber")
    outcomes = ["total"]
    if event == "SendMessage":
        outcomes.append("send_message")
    if isinstance(phone, str):
        outcomes.append("phone")
        if re.fullmatch(r"[0-9]+", phone):
            outcomes.append("digits")

    with DB_LOCK, connect_db() as connection:
        inserted = connection.execute(
            "INSERT OR IGNORE INTO deliveries(id_hash, received_at) VALUES (?, ?)",
            (digest, now),
        ).rowcount
        if not inserted:
            connection.execute(
                "INSERT INTO counters(name, value) VALUES ('duplicate', 1) "
                "ON CONFLICT(name) DO UPDATE SET value = value + 1"
            )
            return False
        for outcome in outcomes:
            connection.execute(
                "INSERT INTO counters(name, value) VALUES (?, 1) "
                "ON CONFLICT(name) DO UPDATE SET value = value + 1",
                (outcome,),
            )
        connection.execute(
            "DELETE FROM deliveries WHERE received_at < ?", (now - RETENTION_SECONDS,)
        )
    return True


def stats():
    with DB_LOCK, connect_db() as connection:
        counters = dict(connection.execute("SELECT name, value FROM counters"))
        unique = connection.execute("SELECT COUNT(*) FROM deliveries").fetchone()[0]
    return {
        "total": counters.get("total", 0),
        "sendMessage": counters.get("send_message", 0),
        "phone": counters.get("phone", 0),
        "digits": counters.get("digits", 0),
        "duplicates": counters.get("duplicate", 0),
        "uniqueDeliveries": unique,
        "signatureMode": SIGNATURE_MODE,
        "signatureValid": counters.get("signature_valid", 0),
        "signatureMissing": counters.get("signature_missing", 0),
        "signatureInvalid": counters.get("signature_invalid", 0),
        "signatureStale": counters.get("signature_stale", 0),
        "signatureValidByKeyID": {
            key_id: counters.get("signature_valid_key:" + key_id, 0)
            for key_id in sorted(SIGNATURE_KEYS)
        },
    }


def prometheus_metrics():
    snapshot = stats()
    lines = [
        "# HELP omniwa_webhook_receiver_signature_total Signature verification outcomes.",
        "# TYPE omniwa_webhook_receiver_signature_total counter",
    ]
    for outcome, field in (("valid", "signatureValid"), ("missing", "signatureMissing"),
                           ("invalid", "signatureInvalid"), ("stale", "signatureStale")):
        lines.append(
            f'omniwa_webhook_receiver_signature_total{{outcome="{outcome}"}} {snapshot[field]}'
        )
    lines.extend([
        "# HELP omniwa_webhook_receiver_valid_key_total Valid signatures by configured key ID.",
        "# TYPE omniwa_webhook_receiver_valid_key_total counter",
    ])
    for key_id, value in snapshot["signatureValidByKeyID"].items():
        lines.append(f'omniwa_webhook_receiver_valid_key_total{{key_id="{key_id}"}} {value}')
    lines.extend([
        "# HELP omniwa_webhook_receiver_mode Receiver signature enforcement mode.",
        "# TYPE omniwa_webhook_receiver_mode gauge",
    ])
    for mode in ("off", "observe", "require"):
        lines.append(f'omniwa_webhook_receiver_mode{{mode="{mode}"}} {1 if mode == SIGNATURE_MODE else 0}')
    lines.extend([
        "# HELP omniwa_webhook_receiver_duplicate_total Duplicate accepted deliveries.",
        "# TYPE omniwa_webhook_receiver_duplicate_total counter",
        f'omniwa_webhook_receiver_duplicate_total {snapshot["duplicates"]}',
    ])
    return "\n".join(lines) + "\n"


class Handler(BaseHTTPRequestHandler):
    server_version = "OmniWAWebhook/3"

    def log_message(self, *_):
        return

    def setup(self):
        super().setup()
        self.connection.settimeout(REQUEST_TIMEOUT_SECONDS)

    def respond(self, status, body, content_type="application/json"):
        encoded = body if isinstance(body, bytes) else json.dumps(body, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        if self.path == "/healthz":
            self.respond(200, {"status": "ok"})
            return
        if self.path == "/stats":
            self.respond(200, stats())
            return
        if self.path == "/metrics":
            self.respond(200, prometheus_metrics().encode("utf-8"), "text/plain; version=0.0.4")
            return
        self.respond(404, {"error": "not_found"})

    def do_POST(self):
        if self.path != "/omniwa":
            self.respond(404, {"error": "not_found"})
            return
        try:
            content_length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            content_length = 0
        if content_length < 1 or content_length > MAX_BODY:
            self.respond(413, {"error": "invalid_size"})
            return
        protected_headers = (
            "X-Omniwa-Delivery-ID",
            "X-Omniwa-Signature",
            "X-Omniwa-Signature-Timestamp",
            "X-Omniwa-Signature-Key-ID",
        )
        if any(len(self.headers.get_all(name, [])) > 1 for name in protected_headers):
            increment_counter("signature_invalid")
            self.respond(401, {"error": "not_authorized"})
            return
        delivery_id = self.headers.get("X-Omniwa-Delivery-ID", "")
        try:
            uuid.UUID(delivery_id)
        except (ValueError, AttributeError):
            self.respond(400, {"error": "invalid_delivery"})
            return
        try:
            body = self.rfile.read(content_length)
        except (TimeoutError, OSError):
            self.respond(408, {"error": "request_timeout"})
            return
        if len(body) != content_length:
            self.respond(400, {"error": "incomplete_body"})
            return
        accepted, signature_outcome, verified_key_id = verify_signature(self.headers, body)
        increment_counter("signature_" + signature_outcome)
        if verified_key_id:
            increment_counter("signature_valid_key:" + verified_key_id)
        if not accepted:
            self.respond(401, {"error": "not_authorized"})
            return
        try:
            payload = json.loads(body)
        except (json.JSONDecodeError, UnicodeDecodeError):
            self.respond(400, {"error": "invalid_json"})
            return
        record_delivery(delivery_id, payload)
        self.respond(200, {})


class BoundedThreadingHTTPServer(ThreadingHTTPServer):
    request_queue_size = 64

    def __init__(self, server_address, handler):
        super().__init__(server_address, handler)
        self.request_slots = threading.BoundedSemaphore(MAX_CONCURRENT_REQUESTS)

    def process_request(self, request, client_address):
        if not self.request_slots.acquire(blocking=False):
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, client_address)
        except Exception:
            self.request_slots.release()
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.request_slots.release()


def main():
    initialize()
    server = BoundedThreadingHTTPServer((HOST, PORT), Handler)
    server.daemon_threads = True
    server.serve_forever()


if __name__ == "__main__":
    main()
