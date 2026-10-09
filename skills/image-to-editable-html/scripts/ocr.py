#!/usr/bin/env python3
"""Detect text lines with RapidOCR (PP-OCR models on onnxruntime, CPU-only,
runs on Linux and macOS).

Usage: ocr.py <image> <out.json>

Writes {"width", "height", "lines": [{"text", "box": [x, y, w, h] (tight
around the glyphs, top-left origin), "confidence", "color": "#rrggbb",
"font_size": px}]} sorted top-to-bottom.
"""
import json
import logging
import os
import sys

import cv2
import numpy as np
from PIL import Image

logging.getLogger("RapidOCR").setLevel(logging.WARNING)


def glyph_mask(rgb, box):
    """Split the box into its two dominant colours; the glyphs are the one
    that does not touch the box border. Returns (mask, glyph colour)."""
    x, y, w, h = box
    crop = rgb[y:y + h, x:x + w].reshape(-1, 3).astype(np.float32)
    if len(crop) < 8:
        return None, (0, 0, 0)
    _, labels, centers = cv2.kmeans(crop, 2, None,
                                    (cv2.TERM_CRITERIA_EPS + cv2.TERM_CRITERIA_MAX_ITER, 20, 1.0),
                                    3, cv2.KMEANS_PP_CENTERS)
    labels = labels.reshape(h, w)
    border = np.concatenate([labels[0], labels[-1], labels[:, 0], labels[:, -1]])
    fg = 1 - np.bincount(border, minlength=2).argmax()
    return labels == fg, tuple(int(v) for v in centers[fg].clip(0, 255))


def row_band(profile):
    """[start, end) of the line's own rows inside a detector box.

    Big display type often gets a box that also swallows a nearby line of
    the same colour (a subtitle above, the next headline word below). The
    line itself is the densest band: find the run of rows above 35% of the
    peak density, then grow it outwards over lighter rows (ascenders,
    descenders, thin serifs) until the ink fades."""
    if profile.max() <= 0:
        return None
    core = np.nonzero(profile >= 0.35 * profile.max())[0]
    runs, start, prev = [], core[0], core[0]
    for i in core[1:]:
        if i - prev > 1:
            runs.append((start, prev + 1))
            start = i
        prev = i
    runs.append((start, prev + 1))
    a, b = max(runs, key=lambda r: profile[r[0]:r[1]].sum())
    # Grow while the ink keeps fading; stop at a valley so the next line of
    # display type right above/below isn't swallowed.
    floor = 0.08 * profile.max()
    while a > 0 and floor < profile[a - 1] <= profile[a] * 1.15:
        a -= 1
    while b < len(profile) and floor < profile[b] <= profile[b - 1] * 1.15:
        b += 1
    return a, b


def tighten(mask, fg, box, display):
    """Shrink a padded detector box to the glyph pixels of its own line.
    Rows come from the glyph-colour mask (`mask`); the horizontal extent
    from the colour-agnostic foreground (`fg`) so a differently coloured
    character in the line (a cyan "7" before white text) is kept. Only
    display-size boxes (`display`) use the valley-aware band — their
    detector boxes are the ones that swallow neighbouring lines; ordinary
    text just takes its first-to-last inked row."""
    x, y, w, h = box
    profile = mask.mean(axis=1)
    if display:
        rows = row_band(profile)
    else:
        on = np.nonzero(profile > 0.01)[0]
        rows = (on[0], on[-1] + 1) if len(on) >= 2 else None
    if rows is None:
        return box
    cols = np.nonzero((fg | mask)[rows[0]:rows[1]].mean(axis=0) > 0.01)[0]
    if len(cols) < 2:
        return box
    c0, c1 = int(cols[0]), int(cols[-1]) + 1
    # Normal-size detector boxes are horizontally tight already (a few px
    # of padding); a big cut means part of the line is in a lighter colour
    # the masks missed ("English Study G...  6:19 PM" with a grey time).
    cut = max(0.1 * w, rows[1] - rows[0])  # more than a character's width
    if not display and c0 > cut:
        c0 = 0
    if not display and w - c1 > cut:
        c1 = w
    return [x + c0, y + int(rows[0]), c1 - c0, int(rows[1] - rows[0])]


def detect(eng, rgb):
    """[(text, score, [x, y, w, h])] for every line RapidOCR reads."""
    H, W = rgb.shape[:2]
    res = eng(rgb)
    raw = []
    for quad, text, score in zip(res.boxes if res.boxes is not None else [], res.txts or [], res.scores or []):
        text = str(text).strip()
        if not text:
            continue
        xs, ys = [p[0] for p in quad], [p[1] for p in quad]
        x0, y0 = max(0, int(min(xs))), max(0, int(min(ys)))
        x1, y1 = min(W, int(np.ceil(max(xs)))), min(H, int(np.ceil(max(ys))))
        if x1 - x0 < 2 or y1 - y0 < 2:
            continue
        raw.append((text, float(score), [x0, y0, x1 - x0, y1 - y0]))
    return raw


