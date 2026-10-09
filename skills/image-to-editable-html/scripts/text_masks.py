"""Per-line glyph masks and text style read from the source pixels."""
import cv2
import numpy as np


def hexc(c):
    return "#%02x%02x%02x" % tuple(int(v) for v in np.clip(c, 0, 255))


def hex2rgb(h):
    return np.array([int(h[i:i + 2], 16) for i in (1, 3, 5)], np.float32)


def line_mask(rgb, ln, display):
    """Glyph pixels of one line, as a full-canvas bool mask.

    Pixels near the line's glyph colour inside the box, plus every
    same-coloured connected component that sits mostly inside the box (a
    letter's foot can stick out of an OCR box). Normal-size text also takes
    the colour-agnostic foreground of the box, which catches multi-colour
    lines (a cyan "7" before white words)."""
    H, W = rgb.shape[:2]
    x, y, w, h = ln["box"]
    ex, ey = int(0.15 * w) + 2, int(0.4 * h) + 2
    if ln.get("rotate"):  # a sideways line: its letters stack along the height
        ex, ey = int(0.4 * w) + 2, int(0.15 * h) + 2
    bx0, by0, bx1, by1 = max(0, x - ex), max(0, y - ey), min(W, x + w + ex), min(H, y + h + ey)
    big = rgb[by0:by1, bx0:bx1].astype(np.float32)
    # Display type sits next to big objects whose colours can be close
    # (caramel next to a red headline): be strict there.
    near = np.linalg.norm(big - hex2rgb(ln["color"]), axis=2) < (50 if display else 60)
    n, labels, stats, _ = cv2.connectedComponentsWithStats(near.astype(np.uint8), connectivity=8)
    inbox = np.zeros(near.shape, bool)
    inbox[y - by0:y - by0 + h, x - bx0:x - bx0 + w] = True
    keep = np.zeros(near.shape, bool)
    for i in range(1, n):
        comp = labels == i
        if (comp & inbox).sum() >= 0.3 * stats[i, cv2.CC_STAT_AREA]:
            keep |= comp
    if not display:
        # Colour-agnostic foreground: the 2-means cluster not on the border.
        crop = rgb[y:y + h, x:x + w].reshape(-1, 3).astype(np.float32)
        if len(crop) >= 8:
            _, lab, _ = cv2.kmeans(crop, 2, None, (cv2.TERM_CRITERIA_EPS + cv2.TERM_CRITERIA_MAX_ITER, 20, 1.0),
                                   3, cv2.KMEANS_PP_CENTERS)
            lab = lab.reshape(h, w)
            border = np.concatenate([lab[0], lab[-1], lab[:, 0], lab[:, -1]])
            fg = lab == (1 - np.bincount(border, minlength=2).argmax())
            keep[y - by0:y - by0 + h, x - bx0:x - bx0 + w] |= fg
    full = np.zeros((H, W), bool)
    full[by0:by1, bx0:bx1] = keep
    return full


def style_of(rgb, mask, ln):
    x, y, w, h = ln["box"]
    g = mask[y:y + h, x:x + w]
    crop = rgb[y:y + h, x:x + w].astype(np.float32)
    if g.sum() < 4:
        return {}
    # Ink = glyph pixels clearly unlike the paper around the box; a loose
    # glyph mask on tiny type otherwise averages to the paper colour.
    p = max(2, int(0.3 * min(w, h)))
    y0, x0 = max(0, y - p), max(0, x - p)
    ring = rgb[y0:y + h + p, x0:x + w + p].astype(np.float32)
    inner = np.zeros(ring.shape[:2], bool)
    inner[y - y0:y - y0 + h, x - x0:x - x0 + w] = True
    paper = np.median(ring[~inner], axis=0) if (~inner).any() else np.median(crop.reshape(-1, 3), axis=0)
    d = np.linalg.norm(crop - paper, axis=2)
    ink = g & (d > max(25.0, 0.5 * float(d[g].max())))
    if ink.sum() < 4:
        ink = g
    rows = np.nonzero(ink.any(axis=1))[0]
    k = max(1, len(rows) // 5)
    top = np.median(crop[rows[:k]][ink[rows[:k]]], axis=0)
    bot = np.median(crop[rows[-k:]][ink[rows[-k:]]], axis=0)
    cover = float(ink.mean())
    st = {"color": hexc(np.median(crop[ink], axis=0)),
          "font_weight": 900 if cover > 0.5 else 700 if cover > 0.38 else 600 if cover > 0.3 else 400}
    if np.abs(top - bot).max() > 40 and not ln.get("rotate"):
        st["gradient"] = [hexc(top), hexc(bot)]
    return st
