#!/usr/bin/env python3
"""Qualify a built bundle with disposable Docker resources; never deploy a live project."""
import argparse
import json
from pathlib import Path
import shlex
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid


def run(*args):
    return subprocess.run([str(a) for a in args], check=True, text=True, capture_output=True).stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("bundle", type=Path)
    parser.add_argument("--work-dir", required=True, type=Path, help="scratch parent on disk, not a small /tmp tmpfs")
    parser.add_argument("--nginx-image", default="nginx:stable-alpine")
    args = parser.parse_args()
    bundle = args.bundle.resolve()
    project = "llewm-proxy-test-" + uuid.uuid4().hex[:12]
    image = "llewm-shared-broker:" + project
    script = bundle / "deploy/docker/deploy.sh"
    args.work_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="proxy-test-", dir=args.work_dir.resolve()) as scratch:
        scratch = Path(scratch)
        tls = scratch / "tls"
        tls.mkdir(mode=0o755)
        cert, key = tls / "fullchain.pem", tls / "privkey.pem"
        run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
            "-subj", "/CN=localhost", "-addext", "subjectAltName=IP:127.0.0.1",
            "-keyout", key, "-out", cert)
        key.chmod(0o600)
        run("docker", "pull", args.nginx_image)
        # Only the generated test key changes owner. Parent stays owned by us for cleanup.
        run("docker", "run", "--rm", "--user", "0:0", "--entrypoint", "chown",
            "-v", f"{tls}:/tls", args.nginx_image, "10001:10001", "/tls/privkey.pem")
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        config = scratch / "docker.env"
        config.write_text("".join(f"{k}={shlex.quote(str(v))}\n" for k, v in {
            "COMPOSE_PROJECT_NAME": project, "BROKER_IMAGE": image, "NGINX_IMAGE": args.nginx_image,
            "BIND_ADDRESS": "127.0.0.1", "HTTPS_PORT": port, "TLS_DIR": tls,
            "PROJECT_ID": "proxy-test", "MACHINE_LABEL": "test-server",
        }.items()))
        compose = ["docker", "compose", "--project-name", project, "--env-file", str(config),
                   "-f", str(bundle / "deploy/docker/compose.yaml")]
        token_path = scratch / "test.token"
        token = ""
        context = ssl.create_default_context(cafile=str(cert))

        def request(path, body=None, credential=None, extra=None):
            headers = dict(extra or {})
            if credential is not None:
                headers["Authorization"] = "Bearer " + credential
            data = None if body is None else json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
            req = urllib.request.Request(f"https://127.0.0.1:{port}" + path, data=data, headers=headers)
            with urllib.request.urlopen(req, context=context, timeout=10) as response:
                return json.load(response)

        def rejects(path, status, extra=None):
            try:
                request(path, extra=extra)
            except urllib.error.HTTPError as error:
                assert error.code == status, (path, error.code)
            else:
                raise AssertionError("Unauthenticated/unexposed request accepted")

        def command(body):
            return request("/v1/broker/commands", {"schema_version": 1, **body}, token)

        try:
            run(script, config, "build", bundle)
            run(script, config, "init")
            run(script, config, "up")
            run(script, config, "enroll", "test-engineer", token_path)
            token = token_path.read_text().strip()
            # Retry only readiness, never an assertion or protocol failure.
            for attempt in range(50):
                try:
                    identity = request("/v1/broker", credential=token)
                    break
                except (urllib.error.URLError, ConnectionError):
                    if attempt == 49:
                        raise
                    time.sleep(0.1)
            rejects("/v1/broker", 403)
            rejects("/v1/broker", 403, {"X-Authenticated-User": "test-engineer", "X-Forwarded-Proto": "https"})
            rejects("/v1/plans", 404)
            for session in ("alice", "bob"):
                command({"request_id": "start-" + session, "action": "state", "session_id": session,
                         "machine": session + "-host", "agent": "smoke", "state": "active", "ttl_seconds": 60})
            before = request("/v1/broker/pull", {"session_id": "bob", "after": 0}, token)
            start = time.monotonic()
            query = {"session_id": "bob", "after": before["next_after"], "broker_id": identity["id"]}
            waited = request("/v1/broker/pull", {**query, "wait_seconds": 1}, token)
            assert waited["reason"] == "timeout" and time.monotonic() - start >= 0.8
            message = {"request_id": "message-1", "action": "message", "session_id": "alice",
                       "to_session": "bob", "text": "Temporary nginx qualification"}
            receipt = command(message)
            assert receipt["status"] == "shared_stored"
            run(script, config, "up")  # Recreates both network namespace participants.
            assert request("/v1/broker", credential=token) == identity
            assert command(message) == receipt
            feed = request("/v1/broker/pull", query, token)
            assert len(feed["events"]) == 1 and feed["events"][0]["id"] == receipt["id"]
            assert command({"request_id": "ack", "action": "ack", "session_id": "bob", "reply_to": receipt["id"]})["status"] == "acknowledged"
            run(bundle / "llewm-orchestrate", "shared-broker-info", "--endpoint", f"https://127.0.0.1:{port}",
                "--project", "proxy-test", "--token-file", token_path, "--ca-file", cert)
            run(script, config, "check")
            print("PASS: non-root Docker, nginx TLS, bearer auth/header spoof rejection, route filtering, bounded wait, message/ack, container recreation, cursor/retry recovery and CLI.")
        finally:
            if sys.exc_info()[0] is not None:
                logs = subprocess.run([*compose, "logs", "--no-color", "--tail", "40"], text=True, capture_output=True)
                print(logs.stdout, file=sys.stderr)
            # Only this invocation's random project, volume and image are removed.
            subprocess.run([*compose, "down", "--volumes", "--remove-orphans"], check=True, capture_output=True)
            subprocess.run(["docker", "image", "rm", image], check=False, capture_output=True)


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        # Commands contain paths and public config only; credentials never use argv/stdout.
        print(error.stderr, end="", file=sys.stderr)
        raise SystemExit(error.returncode) from None
