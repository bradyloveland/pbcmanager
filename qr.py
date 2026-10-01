"""
Minimal QR code generator: byte mode, error correction level M, versions 1-15.
Enough for otpauth:// URIs. Standard library only.
"""

# version: (ec codewords per block, [(block count, data codewords per block), ...])
_ECC_M = {
    1: (10, [(1, 16)]), 2: (16, [(1, 28)]), 3: (26, [(1, 44)]), 4: (18, [(2, 32)]),
    5: (24, [(2, 43)]), 6: (16, [(4, 27)]), 7: (18, [(4, 31)]), 8: (22, [(2, 38), (2, 39)]),
    9: (22, [(3, 36), (2, 37)]), 10: (26, [(4, 43), (1, 44)]), 11: (30, [(1, 50), (4, 51)]),
    12: (22, [(6, 36), (2, 37)]), 13: (22, [(8, 37), (1, 38)]), 14: (24, [(4, 40), (5, 41)]),
    15: (24, [(5, 41), (5, 42)]),
}
_ALIGN = {
    1: [], 2: [6, 18], 3: [6, 22], 4: [6, 26], 5: [6, 30], 6: [6, 34], 7: [6, 22, 38],
    8: [6, 24, 42], 9: [6, 26, 46], 10: [6, 28, 50], 11: [6, 30, 54], 12: [6, 32, 58],
    13: [6, 34, 62], 14: [6, 26, 46, 66], 15: [6, 26, 48, 70],
}


def _gf_mul(x, y):
    z = 0
    for i in range(7, -1, -1):
        z = (z << 1) ^ ((z >> 7) * 0x11D)
        z ^= ((y >> i) & 1) * x
    return z


def _rs_divisor(degree):
    result = [0] * (degree - 1) + [1]
    root = 1
    for _ in range(degree):
        for j in range(degree):
            result[j] = _gf_mul(result[j], root)
            if j + 1 < degree:
                result[j] ^= result[j + 1]
        root = _gf_mul(root, 0x02)
    return result


def _rs_remainder(data, divisor):
    result = [0] * len(divisor)
    for b in data:
        factor = b ^ result.pop(0)
        result.append(0)
        for i, coef in enumerate(divisor):
            result[i] ^= _gf_mul(coef, factor)
    return result


def _encode_codewords(payload):
    for version in range(1, 16):
        ec_len, groups = _ECC_M[version]
        capacity = sum(n * d for n, d in groups)
        cc_bits = 8 if version < 10 else 16
        if 4 + cc_bits + 8 * len(payload) <= capacity * 8:
            break
    else:
        raise ValueError("Data too long for a version 15 QR code")

    bits = []

    def put(value, length):
        bits.extend((value >> i) & 1 for i in range(length - 1, -1, -1))

    put(0b0100, 4)
    put(len(payload), cc_bits)
    for b in payload:
        put(b, 8)
    put(0, min(4, capacity * 8 - len(bits)))
    put(0, (-len(bits)) % 8)
    pad = 0xEC
    while len(bits) < capacity * 8:
        put(pad, 8)
        pad ^= 0xEC ^ 0x11
    data = [int("".join(map(str, bits[i:i + 8])), 2) for i in range(0, len(bits), 8)]

    blocks, pos = [], 0
    divisor = _rs_divisor(ec_len)
    for count, size in groups:
        for _ in range(count):
            chunk = data[pos:pos + size]
            pos += size
            blocks.append((chunk, _rs_remainder(chunk, divisor)))
    out = []
    for i in range(max(len(d) for d, _ in blocks)):
        out.extend(d[i] for d, _ in blocks if i < len(d))
    for i in range(ec_len):
        out.extend(e[i] for _, e in blocks)
    return version, out


