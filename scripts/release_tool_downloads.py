"""Anonymous, verified downloads shared by pinned platform bundler preparation."""

from dataclasses import dataclass
import hashlib
import http.client
import os
from pathlib import Path
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
        return super().redirect_request(request, fp, code, message, headers, new_url)

def matches_asset(path, asset):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != asset.size:
        return False
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest() == asset.sha256

def download_asset(asset, cache, opener):
    require_official_https(asset.url)
    temporary = None
    try:
        fd, filename = tempfile.mkstemp(prefix="." + asset.name + ".", suffix=".download", dir=cache)
        temporary = Path(filename)
        # No token, authorization header or user credential is consulted.
        request = urllib.request.Request(asset.url, headers={
            "Accept": "application/octet-stream", "User-Agent": "OmniShare-AppImage-Tools",
            "X-GitHub-Api-Version": "2022-11-28",
        })
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
    for attempt in range(attempts):
        try:
            download_asset(asset, cache, opener)
            return "downloaded and verified"
        except urllib.error.HTTPError as error:
            failure = "GitHub returned HTTP " + str(error.code) + "."
            error.close()
        except (urllib.error.URLError, TimeoutError, http.client.HTTPException):
            failure = "The GitHub asset download failed or timed out."
        except OSError:
            failure = "An I/O error interrupted asset preparation."
        except PreparationError as error:
            failure = str(error)
        if attempt + 1 < attempts:
            sleep(2 ** attempt)
    raise PreparationError("Unable to prepare " + asset.name + " after " + str(attempts) + " attempts. " + failure)
