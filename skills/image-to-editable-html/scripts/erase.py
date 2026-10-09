"""Erase a masked region and fill it with what plausibly lies behind, using
Bria Eraser on fal.ai: a dedicated object-removal model that fills large
areas cleanly and, unlike prompt-driven fill models, does not invent text.
Pixels outside the mask are kept bit-exact."""
import cv2
import numpy as np
from PIL import Image

import fal

MAX_SIDE = 2048


def erase(rgb, mask, parts=None):
    """rgb: HxWx3 uint8, mask: HxW bool (True = erase). `parts` optionally
    splits the mask into regions judged one by one (one per text line —
    otherwise a small line touching a huge headline is lumped in with it).

    Regions on a smooth surround (text on a UI panel, a product on plain
    paper) are filled locally by continuing that surround — nothing for a
    model to hallucinate: on text-dense UIs Bria writes fresh gibberish, and
    on plain paper it leaves pale ghosts of what it removed. Only regions
    on textured or busy surrounds go to Bria."""
    if not mask.any():
        return rgb.copy()
    out = rgb.copy()
    rest = np.zeros(mask.shape, bool)
    rest_parts = []
    if parts is None:
        n, lab = cv2.connectedComponents(mask.astype(np.uint8))
        parts = [lab == i for i in range(1, n)]
    # A part may be (tight, loose): the tight mask hugs the glyphs and is
    # what a local fill should replace (less to repaint, less to get wrong);
    # the loose one is a padded box for Bria, which given anti-aliased
    # letter edges to look at, redraws ghost letters.
    tight_all = np.zeros(mask.shape, bool)
    for pt in parts:
        tight_all |= pt[0] if isinstance(pt, tuple) else pt
    done = np.zeros(mask.shape, bool)
    for pt in parts:
        if isinstance(pt, tuple):
            tight, loose = pt[0] & ~done, pt[1] & mask & ~done
            got = smooth_fill(rgb, tight, tight_all) if tight.any() else None
            if got is not None and (got[0] == tight).all():
                out[tight] = got[1]
                done |= tight
                continue
            region = loose
        else:
            region = pt & mask & ~done
        if not region.any():
            continue
        done |= region
        got = smooth_fill(rgb, region, mask)
        if got is None:
            rest |= region
            rest_parts.append(region)
            continue
        local, fill = got
        out[local] = fill
        if (region & ~local).any():
            rest |= region & ~local
            rest_parts.append(region & ~local)
    if rest.any():
        # Bria sees the locally filled result: text it can still see next
        # to the hole is text it will happily continue into it.
        out = bria_regions(out, rest_parts)
    return out


def bria_regions(rgb, parts):
    """Bria per part (one object's hole, one text line), each on a crop
    around it with only that part masked. Given one huge mask — a menu of
    fifteen products whose holes run together — Bria paints the products
    back in; given one product with its surroundings it removes it cleanly,
    and at a higher resolution."""
    from concurrent.futures import ThreadPoolExecutor
    H, W = rgb.shape[:2]

    def one(part):
        ys, xs = np.nonzero(part)
        x, y, w, h = xs.min(), ys.min(), xs.max() - xs.min() + 1, ys.max() - ys.min() + 1
        p = int(max(w, h) * 0.6) + 16
        x0, y0, x1, y1 = max(0, x - p), max(0, y - p), min(W, x + w + p), min(H, y + h + p)
        return part, (x0, y0, x1, y1), bria(rgb[y0:y1, x0:x1], part[y0:y1, x0:x1])

    out = rgb.copy()
    # All parts are filled from the same input, so the result doesn't
    # depend on the order; overlapping parts just take the later fill.
    with ThreadPoolExecutor(min(8, len(parts)) or 1) as ex:
        for part, (x0, y0, x1, y1), filled in ex.map(one, [p for p in parts if p.any()]):
            own = part[y0:y1, x0:x1]
            out[y0:y1, x0:x1][own] = filled[own]
    return out


