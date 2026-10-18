"""Draws the app icon (magnifier over a storage pie) and writes a Windows .ico.

Small sizes are stored as 32-bit DIBs and 256 px as PNG, which every Windows
version since Vista reads correctly.
"""
import io
import struct
import sys

from PIL import Image, ImageDraw


def lerp(a, b, t):
    return tuple(round(x + (y - x) * t) for x, y in zip(a, b))


def draw(size):
    k = 4  # supersampling
    S = size * k
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))

    # rounded square with a vertical gradient
    top, bottom = (62, 99, 210), (33, 64, 158)
    grad = Image.new("RGBA", (S, S))
    gd = ImageDraw.Draw(grad)
    for y in range(S):
        gd.line([(0, y), (S, y)], fill=lerp(top, bottom, y / (S - 1)) + (255,))
    mask = Image.new("L", (S, S), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, S - 1, S - 1], radius=int(S * 0.23), fill=255)
    img.paste(grad, (0, 0), mask)

    layer = Image.new("RGBA", (S, S), (0, 0, 0, 0))  # shapes go on a layer, then get composited
    d = ImageDraw.Draw(layer)
    small = size <= 24
    cx = cy = S * 0.43
    r = S * (0.25 if small else 0.245)
    ring = S * (0.105 if small else 0.072)
    # lens glass
    d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=(255, 255, 255, 40))
    # storage pie inside the lens (a quarter, like "used space")
    if not small:
        pr = r - ring * 1.25
        d.pieslice([cx - pr, cy - pr, cx + pr, cy + pr], start=-90, end=0, fill=(255, 255, 255, 245))
    # lens ring
    d.ellipse([cx - r, cy - r, cx + r, cy + r], outline=(255, 255, 255, 255), width=max(1, int(ring)))
    # handle
    hw = S * (0.15 if small else 0.11)
    x0 = y0 = cx + r * 0.72
    x1 = y1 = S * 0.80
    d.line([(x0, y0), (x1, y1)], fill=(255, 255, 255, 255), width=int(hw))
    for (px, py) in [(x1, y1)]:
        d.ellipse([px - hw / 2, py - hw / 2, px + hw / 2, py + hw / 2], fill=(255, 255, 255, 255))
    img = Image.alpha_composite(img, layer)
    return img.resize((size, size), Image.LANCZOS)


def dib(img):
    w, h = img.size
    px = img.load()
    header = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0, 0, 0, 0, 0, 0)
    xor = bytearray()
    for y in range(h - 1, -1, -1):
        for x in range(w):
            r, g, b, a = px[x, y]
            xor += bytes((b, g, r, a))
    row = ((w + 31) // 32) * 4
    andmask = bytearray()
    for y in range(h - 1, -1, -1):
        bits = bytearray(row)
        for x in range(w):
            if px[x, y][3] == 0:
                bits[x // 8] |= 0x80 >> (x % 8)
        andmask += bits
    return header + bytes(xor) + bytes(andmask)


def write_ico(path, sizes):
    blobs = []
    for s in sizes:
        im = draw(s)
        if s >= 256:
            buf = io.BytesIO()
            im.save(buf, format="PNG")
            blobs.append((s, buf.getvalue()))
        else:
            blobs.append((s, dib(im)))
    out = bytearray(struct.pack("<HHH", 0, 1, len(blobs)))
    offset = 6 + 16 * len(blobs)
    for s, data in blobs:
        wh = 0 if s >= 256 else s
        out += struct.pack("<BBBBHHII", wh, wh, 0, 0, 1, 32, len(data), offset)
        offset += len(data)
    for _, data in blobs:
        out += data
    with open(path, "wb") as f:
        f.write(out)
    draw(256).save(path.replace(".ico", "-preview.png"))


if __name__ == "__main__":
    write_ico(sys.argv[1], [16, 20, 24, 32, 40, 48, 64, 128, 256])
