#!/usr/bin/env python3
"""Exercise an already installed/running app, then verify the same data after restart.

Only a literal loopback HTTP endpoint is accepted. The private state contains a
random test access key: keep it in RUNNER_TEMP, never in uploaded evidence.
This probe does not start a replacement backend or stand in for native GUI tests.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import stat
import sys
import urllib.error
import urllib.parse
import urllib.request


class ProbeError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise ProbeError(message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class InstalledFeatures:
    def __init__(self, base_url, data_dir, version):
        parsed = urllib.parse.urlsplit(base_url)
        require(parsed.scheme == "http" and parsed.hostname == "127.0.0.1"
                and parsed.port is not None and not parsed.username
                and not parsed.password and parsed.path in ("", "/")
                and not parsed.query and not parsed.fragment,
                "The probe requires an explicit literal loopback HTTP port")
        self.base = base_url.rstrip("/")
        self.data_dir = Path(data_dir).resolve(strict=True)
        self.version = version
        self.auth = {}
        self.checks = {}
        self.current_check = "health_and_embedded_ui"
        self.client = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def request(self, method, path, body=None, headers=None, authenticated=True):
        require(path.startswith("/") and not path.startswith("//"), "Invalid probe endpoint")
        request_headers = dict(self.auth if authenticated else {})
        request_headers.update(headers or {})
        if isinstance(body, dict):
            request_headers["Content-Type"] = "application/json"
            body = json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(self.base + path, data=body, method=method,
                                         headers=request_headers)
        try:
            response = self.client.open(request, timeout=30)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read(16 * 1024 * 1024 + 1)
            require(len(payload) <= 16 * 1024 * 1024, "Probe response exceeded its size limit")
            return response.status, response.headers, payload

    def api(self, method, path, body=None, expected=200, headers=None):
        status, _, raw = self.request(method, path, body, headers)
        require(status == expected, "Unexpected API status: " + str(status))
        result = json.loads(raw)
        require(result.get("code") == 0, "API did not report success")
        return result.get("data")

    def passed(self, name):
        self.checks[name] = True
        self.current_check = name

    def health(self):
        health = self.api("GET", "/api/v1/health")
        require(health.get("status") == "ok" and health.get("version") == self.version
                and health.get("storage") == "sqlite", "Installed backend/version/storage mismatch")
        status, headers, raw = self.request("GET", "/", authenticated=False)
        require(status == 200 and "text/html" in headers.get("Content-Type", "")
                and b"<title>OmniShare</title>" in raw and b"app.js" in raw,
                "Installed backend did not serve the embedded frontend")
        self.passed("health_and_embedded_ui")

    @staticmethod
    def multipart(field, filename, payload):
        boundary = "omnishare-install-" + secrets.token_hex(12)
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="{field}"; '
                f'filename="{filename}"\r\nContent-Type: application/octet-stream\r\n\r\n').encode()
        return body + payload + f"\r\n--{boundary}--\r\n".encode(), {
            "Content-Type": "multipart/form-data; boundary=" + boundary}

    def exercise(self):
        self.health()
        self.current_check = "first_run_authentication"
        require(self.request("GET", "/api/v1/notes", authenticated=False)[0] == 200,
                "Fresh installed data must allow token-free access")
        token_path = self.data_dir / "init-token"
        require(not token_path.exists(), "Obsolete initialization token was created")
        self.auth = {}
        key = secrets.token_urlsafe(36)
        config = self.api("GET", "/api/v1/config")
        require(config.get("has_access_key") is False, "Exercise requires fresh, isolated installation data")
        config = self.api("PUT", "/api/v1/config", {
            "node_name": "OmniShare 安装验收", "port": config["port"],
            "listen_address": "127.0.0.1", "allow_lan": False,
            "auto_open_browser": False, "max_upload_mb": 32,
            "backup_max_mb": 64, "retention_days": 0, "trash_retention_days": 14,
            "allowed_origins": [], "peers": [], "access_key_enabled": True, "access_key": key,
        })
        require(config.get("has_access_key") is True and config.get("access_key_enabled") is True and not token_path.exists(),
                "Optional protection was not enabled without a bootstrap token")
        self.auth = {"X-OmniShare-Key": key}
        require(key not in (self.data_dir / "config.json").read_text(),
                "Configuration stored the access key in plaintext")
        require(self.request("GET", "/api/v1/notes", authenticated=False)[0] == 401,
                "Configured content access must require authentication")
        require(self.request("GET", "/api/v1/notes", headers={"Origin": "https://untrusted.example"})[0] == 403,
                "Cross-origin protected access was not denied")
        self.passed("first_run_authentication")

        self.current_check = "notes_and_trash"
        content = "OmniShare 安装验收：中文、空格 #100% — " + secrets.token_hex(6)
        note = self.api("POST", "/api/v1/notes", {"content": content, "tags": ["安装验收"], "pinned": True}, 201)
        note_path = "/api/v1/notes/" + note["id"]
        self.api("DELETE", note_path)
        require(self.request("GET", note_path + "/manage")[0] == 404, "Deleted note remained active")
        self.api("POST", "/api/v1/trash/note/" + note["id"] + "/restore")
        require(self.api("GET", note_path + "/manage")["content"] == content, "Trash restore lost note content")
        self.passed("notes_and_trash")

        self.current_check = "documents_and_conflicts"
        pad_content = "# 安装后的文档\n\n保存并重启验证。"
        pad = self.api("POST", "/api/v1/pads", {"title": "安装验收文档", "content": "第一版"}, 201)
        pad_path = "/api/v1/pads/" + pad["id"]
        pad = self.api("PUT", pad_path, {"title": "安装验收文档", "content": pad_content, "version": pad["version"]})
        require(self.request("PUT", pad_path, {"title": "冲突", "content": "不应覆盖", "version": pad["version"] - 1})[0] == 409,
                "Document version conflict was not rejected")
        require(self.api("GET", pad_path)["content"] == pad_content, "Conflict changed the document")
        self.passed("documents_and_conflicts")

        self.current_check = "file_upload_download_and_range"
        payload = "OmniShare 文件完整性\n".encode() + bytes(range(256)) * 128
        body, headers = self.multipart("file", "安装测试 #100%.bin", payload)
        file = self.api("POST", "/api/v1/files/upload", body, 201, headers)
        digest = hashlib.sha256(payload).hexdigest()
        require(file["file_hash"] == digest, "Uploaded file digest mismatch")
        download_path = "/api/v1/files/" + file["id"] + "/download"
        status, _, downloaded = self.request("GET", download_path)
        require(status == 200 and hashlib.sha256(downloaded).hexdigest() == digest, "Downloaded file bytes differ")
        status, _, part = self.request("GET", download_path, headers={"Range": "bytes=0-8"})
        require(status == 206 and part == payload[:9], "HTTP range did not return the original bytes")
        self.passed("file_upload_download_and_range")

        self.current_check = "share_access_and_revocation"
        share = self.api("POST", "/api/v1/shares", {
            "object_type": "note", "object_id": note["id"], "ttl_seconds": 3600, "max_access_count": 3}, 201)
        parsed_share = urllib.parse.urlsplit(share["url"])
        require(parsed_share.scheme + "://" + parsed_share.netloc == self.base, "Share URL left the installed server")
        status, _, raw = self.request("GET", parsed_share.path, authenticated=False)
        require(status == 200 and content.encode() in raw, "Share did not expose the intended test content")
        self.api("DELETE", "/api/v1/shares/" + share["id"])
        require(self.request("GET", parsed_share.path, authenticated=False)[0] == 410, "Revoked share remained usable")
        self.passed("share_access_and_revocation")

        self.current_check = "encrypted_backup_restore"
        password = secrets.token_urlsafe(32)
        status, _, backup = self.request("GET", "/api/v1/backup", headers={"X-OmniShare-Backup-Password": password})
        require(status == 200 and backup.startswith(b"OMNISHARE-ENC-BK"), "Encrypted backup failed")
        extra = self.api("POST", "/api/v1/notes", {"content": "备份后临时数据"}, 201)
        body, headers = self.multipart("backup", "installed.osb", backup)
        headers.update({"X-OmniShare-Confirm": "RESTORE-BACKUP", "X-OmniShare-Backup-Password": "wrong-test-password"})
        require(self.request("POST", "/api/v1/restore", body, headers)[0] == 400, "Wrong backup password was accepted")
        self.api("GET", "/api/v1/notes/" + extra["id"] + "/manage")
        headers["X-OmniShare-Backup-Password"] = password
        self.api("POST", "/api/v1/restore", body, 200, headers)
        require(self.request("GET", "/api/v1/notes/" + extra["id"] + "/manage")[0] == 404, "Restore did not roll back later data")
        require(self.api("GET", note_path + "/manage")["content"] == content, "Restored note was lost")
        require(hashlib.sha256(self.request("GET", download_path)[2]).hexdigest() == digest, "Restored file was corrupted")
        self.passed("encrypted_backup_restore")

        # Read immediately before normal exit. The verifier checks the counter
        # after restart, catching child.kill()/SIGKILL that skips Close/flush.
        self.api("GET", note_path)
        counted = self.api("GET", note_path + "/manage")["read_count"]
        return {"schema_version": 1, "version": self.version, "access_key": key,
                "note_id": note["id"], "note_content": content, "note_read_count": counted,
                "file_id": file["id"], "file_sha256": digest,
                "pad_id": pad["id"], "pad_content": pad_content, "pad_version": pad["version"]}

    def verify(self, state):
        require(state.get("schema_version") == 1 and state.get("version") == self.version,
                "Private state does not match this installation version")
        self.auth = {"X-OmniShare-Key": state["access_key"]}
        self.health()
        self.current_check = "configuration_persistence"
        config = self.api("GET", "/api/v1/config")
        require(config.get("node_name") == "OmniShare 安装验收" and config.get("has_access_key") is True
                and config.get("allow_lan") is False, "Saved configuration did not survive restart")
        require(self.request("GET", "/api/v1/notes", authenticated=False)[0] == 401,
                "Restart lost authentication protection")
        self.passed("configuration_persistence")
        self.current_check = "note_and_counter_persistence"
        note = self.api("GET", "/api/v1/notes/" + state["note_id"] + "/manage")
        require(note["content"] == state["note_content"] and note["read_count"] >= state["note_read_count"],
                "Note content or read count did not survive normal exit")
        self.passed("note_and_counter_persistence")
        self.current_check = "file_persistence"
        status, _, payload = self.request("GET", "/api/v1/files/" + state["file_id"] + "/download")
        require(status == 200 and hashlib.sha256(payload).hexdigest() == state["file_sha256"],
                "Uploaded file did not survive restart")
        self.passed("file_persistence")
        self.current_check = "document_persistence"
        pad = self.api("GET", "/api/v1/pads/" + state["pad_id"])
        require(pad["content"] == state["pad_content"] and pad["version"] == state["pad_version"],
                "Document or version did not survive restart")
        self.passed("document_persistence")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--data-dir", required=True, type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--state", required=True, type=Path)
    parser.add_argument("--phase", required=True, choices=("exercise", "verify"))
    parser.add_argument("--report", required=True, type=Path)
    args = parser.parse_args()
    report = {"schema_version": 1, "version": args.version, "phase": args.phase,
              "success": False, "checks": {}, "scope": "installed-backend-http-features"}
    probe = None
    try:
        require(args.state.resolve() != args.report.resolve(), "Private state cannot be the public report")
        probe = InstalledFeatures(args.base_url, args.data_dir, args.version)
        if args.phase == "exercise":
            require(not args.state.exists() and not args.state.is_symlink(), "Private state must be new")
            state = probe.exercise()
            args.state.parent.mkdir(parents=True, exist_ok=True)
            descriptor = os.open(args.state, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
                json.dump(state, stream, ensure_ascii=False)
        else:
            require(args.state.is_file() and not args.state.is_symlink(), "Private state must be a regular file")
            probe.verify(json.loads(args.state.read_text(encoding="utf-8")))
        report["success"] = True
    except Exception as error:
        # Never publish response bodies, URLs with tokens, state, or tracebacks.
        report["failed_check"] = probe.current_check if probe else "input_validation"
        report["error"] = str(error) if isinstance(error, ProbeError) else type(error).__name__
    finally:
        if probe:
            report["checks"] = probe.checks
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False))
    return 0 if report["success"] else 1


if __name__ == "__main__":
    sys.exit(main())