def smooth_fill(rgb, region, mask):
    """Continue a smooth surround (flat UI panel, paper with a gentle
    vignette) into `region`: a quadratic shade fitted robustly to a ring
    around it, plus grain resampled from the ring. Where part of the ring is
    busy (a headline running behind a photo of a product), only the part of
    the region away from it is filled. Returns (filled pixels mask, values)
    or None when the surround is not smooth at all."""
    ys, xs = np.nonzero(region)
    y0, y1, x0, x1 = ys.min(), ys.max() + 1, xs.min(), xs.max() + 1
    p = max(10, int(0.02 * max(y1 - y0, x1 - x0)))
    Y0, Y1, X0, X1 = max(0, y0 - p), min(rgb.shape[0], y1 + p), max(0, x0 - p), min(rgb.shape[1], x1 + p)
    reg = region[Y0:Y1, X0:X1].astype(np.uint8)
    # Sample a ring a few px out, past any anti-aliasing halo.
    ring = (cv2.dilate(reg, np.ones((2 * p + 1, 2 * p + 1), np.uint8)) > 0) & \
        ~(cv2.dilate(reg, np.ones((7, 7), np.uint8)) > 0) & ~mask[Y0:Y1, X0:X1]
    reg = reg > 0
    if ring.sum() < 30:
        return None
    s = float(max(Y1 - Y0, X1 - X0))

    def design(yy, xx):
        u, v = xx / s, yy / s
        return np.stack([np.ones_like(u), u, v, u * u, u * v, v * v], 1)

    ry, rx = np.nonzero(ring)
    A = design(ry.astype(np.float64), rx.astype(np.float64))
    vals = rgb[Y0:Y1, X0:X1][ring].astype(np.float64)
    # Robust fit: a divider line or a neighbour's edge in the ring is an
    # outlier, not a sign of a busy surround.
    keep = np.ones(len(vals), bool)
    for _ in range(4):
        coef, *_ = np.linalg.lstsq(A[keep], vals[keep], rcond=None)
        dev = np.abs(vals - A @ coef).max(axis=1)
        keep = dev <= max(6.0, 3.0 * float(np.median(dev[keep])) * 1.4826)
    resid = (vals - A @ coef)[keep]
    if keep.mean() < 0.4 or resid.std(axis=0).max() > 5.0:
        return None
    local = reg.copy()
    if keep.mean() < 0.85:
        busy = np.zeros(ring.shape, np.uint8)
        busy[ry[~keep], rx[~keep]] = 1
        r = max(12, int(0.04 * s))
        local &= ~(cv2.dilate(busy, cv2.getStructuringElement(cv2.MORPH_ELLIPSE, (2 * r + 1, 2 * r + 1))) > 0)
    qy, qx = np.nonzero(local)
    fill = design(qy.astype(np.float64), qx.astype(np.float64)) @ coef
    fill += resid[np.random.default_rng(0).integers(0, len(resid), len(fill))]
    full = np.zeros(region.shape, bool)
    full[Y0:Y1, X0:X1] = local
    return full, np.clip(fill, 0, 255).round().astype(np.uint8)


def bria(rgb, mask):
    H, W = rgb.shape[:2]
    img, m = Image.fromarray(rgb), Image.fromarray(mask.astype(np.uint8) * 255)
    if max(W, H) > MAX_SIDE:
        k = MAX_SIDE / max(W, H)
        size = (round(W * k), round(H * k))
        img, m = img.resize(size, Image.LANCZOS), m.resize(size, Image.NEAREST)
    r = fal.run("fal-ai/bria/eraser", {"image_url": fal.data_url(img), "mask_url": fal.data_url(m), "mask_type": "manual",
                                        "sync_mode": True})
    out = fal.load(r["image"]["url"])
    if out.size != (W, H):
        out = out.resize((W, H), Image.LANCZOS)
    out = match_tone(rgb, np.asarray(out).astype(np.float32), mask)
    out = blend_in(rgb, out.astype(np.uint8), mask)
    return np.where(mask[..., None], out, rgb).astype(np.uint8)


def blend_in(rgb, fill, mask):
    """Poisson-blend the fill into the original along the mask boundary, so
    a big filled area takes on the brightness of what surrounds it (a dark
    fill against glowing bokeh shows as a hard-edged patch otherwise)."""
    pad = 2
    src = cv2.copyMakeBorder(fill, pad, pad, pad, pad, cv2.BORDER_REFLECT)
    dst = cv2.copyMakeBorder(rgb, pad, pad, pad, pad, cv2.BORDER_REFLECT)
    m = cv2.copyMakeBorder(mask.astype(np.uint8) * 255, pad, pad, pad, pad, cv2.BORDER_CONSTANT, value=0)
    m = cv2.dilate(m, np.ones((3, 3), np.uint8))
    m[:pad + 1], m[-pad - 1:], m[:, :pad + 1], m[:, -pad - 1:] = 0, 0, 0, 0
    if not m.any():
        return fill
    n, lab, stats, _ = cv2.connectedComponentsWithStats(m)
    out = dst.copy()
    H, W = m.shape
    for i in range(1, n):
        x, y, w, h = stats[i, :4]
        if x <= pad + 1 or y <= pad + 1 or x + w >= W - pad - 1 or y + h >= H - pad - 1:
            continue  # touches the image edge: Poisson would smear the edge pixels in
        mi = (lab == i).astype(np.uint8) * 255
        center = (x + w // 2, y + h // 2)
        try:
            # seamlessClone centres the mask's bounding box on `center`.
            out = cv2.seamlessClone(src, out, mi, center, cv2.NORMAL_CLONE)
        except cv2.error:
            out[mi > 0] = src[mi > 0]
    return out[pad:-pad, pad:-pad]


def match_tone(rgb, out, mask):
    """The model's output drifts slightly in colour, which shows as a faint
    patch where the fill meets untouched pixels. Per filled region, shift
    the fill by the drift measured on a thin ring around it."""
    n, lab = cv2.connectedComponents(mask.astype(np.uint8))
    src = rgb.astype(np.float32)
    k = np.ones((9, 9), np.uint8)
    for i in range(1, n):
        region = lab == i
        ring = (cv2.dilate(region.astype(np.uint8), k) > 0) & ~mask
        if ring.sum() < 20:
            continue
        # Median and a cap: the model also repaints things next to the
        # mask (an underline, an object edge), which must not skew this.
        drift = np.median(out[ring] - src[ring], axis=0)
        if np.abs(drift).max() <= 12:
            out[region] -= drift
    return np.clip(out, 0, 255)
