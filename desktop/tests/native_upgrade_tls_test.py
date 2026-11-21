"""Real ephemeral-CA/TLS regressions; never touches the machine trust store."""
import importlib.util
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import unittest

spec = importlib.util.spec_from_file_location("native_upgrade", Path(__file__).with_name("native-upgrade-test.py"))
upgrade = importlib.util.module_from_spec(spec)
spec.loader.exec_module(upgrade)


class FixtureTLS(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="omnishare-tls-test-")
        cls.addClassCleanup(cls.directory.cleanup)
        cls.root = Path(cls.directory.name)
        cls.openssl = shutil.which("openssl")
        if not cls.openssl:
            candidate = Path(r"C:\Program Files\Git\usr\bin\openssl.exe")
            if not candidate.is_file():
                raise RuntimeError("OpenSSL is required for real TLS tests")
            cls.openssl = str(candidate)
        upgrade.create_test_tls(cls.root, cls.openssl)

    def connect(self, hostname):
        server = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server.load_cert_chain(self.root / "fixture-server.pem", self.root / "fixture-server.key")
        listener = socket.socket()
        listener.settimeout(5)
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        failures = []

        def accept():
            try:
                raw, _ = listener.accept()
                with server.wrap_socket(raw, server_side=True) as stream:
                    stream.sendall(b"verified")
            except (OSError, ssl.SSLError) as error:
                failures.append(str(error))

        worker = threading.Thread(target=accept, daemon=True)
        worker.start()
        client = ssl.create_default_context(cafile=str(self.root / "fixture-ca.pem"))
        self.assertEqual(client.cert_store_stats()["x509_ca"], 1)
        try:
            with socket.create_connection(listener.getsockname(), timeout=5) as raw:
                with client.wrap_socket(raw, server_hostname=hostname) as stream:
                    return stream.recv(100)
        finally:
            worker.join(timeout=6)
            listener.close()
            self.assertFalse(worker.is_alive(), "TLS fixture thread leaked")

    def test_root_has_one_ca_constraint(self):
        text = subprocess.check_output(
            [self.openssl, "x509", "-in", str(self.root / "fixture-ca.pem"), "-text", "-noout"],
            text=True,
        )
        self.assertEqual(text.count("X509v3 Basic Constraints:"), 1)
        self.assertIn("CA:TRUE", text)

    def test_github_api_and_asset_names_verify(self):
        for host in ("api.github.com", "github.com"):
            with self.subTest(host=host):
                self.assertEqual(self.connect(host), b"verified")

    def test_foreign_hostname_is_rejected(self):
        with self.assertRaises(ssl.SSLCertVerificationError):
            self.connect("not-github.invalid")


if __name__ == "__main__":
    unittest.main()