def refine(rgb, text, score, box, median_h):
    """Tight box, colour and size of one horizontal line in `rgb`."""
    x0, y0, x1, y1 = box[0], box[1], box[0] + box[2], box[1] + box[3]
    fg, color = glyph_mask(rgb, box)
    if fg is not None:
        crop = rgb[y0:y1, x0:x1].astype(np.float32)
        mask = np.linalg.norm(crop - np.array(color, np.float32), axis=2) < 60
        box = tighten(mask, fg, box, display=box[3] > 3 * median_h)
    return {
        "text": text,
        "box": [int(v) for v in box],
        "confidence": round(float(score), 3),
        "color": "#%02x%02x%02x" % color,
        # The tight box spans ascender to descender; for CJK and
        # mixed-case Latin that is roughly 0.9 of the em.
        "font_size": max(8, int(round(box[3] / 0.9))),
    }


def overlap(a, b):
    """Share of box a covered by box b."""
    ix = max(0, min(a[0] + a[2], b[0] + b[2]) - max(a[0], b[0]))
    iy = max(0, min(a[1] + a[3], b[1] + b[3]) - max(a[1], b[1]))
    return ix * iy / max(1, a[2] * a[3])


def extend(eng, rgb, ln):
    """Grow a display line along its reading direction over more ink of its
    colour (letters partly hidden behind a product stop the detector), then
    read the grown box again."""
    x, y, w, h = ln["box"]
    W = rgb.shape[1]
    c = np.array([int(ln["color"][i:i + 2], 16) for i in (1, 3, 5)], np.float32)
    band = np.linalg.norm(rgb[y:y + h].astype(np.float32) - c, axis=2) < 60
    inked = band.mean(axis=0) > 0.03
    gap = max(3, int(0.6 * h))
    x0, x1 = x, x + w
    for step in (-1, 1):
        i, last, miss = (x - 1, x, 0) if step < 0 else (x + w, x + w - 1, 0)
        while 0 <= i < W and miss <= gap:
            if inked[i]:
                last, miss = i, 0
            else:
                miss += 1
            i += step
        if step < 0:
            x0 = min(x0, last)
        else:
            x1 = max(x1, last + 1)
    if (x0, x1) == (x, x + w):
        return ln
    ln = dict(ln, box=[x0, y, x1 - x0, h])
    pad = max(2, h // 8)
    crop = rgb[max(0, y - pad):y + h + pad, max(0, x0 - pad):x1 + pad]
    best = max(detect(eng, np.ascontiguousarray(crop)), key=lambda d: d[2][2], default=None)
    if best and best[1] >= 0.6 and len(best[0]) > len(ln["text"]):
        ln["text"], ln["confidence"] = best[0], round(best[1], 3)
    return ln


def vertical_lines(eng, rgb, taken, median_h):
    """Lines set sideways (a title running up the edge of a poster): read
    the picture turned a quarter each way, keep confident lines that are
    horizontal there and not already read upright. Their box is the
    upright, tall box on the canvas; `rotate` is how the line is turned
    (-90 = reads bottom to top)."""
    H, W = rgb.shape[:2]
    out = []
    # k=-1: turned clockwise, so bottom-to-top text reads left to right.
    for k, rot in ((-1, -90), (1, 90)):
        r = np.ascontiguousarray(np.rot90(rgb, k))
        for text, score, box in detect(eng, r):
            if score < 0.8 or len(text.replace(" ", "")) < 2 or box[2] < 1.5 * box[3]:
                continue
            ln = refine(r, text, score, box, median_h)
            ln = extend(eng, r, ln)
            rx, ry, rw, rh = ln["box"]
            if k == -1:   # rotated (row i, col j) = source (y = H-1-j, x = i)
                ub = [ry, H - rx - rw, rh, rw]
            else:         # rotated (row i, col j) = source (y = j, x = W-1-i)
                ub = [W - ry - rh, rx, rh, rw]
            if any(overlap(ub, t) > 0.3 or overlap(t, ub) > 0.3 for t in taken):
                continue
            ln["box"], ln["rotate"] = ub, rot
            out.append(ln)
            taken.append(ub)
    return out


def main():
    from rapidocr import RapidOCR

    src, out = sys.argv[1], sys.argv[2]
    img = Image.open(src).convert("RGB")
    W, H = img.size
    rgb = np.asarray(img)
    eng = RapidOCR()
    raw = detect(eng, rgb)
    median_h = float(np.median([b[3] for _, _, b in raw])) if raw else 0.0
    lines = [refine(rgb, text, score, box, median_h) for text, score, box in raw]
    # Upright detections of sideways text are tall boxes of garbage; drop
    # them before looking for sideways lines.
    upright = [l for l in lines if not (l["box"][3] > 2 * l["box"][2] and l["confidence"] < 0.8)]
    lines = upright + vertical_lines(eng, rgb, [l["box"] for l in upright], median_h)
    lines.sort(key=lambda l: (l["box"][1], l["box"][0]))
    with open(out, "w") as f:
        json.dump({"width": W, "height": H, "lines": lines}, f, ensure_ascii=False, indent=1)
    print(f"{len(lines)} text lines -> {out}")


if __name__ == "__main__":
    main()
    # Skip interpreter teardown: onnxruntime (OCR) sometimes aborts there
    # on macOS ("recursive_mutex lock failed").
    sys.stdout.flush()
    sys.stderr.flush()
    os._exit(0)
