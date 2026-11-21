"""Verified downloads with optional, explicitly scoped GitHub API credentials."""

from dataclasses import dataclass
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


ATTEMPTS = 3

SOCKET_TIMEOUT = 45

DOWNLOAD_TIMEOUT = 180

ALLOWED_HOSTS = frozenset(("api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"))

@dataclass(frozen=True)
class Asset:
    name: str
    url: str
    size: int
    sha256: str

class PreparationError(Exception):
    pass

def require_official_https(url):
    try:
        parsed = urllib.parse.urlsplit(url)
        allowed = (parsed.scheme == "https" and parsed.hostname in ALLOWED_HOSTS
                   and not parsed.username and not parsed.password and parsed.port in (None, 443))
    except ValueError:
        allowed = False
    if not allowed:
        raise PreparationError("Release tools may only be downloaded over official GitHub HTTPS endpoints.")

class OfficialRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        require_official_https(new_url)
        redirected = super().redirect_request(request, fp, code, message, headers, new_url)
        if redirected is not None:
            # Even a redirect back to api.github.com must not carry credentials.
            redirected.remove_header("Authorization")
        return redirected

def is_github_asset_url(url):
    parsed = urllib.parse.urlsplit(url)
    return (parsed.scheme == "https" and parsed.hostname == "api.github.com" and not parsed.query and not parsed.fragment
            and re.fullmatch(r"/repos/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/releases/assets/[0-9]+", parsed.path) is not None)

def github_asset_token(url):
    if not is_github_asset_url(url):
        return None
    # Do not discover ambient GH_TOKEN/GITHUB_TOKEN or alternate credentials.
    token = os.environ.get("OMNISHARE_TOOL_GITHUB_TOKEN", "")
    if token and (not token.isascii() or any(character.isspace() or ord(character) < 32 or ord(character) == 127 for character in token)):
        raise PreparationError("The explicitly configured GitHub tool credential has an invalid format.")
    return token or None

def http_error_summary(error):
    """Classify a bounded error response, without echoing its body or URL."""
    safe_headers = {}
    for name in ("X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"):
        value = (error.headers or {}).get(name)
        if isinstance(value, str) and re.fullmatch(r"[0-9]{1,12}", value.strip()):
            safe_headers[name.lower()] = value.strip()
    message = ""
    try:
        data = json.loads(error.read(4096).decode("utf-8", "replace"))
        if isinstance(data, dict) and isinstance(data.get("message"), str):
            message = data["message"].lower()
    except (OSError, ValueError, AttributeError, http.client.HTTPException):
        pass
    code = error.code
    if code == 429 or (code == 403 and (safe_headers.get("x-ratelimit-remaining") == "0" or "rate limit" in message)):
        category = "rate_limit"
    elif code in (401, 403):
        category = "access_denied"
    elif code == 404:
        category = "not_found"
    elif 400 <= code < 500:
        category = "client_error"
    else:
        category = "upstream_error"
    fields = "".join("; " + name + "=" + value for name, value in safe_headers.items())
    return "GitHub HTTP " + str(code) + " [" + category + "]" + fields + "."

def matches_asset(path, asset):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != asset.size:
        return False
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest() == asset.sha256

def download_asset(asset, cache, opener, token=None):
    require_official_https(asset.url)
    temporary = None
    try:
        fd, filename = tempfile.mkstemp(prefix="." + asset.name + ".", suffix=".download", dir=cache)
        temporary = Path(filename)
        request = urllib.request.Request(asset.url, headers={
            "Accept": "application/octet-stream", "User-Agent": "OmniShare-AppImage-Tools",
            "X-GitHub-Api-Version": "2022-11-28",
        })
        if token and is_github_asset_url(asset.url):
            # urllib omits unredirected headers when constructing redirect requests.
            request.add_unredirected_header("Authorization", "Bearer " + token)
        deadline = time.monotonic() + DOWNLOAD_TIMEOUT
        with os.fdopen(fd, "wb") as destination:
            with opener.open(request, timeout=SOCKET_TIMEOUT) as response:
                require_official_https(response.geturl())
                if response.getcode() != 200:
                    raise PreparationError("GitHub did not return a complete asset response.")
                total = 0
                digest = hashlib.sha256()
                # read1 returns after a socket read, allowing a bounded overall
                # deadline even if a peer continually sends very small chunks.
                read = getattr(response, "read1", response.read)
                while True:
                    if time.monotonic() > deadline:
                        raise PreparationError("Asset download exceeded its time limit.")
                    chunk = read(min(1024 * 1024, asset.size - total + 1))
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > asset.size:
                        raise PreparationError("Downloaded asset exceeds its pinned size.")
                    digest.update(chunk)
                    destination.write(chunk)
                if total != asset.size:
                    raise PreparationError("Downloaded asset is truncated or has an unexpected size.")
                if digest.hexdigest() != asset.sha256:
                    raise PreparationError("Downloaded asset does not match its pinned SHA256.")
            destination.flush()
            os.fsync(destination.fileno())
        temporary.chmod(0o755)
        os.replace(temporary, cache / asset.name)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)

def prepare_asset(asset, cache, opener, sleep=time.sleep, attempts=ATTEMPTS):
    target = cache / asset.name
    if matches_asset(target, asset):
        target.chmod(0o755)
        return "verified cache"
    # Tauri legitimately zeroes linuxdeploy's ELF bytes at offsets 8..10. That
    # modified cache is not the published artifact: obtain fresh original bytes.
    require_official_https(asset.url)
    token = github_asset_token(asset.url)
    for attempt in range(attempts):
        try:
            download_asset(asset, cache, opener, token=token)
            return "downloaded and verified"
        except urllib.error.HTTPError as error:
            try:
                failure = http_error_summary(error)
            finally:
                error.close()
            if 400 <= error.code < 500:
                # Neither denial nor exhausted quota improves after a 1s retry.
                # Stop with the same credential; never fall back to anonymous.
                raise PreparationError("Unable to prepare " + asset.name + ". " + failure) from None
        except (urllib.error.URLError, TimeoutError, http.client.HTTPException):
            failure = "The GitHub asset download failed or timed out."
        except OSError:
            failure = "An I/O error interrupted asset preparation."
        except PreparationError as error:
            failure = str(error)
        if attempt + 1 < attempts:
            sleep(2 ** attempt)
    raise PreparationError("Unable to prepare " + asset.name + " after " + str(attempts) + " attempts. " + failure)