class _Matrix:
    def __init__(self, version):
        self.version = version
        self.size = 17 + 4 * version
        self.mod = [[False] * self.size for _ in range(self.size)]
        self.fn = [[False] * self.size for _ in range(self.size)]

    def set_fn(self, x, y, dark):
        self.mod[y][x] = dark
        self.fn[y][x] = True

    def draw_function_patterns(self):
        s = self.size
        for i in range(s):
            self.set_fn(6, i, i % 2 == 0)
            self.set_fn(i, 6, i % 2 == 0)
        for cx, cy in ((3, 3), (s - 4, 3), (3, s - 4)):
            for dy in range(-4, 5):
                for dx in range(-4, 5):
                    x, y = cx + dx, cy + dy
                    if 0 <= x < s and 0 <= y < s:
                        self.set_fn(x, y, max(abs(dx), abs(dy)) not in (2, 4))
        pos = _ALIGN[self.version]
        n = len(pos)
        for i in range(n):
            for j in range(n):
                if (i, j) in ((0, 0), (0, n - 1), (n - 1, 0)):
                    continue
                for dy in range(-2, 3):
                    for dx in range(-2, 3):
                        self.set_fn(pos[i] + dx, pos[j] + dy, max(abs(dx), abs(dy)) != 1)
        self.draw_format(0)
        if self.version >= 7:
            rem = self.version
            for _ in range(12):
                rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
            bits = self.version << 12 | rem
            for i in range(18):
                dark = (bits >> i) & 1 == 1
                a, b = s - 11 + i % 3, i // 3
                self.set_fn(a, b, dark)
                self.set_fn(b, a, dark)

    def draw_format(self, mask):
        data = (0 << 3) | mask  # level M = 00
        rem = data
        for _ in range(10):
            rem = (rem << 1) ^ ((rem >> 9) * 0x537)
        bits = (data << 10 | rem) ^ 0x5412
        bit = lambda i: (bits >> i) & 1 == 1
        s = self.size
        for i in range(6):
            self.set_fn(8, i, bit(i))
        self.set_fn(8, 7, bit(6))
        self.set_fn(8, 8, bit(7))
        self.set_fn(7, 8, bit(8))
        for i in range(9, 15):
            self.set_fn(14 - i, 8, bit(i))
        for i in range(8):
            self.set_fn(s - 1 - i, 8, bit(i))
        for i in range(8, 15):
            self.set_fn(8, s - 15 + i, bit(i))
        self.set_fn(8, s - 8, True)

    def draw_codewords(self, data):
        s, i = self.size, 0
        total = len(data) * 8
        right = s - 1
        while right >= 1:
            if right == 6:
                right = 5
            for vert in range(s):
                for j in range(2):
                    x = right - j
                    upward = ((right + 1) & 2) == 0
                    y = s - 1 - vert if upward else vert
                    if not self.fn[y][x] and i < total:
                        self.mod[y][x] = (data[i >> 3] >> (7 - (i & 7))) & 1 == 1
                        i += 1
            right -= 2

    def apply_mask(self, mask):
        conds = [
            lambda x, y: (x + y) % 2 == 0,
            lambda x, y: y % 2 == 0,
            lambda x, y: x % 3 == 0,
            lambda x, y: (x + y) % 3 == 0,
            lambda x, y: (x // 3 + y // 2) % 2 == 0,
            lambda x, y: x * y % 2 + x * y % 3 == 0,
            lambda x, y: (x * y % 2 + x * y % 3) % 2 == 0,
            lambda x, y: ((x + y) % 2 + x * y % 3) % 2 == 0,
        ]
        cond = conds[mask]
        for y in range(self.size):
            for x in range(self.size):
                if not self.fn[y][x] and cond(x, y):
                    self.mod[y][x] = not self.mod[y][x]

    def penalty(self):
        s, m, score = self.size, self.mod, 0
        lines = [row for row in m] + [[m[y][x] for y in range(s)] for x in range(s)]
        pat_a = [True, False, True, True, True, False, True, False, False, False, False]
        pat_b = pat_a[::-1]
        for line in lines:
            run, prev = 0, None
            for v in line:
                if v == prev:
                    run += 1
                else:
                    if run >= 5:
                        score += 3 + run - 5
                    run, prev = 1, v
            if run >= 5:
                score += 3 + run - 5
            for i in range(len(line) - 10):
                seg = line[i:i + 11]
                if seg == pat_a or seg == pat_b:
                    score += 40
        for y in range(s - 1):
            for x in range(s - 1):
                c = m[y][x]
                if c == m[y][x + 1] == m[y + 1][x] == m[y + 1][x + 1]:
                    score += 3
        dark = sum(sum(row) for row in m)
        score += 10 * (abs(dark * 100 // (s * s) - 50) // 5)
        return score


def qr_matrix(text):
    version, codewords = _encode_codewords(text.encode("utf-8"))
    best, best_score = None, None
    for mask in range(8):
        mtx = _Matrix(version)
        mtx.draw_function_patterns()
        mtx.draw_codewords(codewords)
        mtx.apply_mask(mask)
        mtx.draw_format(mask)
        score = mtx.penalty()
        if best_score is None or score < best_score:
            best, best_score = mtx, score
    return best.mod


def qr_svg(text, border=4):
    modules = qr_matrix(text)
    size = len(modules)
    full = size + 2 * border
    path = "".join(f"M{x + border},{y + border}h1v1h-1z"
                   for y, row in enumerate(modules) for x, dark in enumerate(row) if dark)
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {full} {full}" '
            f'shape-rendering="crispEdges" role="img" aria-label="QR code">'
            f'<rect width="{full}" height="{full}" fill="#fff"/>'
            f'<path d="{path}" fill="#000"/></svg>')
