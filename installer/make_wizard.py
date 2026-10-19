"""Draws the side picture of the installer's first and last pages (164x314 BMP).

It reuses the app icon drawing from winres/make_icon.py so the two always match.
Usage: python3 installer/make_wizard.py installer/wizard.bmp   (needs Pillow)
"""
import os
import sys

from PIL import Image, ImageDraw

sys.dont_write_bytecode = True  # no __pycache__ left in winres/
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "winres"))
from make_icon import draw, lerp  # noqa: E402

W, H = 164, 314  # size NSIS Modern UI expects at 96 DPI
K = 4  # supersampling for smooth rings
ICON = 80
CY = 118  # icon centre


def main(out):
    img = Image.new("RGB", (W * K, H * K))
    d = ImageDraw.Draw(img)
    top, bottom = (246, 248, 251), (231, 236, 249)  # app colours --surface-2 -> --accent-soft
    for y in range(H * K):
        d.line([(0, y), (W * K, y)], fill=lerp(top, bottom, y / (H * K - 1)))
    # faint "scan" rings around the icon, fading outwards
    cx, cy = W * K / 2, CY * K
    for r, col in ((56, (214, 221, 233)), (84, (222, 228, 239)), (114, (228, 233, 243))):
        d.ellipse([cx - r * K, cy - r * K, cx + r * K, cy + r * K], outline=col, width=K)
    img = img.resize((W, H), Image.LANCZOS)
    icon = draw(ICON)
    img.paste(icon, (W // 2 - ICON // 2, CY - ICON // 2), icon)
    img.save(out, "BMP")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "wizard.bmp")
