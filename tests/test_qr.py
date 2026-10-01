"""QR code generator used for authenticator enrollment."""
import unittest

from tests.helpers import app  # noqa: F401  (sets sys.path)
import qr

try:
    import cv2
    import numpy as np
except ImportError:  # optional: full decode check
    cv2 = None

URI = ("otpauth://totp/PBS%20Manager%20%28omv%29%3Aadmin?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
       "&issuer=PBS%20Manager&algorithm=SHA1&digits=6&period=30")


def read_format_bits(m):
    """Read the first copy of the 15 format bits around the top-left finder."""
    bits = 0
    coords = [(8, i) for i in range(6)] + [(8, 7), (8, 8), (7, 8)] + [(14 - i, 8) for i in range(9, 15)]
    for i, (x, y) in enumerate(coords):
        bits |= int(m[y][x]) << i
    return bits


def valid_format_words_level_m():
    words = set()
    for mask in range(8):
        data = mask  # level M is 00
        rem = data
        for _ in range(10):
            rem = (rem << 1) ^ ((rem >> 9) * 0x537)
        words.add(((data << 10) | rem) ^ 0x5412)
    return words


class QrStructureTests(unittest.TestCase):
    def test_version_grows_with_length(self):
        for length, size in ((2, 21), (20, 25), (137, 49), (400, 77)):
            with self.subTest(length=length):
                self.assertEqual(len(qr.qr_matrix("a" * length)), size)

    def test_too_long_raises(self):
        with self.assertRaises(ValueError):
            qr.qr_matrix("a" * 2000)

    def test_finder_patterns(self):
        m = qr.qr_matrix(URI)
        n = len(m)
        for ox, oy in ((0, 0), (n - 7, 0), (0, n - 7)):
            for d in range(7):
                self.assertTrue(m[oy][ox + d] and m[oy + 6][ox + d] and m[oy + d][ox] and m[oy + d][ox + 6])
            self.assertTrue(m[oy + 3][ox + 3])

    def test_timing_patterns_alternate(self):
        m = qr.qr_matrix(URI)
        for i in range(8, len(m) - 8):
            self.assertEqual(m[6][i], i % 2 == 0)
            self.assertEqual(m[i][6], i % 2 == 0)

    def test_format_bits_are_valid_for_level_m(self):
        for text in ("hi", URI, "x" * 300):
            self.assertIn(read_format_bits(qr.qr_matrix(text)), valid_format_words_level_m())

    def test_svg(self):
        svg = qr.qr_svg("hello")
        self.assertTrue(svg.startswith("<svg"))
        self.assertIn('viewBox="0 0 29 29"', svg)
        self.assertIn('fill="#000"', svg)


@unittest.skipIf(cv2 is None, "opencv-python-headless not installed")
class QrDecodeTests(unittest.TestCase):
    def decode(self, text):
        m = qr.qr_matrix(text)
        border, scale = 4, 8
        size = (len(m) + 2 * border) * scale
        img = np.full((size, size), 255, np.uint8)
        for y, row in enumerate(m):
            for x, dark in enumerate(row):
                if dark:
                    img[(y + border) * scale:(y + border + 1) * scale, (x + border) * scale:(x + border + 1) * scale] = 0
        value, _, _ = cv2.QRCodeDetector().detectAndDecode(img)
        return value

    def test_decodes_across_versions(self):
        for text in ["hi", URI] + ["Q" * n for n in (40, 90, 150, 250, 400)]:
            with self.subTest(length=len(text)):
                self.assertEqual(self.decode(text), text)


if __name__ == "__main__":
    unittest.main()
