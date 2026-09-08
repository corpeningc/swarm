#!/usr/bin/env python3
"""Generates the swarm app icon: a honeycomb of agents.

Run from desktop/: python build/gen-appicon.py  (needs pillow)

Writes build/appicon.png (Wails uses it for macOS/Linux), build/windows/icon.ico
(the Windows window/taskbar icon), frontend/public/favicon.svg (the webview
favicon) and frontend/public/mark.svg (the sidebar brand mark — no background
tile, so it sits on the sidebar rather than floating on it).

Anything rendered at or below ~32px drops to a 3-cell comb; seven cells turn to
mush at that size.
"""
import math
import struct
from io import BytesIO
from pathlib import Path

from PIL import Image, ImageDraw

# Matches the frontend palette in frontend/src/style.css.
BG_TOP, BG_BOTTOM = (0x23, 0x27, 0x31), (0x16, 0x18, 0x1D)
ACCENT = (0x6A, 0xA3, 0xFF)
BRIGHT = (0x8D, 0xBB, 0xFF)
GREEN = (0x5A, 0xD4, 0x8A)
MID = (0x4E, 0x6C, 0xA0)
DIM = (0x3C, 0x4A, 0x66)

CENTRE = BRIGHT
# Ring cells from the top, clockwise. Varied tones read as agents at different
# stages rather than as a static logo. None drops the cell (small-size comb).
RING = [GREEN, DIM, MID, DIM, ACCENT, MID]
RING_SMALL = [GREEN, None, MID, None, ACCENT, None]

CORNER = 0.20      # rounded-square radius, as a fraction of the tile
CLUSTER = 0.60     # cluster height, as a fraction of the tile
CLUSTER_SMALL = 0.72
INSET = 0.865      # hex radius vs. cell spacing; the remainder is the gap

SS = 4             # supersample factor, for antialiased edges


def hexagon(cx, cy, r):
    """Flat-top hexagon of circumradius r, as a point list."""
    return [(cx + r * math.cos(math.radians(a)), cy + r * math.sin(math.radians(a)))
            for a in range(0, 360, 60)]


def cells(ring, cluster, box):
    """Cell centres and colours for a comb: ([(x, y, colour)], hex_radius)."""
    # A 7-cell comb spans 5*r_cell wide by 5.196*r_cell tall.
    r_cell = cluster * box / 5.196
    spacing = math.sqrt(3) * r_cell
    out = [(box / 2, box / 2, CENTRE)]
    for i, colour in enumerate(ring):
        if colour is None:
            continue
        a = math.radians(270 + 60 * i)
        out.append((box / 2 + spacing * math.cos(a), box / 2 + spacing * math.sin(a), colour))
    return out, INSET * r_cell


def tile(size, ring=RING, cluster=CLUSTER):
    n = size * SS

    column = Image.new("RGB", (1, n))
    for y in range(n):
        t = y / (n - 1)
        column.putpixel((0, y), tuple(round(a + (b - a) * t) for a, b in zip(BG_TOP, BG_BOTTOM)))

    mask = Image.new("L", (n, n), 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, n - 1, n - 1], radius=round(CORNER * n), fill=255)

    img = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    img.paste(column.resize((n, n)), (0, 0), mask)

    draw = ImageDraw.Draw(img)
    placed, r = cells(ring, cluster, n)
    for x, y, colour in placed:
        draw.polygon(hexagon(x, y, r), fill=colour)

    return img.resize((size, size), Image.LANCZOS)


def write_ico(path, images):
    """Writes a PNG-compressed .ico so each size can carry its own artwork.

    Pillow's ICO writer downsamples a single source image, which would force
    the 7-cell comb onto the 16px entry.
    """
    blobs = []
    for img in images:
        buf = BytesIO()
        img.save(buf, format="PNG")
        blobs.append(buf.getvalue())

    offset = 6 + 16 * len(blobs)
    directory = b""
    for img, blob in zip(images, blobs):
        w, h = img.size
        directory += struct.pack("<BBBBHHII", w % 256, h % 256, 0, 0, 1, 32, len(blob), offset)
        offset += len(blob)

    path.write_bytes(struct.pack("<HHH", 0, 1, len(blobs)) + directory + b"".join(blobs))


def hexcolour(rgb):
    return "#%02x%02x%02x" % rgb


def svg(ring, cluster, tiled=True, box=64.0):
    """Hand-rolled SVG on a 64-unit grid, so the mark stays crisp at any size.

    Without the background tile the comb is fitted to the viewBox rather than
    centred on it — a 3-cell comb reaches further above its centre than below,
    so centring alone would leave it sitting visibly low.
    """
    placed, r = cells(ring, cluster, box)
    defs, body = [], []
    open_g = close_g = ""

    if tiled:
        defs.append(
            '  <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">\n'
            f'    <stop offset="0" stop-color="{hexcolour(BG_TOP)}"/>\n'
            f'    <stop offset="1" stop-color="{hexcolour(BG_BOTTOM)}"/>\n'
            '  </linearGradient>'
        )
        body.append(f'  <rect width="{box:g}" height="{box:g}" rx="{CORNER * box:.2f}" fill="url(#bg)"/>')
    else:
        xs = [x for x, _, _ in placed]
        ys = [y for _, y, _ in placed]
        w, h = (max(xs) - min(xs)) + 2 * r, (max(ys) - min(ys)) + 2 * r
        k = box / max(w, h)
        tx = (box - k * w) / 2 - k * (min(xs) - r)
        ty = (box - k * h) / 2 - k * (min(ys) - r)
        open_g = f'  <g transform="translate({tx:.3f} {ty:.3f}) scale({k:.4f})">\n'
        close_g = '\n  </g>'

    points = " ".join(f"{x:.3f},{y:.3f}" for x, y in hexagon(0, 0, r))
    defs.append(f'  <defs><polygon id="c" points="{points}"/></defs>')
    hexes = "\n".join(
        f'    <use href="#c" x="{x:.3f}" y="{y:.3f}" fill="{hexcolour(colour)}"/>'
        for x, y, colour in placed
    )

    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {box:g} {box:g}">\n'
        + "\n".join(defs) + "\n"
        + ("\n".join(body) + "\n" if body else "")
        + open_g + hexes + close_g + "\n</svg>\n"
    )


def main():
    build = Path(__file__).resolve().parent
    desktop = build.parent

    tile(1024).save(build / "appicon.png")

    sizes = [256, 128, 64, 48, 32, 24, 16]
    images = [tile(s) if s > 32 else tile(s, RING_SMALL, CLUSTER_SMALL) for s in sizes]
    (build / "windows").mkdir(exist_ok=True)
    write_ico(build / "windows" / "icon.ico", images)

    public = desktop / "frontend" / "public"
    public.mkdir(parents=True, exist_ok=True)
    (public / "favicon.svg").write_text(svg(RING, CLUSTER), encoding="utf-8")
    # The sidebar mark renders at ~18px, so it takes the 3-cell comb, and drops
    # the tile so it reads as part of the sidebar instead of floating on it.
    (public / "mark.svg").write_text(svg(RING_SMALL, CLUSTER, tiled=False), encoding="utf-8")

    print("wrote appicon.png, windows/icon.ico, frontend/public/{favicon,mark}.svg")


if __name__ == "__main__":
    main()
