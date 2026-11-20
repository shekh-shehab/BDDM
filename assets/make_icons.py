#!/usr/bin/env python3
"""Build BDDM icons from the user's dark-theme artwork.

Crops tightly to the rounded square, makes the outside corners transparent,
and writes:
  - assets/icon.png              full icon with "BDDM" text (1024)
  - assets/icon-symbol.png       shield and symbols only (1024)
  - sized PNGs under assets/generated/
  - installer/BDDM.ico           multi-size Windows icon
  - ui/icon.png, ui/icon-32.png  app window / header
  - extension/icons/icon{16,32,48,128}.png
  - icon-preview-*.png           at the repo root (for review; not shipped)
"""

from __future__ import annotations

import io
import struct
import sys
from collections import deque
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = Path(__file__).resolve().parents[1]
SRC = Path(
    "/cursor/stores/user/grok-bot/agents/49934f04-7389-4e36-aa5b-b72244398341/assets/"
    "2189378ad2c06dc7cec4c8ad91694d2e27e4c4aa07aa35b8ee571f43c94c46a6.jpg"
)
# Tight box around the rounded square in the 1024×1024 source, plus a few
# pixels of the grey background so flood-fill can start outside the icon.
CROP = (244, 221, 782, 790)
BG_TOL = 16.0
FEATHER = 2.0


def flood_alpha(rgb: np.ndarray, tol: float, feather: float) -> np.ndarray:
    h, w, _ = rgb.shape
    # Background is the mid-grey canvas around the rounded square.
    border = np.concatenate(
        [
            rgb[0:6].reshape(-1, 3),
            rgb[-6:].reshape(-1, 3),
            rgb[:, 0:6].reshape(-1, 3),
            rgb[:, -6:].reshape(-1, 3),
        ]
    )
    bg = np.median(border, axis=0).astype(np.float32)
    dist = np.linalg.norm(rgb.astype(np.float32) - bg, axis=2)
    vis = np.zeros((h, w), dtype=bool)
    q: deque[tuple[int, int]] = deque()

    def push(y: int, x: int) -> None:
        if 0 <= y < h and 0 <= x < w and not vis[y, x] and dist[y, x] <= tol:
            vis[y, x] = True
            q.append((y, x))

    for x in range(w):
        push(0, x)
        push(h - 1, x)
    for y in range(h):
        push(y, 0)
        push(y, w - 1)
    while q:
        y, x = q.popleft()
        for dy, dx in ((-1, 0), (1, 0), (0, -1), (0, 1)):
            push(y + dy, x + dx)

    alpha = np.full((h, w), 255, dtype=np.uint8)
    alpha[vis] = 0
    # Soften the anti-aliased rim so leftover grey does not halo.
    if feather > 0:
        ring = np.zeros((h, w), dtype=bool)
        ring[1:, :] |= vis[:-1, :]
        ring[:-1, :] |= vis[1:, :]
        ring[:, 1:] |= vis[:, :-1]
        ring[:, :-1] |= vis[:, 1:]
        ring &= ~vis
        for y, x in zip(*np.where(ring)):
            # Map colour distance just above the flood threshold to 0–255.
            t = (dist[y, x] - tol) / max(feather, 0.5)
            t = 0.0 if t < 0 else 1.0 if t > 1 else t
            alpha[y, x] = int(round(t * 255))
    return alpha


