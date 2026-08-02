#!/usr/bin/env python3
"""Fail-closed health checks for the operator-owned webhook boundary."""

import json
import hashlib
import os
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_STATE = "/var/lib/omniwa-webhook-monitor/state.json"
COUNTERS = ("signatureMissing", "signatureInvalid", "signatureStale")


SAFE_CREDENTIAL = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


def fetch_text(url, timeout=5, headers=None):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(url, headers=headers or {})
    with opener.open(request, timeout=timeout) as response:
        if response.status != 200:
            raise RuntimeError("unexpected HTTP status")
        return response.read(1024 * 1024).decode("utf-8")


def ntp_synchronized():
    result = subprocess.run(
        ["timedatectl", "show", "--property=NTPSynchronized", "--value"],
        check=True,
        capture_output=True,
        text=True,
        timeout=5,
    )
    return result.stdout.strip() == "yes"


def load_state(path):
    if not os.path.exists(path):
        return {}
    with open(path, encoding="utf-8") as state_file:
        state = json.load(state_file)
    if not isinstance(state, dict):
        raise RuntimeError("invalid monitor state")
    return state


def write_state(path, state):
    directory = os.path.dirname(path)
    os.makedirs(directory, mode=0o700, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix="state-", dir=directory, text=True)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as state_file:
            json.dump(state, state_file, sort_keys=True, separators=(",", ":"))
            state_file.write("\n")
        os.replace(temporary, path)
    except Exception:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise


def check(environment, previous, fetch=fetch_text, ntp_check=ntp_synchronized):
    errors = []
    receiver = environment.get("OMNIWA_MONITOR_RECEIVER_URL", "http://127.0.0.1:9000").rstrip("/")
    try:
        health = json.loads(fetch(receiver + "/healthz"))
        if health != {"status": "ok"}:
            errors.append("receiver_health_invalid")
        snapshot = json.loads(fetch(receiver + "/stats"))
    except Exception:
        return ["receiver_unavailable"], previous

    if environment.get("OMNIWA_MONITOR_REQUIRE_SIGNATURE", "true").lower() == "true" and snapshot.get("signatureMode") != "require":
        errors.append("receiver_not_enforcing")

    current = {}
    for counter in COUNTERS:
        value = snapshot.get(counter)
        if not isinstance(value, int) or value < 0:
            errors.append("receiver_counter_invalid")
            continue
        current[counter] = value
        prior = previous.get(counter)
        if isinstance(prior, int) and value > prior:
            errors.append("receiver_" + counter.removeprefix("signature").lower() + "_increased")
        if isinstance(prior, int) and value < prior:
            errors.append("receiver_counter_reset")

    for url in filter(None, (item.strip() for item in environment.get("OMNIWA_MONITOR_API_URLS", "").split(","))):
        try:
            if json.loads(fetch(url.rstrip("/") + "/server/ok")) != {"status": "ok"}:
                errors.append("api_health_invalid")
        except Exception:
            errors.append("api_unavailable")

    expected_egress = environment.get("OMNIWA_MONITOR_EXPECTED_EGRESS_IP", "").strip()
    if expected_egress:
        try:
            actual = fetch(environment.get("OMNIWA_MONITOR_EGRESS_URL", "https://api.ipify.org")).strip()
            if actual != expected_egress:
                errors.append("egress_ip_changed")
        except Exception:
            errors.append("egress_check_failed")

    credentials_directory = environment.get("CREDENTIALS_DIRECTORY", "").strip()
    metrics_targets = environment.get("OMNIWA_MONITOR_METRICS_TARGETS", "").strip()
    if metrics_targets:
        for entry in metrics_targets.split(","):
            try:
                url, credential = (part.strip() for part in entry.rsplit("=", 1))
                parsed = urllib.parse.urlsplit(url)
                if (
                    parsed.scheme != "http"
                    or parsed.hostname not in {"127.0.0.1", "::1", "localhost"}
                    or parsed.username is not None
                    or parsed.password is not None
                    or parsed.path != "/metrics"
                    or parsed.query
                    or parsed.fragment
                    or not SAFE_CREDENTIAL.fullmatch(credential)
                    or not credentials_directory
                ):
                    raise RuntimeError("invalid metrics target")
                with open(os.path.join(credentials_directory, credential), encoding="utf-8") as key_file:
                    api_key = key_file.read(4097).strip()
                if not api_key or len(api_key) > 4096:
                    raise RuntimeError("invalid metrics credential")
                metrics = fetch(url, headers={"apikey": api_key})
                values = parse_outbox_metrics(metrics)
                target = hashlib.sha256(url.encode("utf-8")).hexdigest()[:16]
                state_key = "outbox_dead_letter:" + target
                prior_dead_letter = previous.get(state_key)
                if isinstance(prior_dead_letter, (int, float)) and not isinstance(prior_dead_letter, bool) and values["dead_letter"] > prior_dead_letter:
                    errors.append("outbox_dead_letter_increased")
                current[state_key] = values["dead_letter"]
                if values["oldest_pending"] > 300:
                    errors.append("outbox_pending_too_old")
            except Exception:
                errors.append("outbox_metrics_unavailable")

    try:
        if not ntp_check():
            errors.append("ntp_unsynchronized")
    except Exception:
        errors.append("ntp_check_failed")
    return sorted(set(errors)), current


def parse_outbox_metrics(metrics):
    result = {}
    patterns = {
        "dead_letter": re.compile(
            r'^omniwa_external_event_outbox_rows\{status="dead_letter"\}\s+([0-9.eE+-]+)$'
        ),
        "oldest_pending": re.compile(
            r"^omniwa_external_event_outbox_oldest_pending_age_seconds\s+([0-9.eE+-]+)$"
        ),
    }
    for line in metrics.splitlines():
        for name, pattern in patterns.items():
            match = pattern.fullmatch(line.strip())
            if match:
                result[name] = float(match.group(1))
    if set(result) != set(patterns) or any(value < 0 for value in result.values()):
        raise RuntimeError("required outbox metrics are missing or invalid")
    return result


def main():
    state_path = os.environ.get("OMNIWA_MONITOR_STATE", DEFAULT_STATE)
    try:
        previous = load_state(state_path)
        errors, current = check(os.environ, previous)
        write_state(state_path, current)
    except Exception:
        print("status=failed checks=monitor_internal_error")
        return 1
    if errors:
        print("status=failed checks=" + ",".join(errors))
        return 1
    print("status=ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
