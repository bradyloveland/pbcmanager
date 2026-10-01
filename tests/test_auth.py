"""Passwords, TOTP (RFC 6238) and recovery codes."""
import base64
import re
import unittest
from unittest import mock

from tests.helpers import app


class PasswordHashTests(unittest.TestCase):
    def test_round_trip(self):
        stored = app.hash_password("s3cret-password", iterations=1000)
        self.assertTrue(stored.startswith("pbkdf2_sha256$1000$"))
        self.assertTrue(app.verify_password("s3cret-password", stored))
        self.assertFalse(app.verify_password("wrong", stored))

    def test_salts_differ(self):
        self.assertNotEqual(app.hash_password("x" * 12, 1000), app.hash_password("x" * 12, 1000))

    def test_malformed_hash_is_rejected(self):
        for stored in ("", "plain", "md5$1$a$b", "pbkdf2_sha256$notanumber$a$b"):
            self.assertFalse(app.verify_password("anything", stored))


class TotpTests(unittest.TestCase):
    # RFC 6238 appendix B, SHA-1, secret "12345678901234567890", 8 digits.
    RFC_SECRET = base64.b32encode(b"12345678901234567890").decode()
    RFC_VECTORS = [
        (59, "94287082"), (1111111109, "07081804"), (1111111111, "14050471"),
        (1234567890, "89005924"), (2000000000, "69279037"), (20000000000, "65353130"),
    ]

    def test_rfc6238_vectors(self):
        for t, expected in self.RFC_VECTORS:
            with self.subTest(t=t):
                self.assertEqual(app.totp_code(self.RFC_SECRET, t // 30, digits=8), expected)

    def test_new_secret_is_base32_160_bits(self):
        secret = app.totp_new_secret()
        self.assertRegex(secret, r"^[A-Z2-7]{32}$")
        self.assertEqual(len(base64.b32decode(secret)), 20)

    def _at(self, step):
        return mock.patch.object(app.time, "time", return_value=step * 30 + 5)

    def test_accepts_current_and_adjacent_steps(self):
        secret = app.totp_new_secret()
        with self._at(1000):
            for step in (999, 1000, 1001):
                self.assertEqual(app.totp_match(secret, app.totp_code(secret, step)), step)

    def test_rejects_steps_outside_window(self):
        secret = app.totp_new_secret()
        with self._at(1000):
            for step in (997, 998, 1002, 1003):
                self.assertIsNone(app.totp_match(secret, app.totp_code(secret, step)))

    def test_rejects_replay_of_used_step(self):
        secret = app.totp_new_secret()
        with self._at(1000):
            code = app.totp_code(secret, 1000)
            self.assertIsNone(app.totp_match(secret, code, last_step=1000))
            self.assertIsNone(app.totp_match(secret, app.totp_code(secret, 999), last_step=1000))
            self.assertEqual(app.totp_match(secret, app.totp_code(secret, 1001), last_step=1000), 1001)

    def test_tolerates_spaces_and_rejects_junk(self):
        secret = app.totp_new_secret()
        with self._at(1000):
            code = app.totp_code(secret, 1000)
            self.assertEqual(app.totp_match(secret, code[:3] + " " + code[3:]), 1000)
            for junk in ("", "12345", "1234567", "abcdef", None):
                self.assertIsNone(app.totp_match(secret, junk))
        self.assertIsNone(app.totp_match("", "123456"))

    def test_uri_contents(self):
        uri = app.totp_uri("ABCDEFGH", "admin")
        self.assertTrue(uri.startswith("otpauth://totp/"))
        self.assertIn("secret=ABCDEFGH", uri)
        self.assertIn("issuer=PBS%20Manager", uri)
        self.assertIn("period=30", uri)


class RecoveryCodeTests(unittest.TestCase):
    def test_codes_are_unique_and_formatted(self):
        codes = app.new_recovery_codes()
        self.assertEqual(len(codes), 10)
        self.assertEqual(len(set(codes)), 10)
        for code in codes:
            self.assertRegex(code, r"^[a-z2-9]{5}-[a-z2-9]{5}$")
            self.assertFalse(re.search(r"[01ilo]", code), "ambiguous characters")

    def test_hash_ignores_case_and_separators(self):
        self.assertEqual(app.hash_recovery("abcde-fghjk"), app.hash_recovery("ABCDE FGHJK"))
        self.assertEqual(app.hash_recovery("abcde-fghjk"), app.hash_recovery("abcdefghjk"))
        self.assertNotEqual(app.hash_recovery("abcde-fghjk"), app.hash_recovery("abcde-fghjm"))


if __name__ == "__main__":
    unittest.main()