def to_square(im: Image.Image, margin: int = 2) -> Image.Image:
    a = np.array(im)
    opaque = a[:, :, 3] > 8
    ys, xs = np.where(opaque)
    if len(xs) == 0:
        return im
    x0, x1 = int(xs.min()), int(xs.max()) + 1
    y0, y1 = int(ys.min()), int(ys.max()) + 1
    cropped = im.crop((x0, y0, x1, y1))
    cw, ch = cropped.size
    side = max(cw, ch) + margin * 2
    out = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    out.paste(cropped, ((side - cw) // 2, (side - ch) // 2), cropped)
    return out


def resize(im: Image.Image, size: int) -> Image.Image:
    if im.size == (size, size):
        return im.copy()
    return im.resize((size, size), Image.Resampling.LANCZOS)


def make_full(src: Image.Image) -> Image.Image:
    crop = src.crop(CROP).convert("RGB")
    rgb = np.array(crop)
    alpha = flood_alpha(rgb, BG_TOL, FEATHER)
    rgba = np.dstack([rgb, alpha])
    return to_square(Image.fromarray(rgba, "RGBA"))


def text_top(full: Image.Image) -> int:
    """First row of the 'BDDM' wordmark, in the square full-icon image."""
    a = np.array(full)
    rgb, alpha = a[:, :, :3], a[:, :, 3]
    br = rgb.mean(axis=2)
    chroma = rgb.max(axis=2).astype(int) - rgb.min(axis=2).astype(int)
    white = (br > 180) & (chroma < 50) & (alpha > 128)
    # The wordmark is the wide white band in the lower half.
    h = full.size[1]
    counts = white.sum(axis=1)
    rows = np.where((counts > 40) & (np.arange(h) > h * 0.55))[0]
    if len(rows) == 0:
        raise SystemExit("could not find the BDDM wordmark in the icon")
    return int(rows[0])


def body_color(full: Image.Image, text_y: int) -> tuple[int, int, int]:
    a = np.array(full)
    h, w, _ = a.shape
    # A quiet band between the shield point and the wordmark.
    y0 = max(0, text_y - 40)
    y1 = max(y0 + 4, text_y - 8)
    band = a[y0:y1, w // 3 : 2 * w // 3]
    keep = band[:, :, 3] > 200
    # Prefer the dark fill, not any leftover glow.
    dark = keep & (band[:, :, :3].mean(axis=2) < 50)
    sample = band[:, :, :3][dark] if dark.any() else band[:, :, :3][keep]
    med = np.median(sample, axis=0)
    return tuple(int(x) for x in med)


def make_symbol(full: Image.Image) -> Image.Image:
    """Rounded square with the shield/symbols only, scaled up to fill it."""
    a = np.array(full)
    h, w, _ = a.shape
    ty = text_top(full)
    fill = body_color(full, ty)
    rgb = a[:, :, :3].astype(int)
    alpha = a[:, :, 3]
    # The shield and its blue glow sit well above the wordmark.
    cutoff = max(0, ty - 16)
    blue = (
        (rgb[:, :, 2] > 70)
        & (rgb[:, :, 2] > rgb[:, :, 0] + 8)
        & (alpha > 128)
        & (np.arange(h)[:, None] < cutoff)
    )
    # Also keep the lighter faces of the arrow / magnet / file.
    bright = (rgb.mean(axis=2) > 70) & (alpha > 128) & (np.arange(h)[:, None] < cutoff)
    content = blue | bright
    ys, xs = np.where(content)
    if len(xs) == 0:
        raise SystemExit("could not find the shield in the icon")
    # A little padding so the glow is not clipped, but never into the letters.
    pad = int(round(0.03 * w))
    x0, x1 = max(0, int(xs.min()) - pad), min(w, int(xs.max()) + 1 + pad)
    y1s = max(0, int(ys.min()) - pad)
    y2s = min(cutoff, int(ys.max()) + 1 + pad)
    shield = full.crop((x0, y1s, x1, y2s))

    # Background: the same rounded square, filled with the body colour.
    bg = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    body = Image.new("RGBA", (w, h), fill + (255,))
    bg.paste(body, (0, 0), Image.fromarray(alpha, "L"))

    # Scale the shield so it fills the square, with a modest inset.
    inset = int(round(0.09 * w))
    box = w - 2 * inset
    sw, sh = shield.size
    scale = min(box / sw, box / sh)
    nw, nh = max(1, int(round(sw * scale))), max(1, int(round(sh * scale)))
    shield = shield.resize((nw, nh), Image.Resampling.LANCZOS)
    # Feather the crop edges so the original subtle background does not
    # show up as a rectangle on the flat fill.
    feather = max(4, int(round(min(nw, nh) * 0.10)))
    mask = Image.new("L", (nw, nh), 255)
    mpx = np.array(mask)
    for i in range(feather):
        v = int(round(255 * (i + 1) / (feather + 1)))
        mpx[i, :] = np.minimum(mpx[i, :], v)
        mpx[nh - 1 - i, :] = np.minimum(mpx[nh - 1 - i, :], v)
        mpx[:, i] = np.minimum(mpx[:, i], v)
        mpx[:, nw - 1 - i] = np.minimum(mpx[:, nw - 1 - i], v)
    sh_a = np.array(shield)
    sh_a[:, :, 3] = (sh_a[:, :, 3].astype(np.uint16) * mpx.astype(np.uint16) // 255).astype(np.uint8)
    shield = Image.fromarray(sh_a, "RGBA")
    px = (w - nw) // 2
    py = (h - nh) // 2
    bg.paste(shield, (px, py), shield)
    return bg


def save_ico(path: Path, images: list[Image.Image]) -> None:
    images = list(images)
    n = len(images)
    payloads = []
    for im in images:
        buf = io.BytesIO()
        im.save(buf, format="PNG")
        payloads.append(buf.getvalue())
    offset = 6 + 16 * n
    header = struct.pack("<HHH", 0, 1, n)
    entries = b""
    for im, data in zip(images, payloads):
        w, h = im.size
        entries += struct.pack(
            "<BBBBHHII",
            0 if w >= 256 else w,
            0 if h >= 256 else h,
            0,
            0,
            1,
            32,
            len(data),
            offset,
        )
        offset += len(data)
    path.write_bytes(header + entries + b"".join(payloads))


def contact_sheet(pairs: list[tuple[str, Image.Image]], dest: Path, bg: tuple[int, int, int]) -> None:
    pad, label_h, cell = 24, 28, 280
    cols = 4
    rows = (len(pairs) + cols - 1) // cols
    sheet = Image.new("RGB", (pad + cols * (cell + pad), pad + rows * (cell + label_h + pad)), bg)
    draw = ImageDraw.Draw(sheet)
    try:
        font = ImageFont.truetype("DejaVuSans.ttf", 16)
    except OSError:
        font = ImageFont.load_default()
    fill = (20, 20, 20) if bg[0] > 128 else (230, 230, 230)
    for i, (name, im) in enumerate(pairs):
        r, c = divmod(i, cols)
        x = pad + c * (cell + pad)
        y = pad + r * (cell + label_h + pad)
        # Checkerboard so transparency is visible, then the icon centered.
        tile = 8
        for yy in range(y, y + cell, tile):
            for xx in range(x, x + cell, tile):
                shade = 200 if bg[0] > 128 else 50
                alt = 230 if bg[0] > 128 else 70
                color = (shade, shade, shade) if ((xx // tile) + (yy // tile)) % 2 == 0 else (alt, alt, alt)
                draw.rectangle([xx, yy, min(xx + tile, x + cell) - 1, min(yy + tile, y + cell) - 1], fill=color)
        shown = im if im.size[0] <= cell else resize(im, cell)
        px = x + (cell - shown.size[0]) // 2
        py = y + (cell - shown.size[1]) // 2
        sheet.paste(shown, (px, py), shown)
        draw.text((x, y + cell + 4), name, fill=fill, font=font)
    sheet.save(dest)


def main() -> int:
    if not SRC.is_file():
        print("missing source artwork:", SRC, file=sys.stderr)
        return 1
    src = Image.open(SRC).convert("RGB")
    full = make_full(src)
    symbol = make_symbol(full)

    full_1024 = resize(full, 1024)
    symbol_1024 = resize(symbol, 1024)
    (ROOT / "assets").mkdir(exist_ok=True)
    full_1024.save(ROOT / "assets" / "icon.png", format="PNG")
    symbol_1024.save(ROOT / "assets" / "icon-symbol.png", format="PNG")

    gen = ROOT / "assets" / "generated"
    gen.mkdir(exist_ok=True)
    full_sizes = (256, 128, 96, 64)
    symbol_sizes = (48, 32, 24, 16)
    sized: dict[int, Image.Image] = {}
    for n in full_sizes:
        sized[n] = resize(full_1024, n)
        sized[n].save(gen / f"icon-full-{n}.png")
    for n in symbol_sizes:
        sized[n] = resize(symbol_1024, n)
        sized[n].save(gen / f"icon-symbol-{n}.png")

    ico_images = [sized[n] for n in (256, 128, 96, 64, 48, 32, 24, 16)]
    save_ico(ROOT / "installer" / "BDDM.ico", ico_images)

    # UI: a large full icon for the window/About, and a 32px symbol for the header.
    (ROOT / "ui").mkdir(exist_ok=True)
    sized[128].save(ROOT / "ui" / "icon.png")
    sized[32].save(ROOT / "ui" / "icon-32.png")

    # Extension: symbol at 16/32/48 (text would be unreadable), full icon at 128.
    ext = ROOT / "extension" / "icons"
    ext.mkdir(parents=True, exist_ok=True)
    sized[16].save(ext / "icon16.png")
    sized[32].save(ext / "icon32.png")
    sized[48].save(ext / "icon48.png")
    sized[128].save(ext / "icon128.png")

    # Previews at the repo root for review.
    pairs = [
        ("256 full", sized[256]),
        ("128 full", sized[128]),
        ("96 full", sized[96]),
        ("64 full", sized[64]),
        ("48 symbol", sized[48]),
        ("32 symbol", sized[32]),
        ("24 symbol", sized[24]),
        ("16 symbol", sized[16]),
    ]
    for name, im in pairs:
        slug = name.replace(" ", "-")
        # Icon on a mid grey so both the dark body and transparent corners show.
        canvas = Image.new("RGBA", (im.size[0] + 16, im.size[1] + 16), (160, 160, 165, 255))
        canvas.paste(im, (8, 8), im)
        canvas.save(ROOT / f"icon-preview-{slug}.png")
    contact_sheet(pairs, ROOT / "icon-preview-sheet-light.png", (245, 245, 247))
    contact_sheet(pairs, ROOT / "icon-preview-sheet-dark.png", (32, 33, 36))
    print("wrote icons and previews")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
