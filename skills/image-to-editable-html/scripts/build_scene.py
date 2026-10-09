#!/usr/bin/env python3
"""Build an editable scene: every text line becomes live text, everything
else is cut from the background as whole objects.

Usage: build_scene.py <source.png> <ocr.json> <out_dir> [--extras "logo; round badge|badge; icon"]

Needs FAL_KEY (nano-banana-2 / gpt-image-2, BiRefNet and, for --extras,
SAM 3 on fal.ai).

  1. An image-editing model removes all text (products that were partly
     behind a headline come out whole) and, in parallel, paints the empty
     background. Both are checked to line up with the source; if not, the
     text is erased locally instead (masks + eraser).
  2. Foreground matte of the text-free picture (BiRefNet): every connected
     group of foreground pixels is one element — a cup with its scoop,
     spoon and garnish is one piece.
  3. --extras: flat graphics a matte model treats as background (logos,
     badges, icons, divider lines) are found by name with SAM 3.
  4. Background = the text-free picture with the empty plate blended in
     where the elements were.

Writes scene.json, bg.png, one PNG per element, clean.png, text_mask.png and
objects.png (a labelled preview) into out_dir.
"""
import argparse
import json
import os
import sys
import time
from concurrent.futures import ThreadPoolExecutor

import cv2
import numpy as np
from PIL import Image

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import fal  # noqa: E402
import gen  # noqa: E402
from erase import erase  # noqa: E402
from text_masks import hex2rgb, hexc, line_mask, style_of  # noqa: E402

MIN_SCORE = 0.4
MIN_AREA = 40
SHAPE_STD_MAX = 7.0


def disk(r):
    r = max(1, int(r))
    return cv2.getStructuringElement(cv2.MORPH_ELLIPSE, (2 * r + 1, 2 * r + 1))

def dilate(m, r):
    return cv2.dilate(m.astype(np.uint8), disk(r)) > 0 if r > 0 else m

def fill_holes(m):
    h, w = m.shape
    ff = np.pad(m.astype(np.uint8), 1) * 255
    cv2.floodFill(ff, np.zeros((h + 4, w + 4), np.uint8), (0, 0), 128)
    return ff[1:-1, 1:-1] != 128

def bbox(m):
    ys, xs = np.nonzero(m)
    return [int(xs.min()), int(ys.min()), int(xs.max() - xs.min() + 1), int(ys.max() - ys.min() + 1)]


def matte(rgb):
    """Foreground matte: (alpha HxW uint8, colours HxWx3 uint8), colours
    being BiRefNet's edge-refined foreground (no background halo). Two
    BiRefNet variants, merged: each one occasionally skips an object in a
    busy layout (one product of a 15-item menu grid)."""
    H, W = rgb.shape[:2]
    url = fal.data_url(rgb)

    def one(model):
        r = fal.run("fal-ai/birefnet/v2", {"image_url": url, "model": model, "operating_resolution": "2048x2048",
                                           "output_format": "png", "refine_foreground": True, "sync_mode": True})
        im = fal.load(r["image"]["url"], "RGBA")
        if im.size != (W, H):
            im = im.resize((W, H), Image.LANCZOS)
        return np.asarray(im)

    with ThreadPoolExecutor(2) as ex:
        a, b = ex.map(one, ["General Use (Dynamic)", "General Use (Heavy)"])
    pick_b = b[:, :, 3] > a[:, :, 3]
    out = np.where(pick_b[..., None], b, a)
    return out[:, :, 3].copy(), out[:, :, :3].copy()


def components(alpha, merge_px, min_area):
    """Objects in a foreground matte. Thin bridges (a divider line running
    from one product to the next, a faint floor reflection) are cut first,
    so neighbours stay separate; then loose bits close to an object (a
    strawberry beside its cup, a crumb) join it. Long thin leftovers — lines
    — stay in the background (list them as --extras to cut them out)."""
    H, W = alpha.shape
    solid = alpha >= 128
    r = max(2, int(0.004 * min(H, W)))
    core = cv2.morphologyEx(solid.astype(np.uint8), cv2.MORPH_OPEN, disk(r)) > 0
    n, lab = cv2.connectedComponents(core.astype(np.uint8))
    # Only hairline bridges (a few px of floor reflection between two
    # products) — a spoon handle joining a scoop to its cup is a real,
    # solid part of one object and stays.
    lab = split_necks(lab, n, max(4, int(0.008 * min(H, W))))
    n = lab.max() + 1
    sizes = np.bincount(lab.ravel())
    keep = [i for i in range(1, n) if sizes[i] >= min_area]
    masks = {i: lab == i for i in keep}
    if not masks:
        return []
    # Distance to each core, for attaching the rest.
    ids = np.array(keep)
    dts = np.stack([cv2.distanceTransform((~masks[i]).astype(np.uint8), cv2.DIST_L2, 3) for i in keep])
    nearest, dist = ids[dts.argmin(axis=0)], dts.min(axis=0)
    rest = solid & ~np.isin(lab, ids)
    m2, lab2, stats, _ = cv2.connectedComponentsWithStats(rest.astype(np.uint8))
    for j in range(1, m2):
        x, y, w, h, area = stats[j]
        piece = lab2 == j
        if max(w, h) > 8 * max(1, min(w, h)) and area < 0.3 * w * h + 4 * max(w, h):
            continue  # a line
        d = dist[piece]
        if d.min() > merge_px:
            continue
        owner = nearest[piece][d.argmin()]
        masks[owner] |= piece
    return [fill_holes(m) for m in masks.values()]


def attach_nearby(rgb, objects, keep_out=()):
    """A matting model leaves out things lying on the ground next to an
    object (a heap of matcha powder by the cup, crumbs, a leaf). Pixels
    that stand out from a smooth estimate of the background and form a
    patch close to an object join it, whole. Long thin patches (lines) are
    left alone."""
    H, W = rgb.shape[:2]
    union = np.zeros((H, W), bool)
    for ob in objects:
        union |= ob["mask"]
    blocked = np.zeros((H, W), bool)
    for g in keep_out:
        blocked |= g["mask"]
    f = rgb.astype(np.float32)
    w = (~dilate(union | blocked, max(5, int(0.03 * min(H, W))))).astype(np.float32)
    s = max(10, int(0.05 * min(H, W)))
    est = cv2.GaussianBlur(f * w[..., None], (0, 0), s) / np.maximum(cv2.GaussianBlur(w, (0, 0), s), 1e-3)[..., None]
    fg = (np.linalg.norm(f - est, axis=2) > 35) & ~union & ~dilate(blocked, 2)
    fg = cv2.morphologyEx(fg.astype(np.uint8), cv2.MORPH_OPEN, np.ones((3, 3), np.uint8)) > 0
    n, lab, stats, _ = cv2.connectedComponentsWithStats(cv2.dilate(fg.astype(np.uint8), np.ones((5, 5), np.uint8)))
    dts = [cv2.distanceTransform((~ob["mask"]).astype(np.uint8), cv2.DIST_L2, 3) for ob in objects]
    reach = [max(6.0, 0.06 * float(np.sqrt(ob["mask"].sum()))) for ob in objects]
    for i in range(1, n):
        x, y, w_, h_, area = stats[i]
        if max(w_, h_) > 8 * max(1, min(w_, h_)):
            continue  # a line
        win = np.s_[y:y + h_, x:x + w_]
        piece = (lab[win] == i) & fg[win]
        if piece.sum() < 12:
            continue
        d = [float(dt[win][piece].min()) for dt in dts]
        k = int(np.argmin(d))
        # Must be close, and clearly smaller than what it joins — in area
        # and in extent: a long patch of lit floor is ground, not garnish.
        ob = objects[k]
        if "extent" not in ob:
            ys, xs = np.nonzero(ob["mask"])
            ob["extent"] = max(xs.max() - xs.min(), ys.max() - ys.min()) + 1
        if d[k] <= reach[k] and piece.sum() < 0.2 * ob["mask"].sum() and max(w_, h_) <= 0.4 * ob["extent"]:
            ob["mask"][win] |= piece
    for ob in objects:
        ob.pop("extent", None)
        ob["mask"] = fill_holes(cv2.morphologyEx(ob["mask"].astype(np.uint8), cv2.MORPH_CLOSE, disk(3)) > 0)


def split_necks(lab, n, max_r):
    """Two objects joined by a thin strip of floor or a reflection come out
    of the matte as one blob. Erode each blob step by step up to max_r; if
    it falls apart into two or more substantial pieces, split it along the
    neck (every pixel goes to the nearest piece)."""
    out = lab.copy()
    nxt = n
    for i in range(1, n):
        m = lab == i
        area = m.sum()
        if area < 200:
            continue
        ys, xs = np.nonzero(m)
        y0, y1, x0, x1 = ys.min(), ys.max() + 1, xs.min(), xs.max() + 1
        sub = m[y0:y1, x0:x1].astype(np.uint8)
        for r in range(2, max_r + 1, 2):
            er = cv2.erode(sub, disk(r))
            k, parts, stats, _ = cv2.connectedComponentsWithStats(er)
            big = [j for j in range(1, k) if stats[j, cv2.CC_STAT_AREA] >= 0.12 * er.sum() and
                   stats[j, cv2.CC_STAT_AREA] >= 0.04 * area]
            if len(big) < 2:
                if er.sum() < 0.3 * area:
                    break  # eroded most of it away without a clean split
                continue
            seeds = np.isin(parts, big)
            # Nearest seed for every pixel of the blob.
            _, idx = cv2.distanceTransformWithLabels((~seeds).astype(np.uint8), cv2.DIST_L2, 5,
                                                     labelType=cv2.DIST_LABEL_PIXEL)
            seed_lab = np.zeros(idx.max() + 1, np.int32)
            seed_lab[idx[seeds]] = parts[seeds]
            owner = seed_lab[idx]
            for j, b in enumerate(big):
                piece = (owner == b) & (sub > 0)
                tgt = out[y0:y1, x0:x1]
                tgt[piece] = i if j == 0 else nxt
                if j:
                    nxt += 1
            break
    return out


def sam(rgb, prompt):
    """SAM 3 instances for one text prompt: [(mask, score)]."""
    r = fal.run("fal-ai/sam-3/image", {"image_url": fal.data_url(rgb), "prompt": prompt,
                                       "return_multiple_masks": True, "max_masks": 32, "include_scores": True,
                                       # masks inline: one response instead of a download per mask
                                       "sync_mode": True})
    H, W = rgb.shape[:2]
    scores = r.get("scores") or []
    out = []
    for i, m in enumerate(r.get("masks") or []):
        s = scores[i] if i < len(scores) and scores[i] is not None else 1.0
        im = fal.load(m["url"], "L")
        if im.size != (W, H):
            im = im.resize((W, H), Image.NEAREST)
        mask = np.asarray(im) > 127
        if s >= MIN_SCORE and mask.sum() >= MIN_AREA:
            out.append((mask, float(s)))
    return out

def dist_to(mask, pt):
    H, W = mask.shape
    ys, xs = np.nonzero(mask)
    px, py = pt[0] * W, pt[1] * H
    if mask[min(H - 1, int(py)), min(W - 1, int(px))]:
        return 0.0
    return float(np.min((xs - px) ** 2 + (ys - py) ** 2)) ** 0.5

def sam_any(rgb, name):
    """"avatar|profile picture|person": SAM 3 knows some words and not
    others, so try the alternatives in order until one finds something."""
    for alt in name.split("|"):
        if alt.strip():
            found = sam(rgb, alt.strip())
            if found:
                return found
    return []


def parse_extras(spec):
    """"logo @ 22,6; icon" -> [(name, (x%, y%) or None)]."""
    out = []
    for part in (spec or "").split(";"):
        name, _, at = part.partition("@")
        if not name.strip():
            continue
        pt = None
        if at.strip():
            x, y = (float(v) for v in at.replace("%", "").split(","))
            pt = (x / 100, y / 100)
        out.append((name.strip(), pt))
    return out


def solid_graphic(rgb, m):
    """SAM sometimes returns a sparse mask (an outline, a scatter of
    pixels) for a flat graphic. Within its box, the graphic is whatever
    stands out from the paper around the box and touches the mask."""
    H, W = rgb.shape[:2]
    x, y, w, h = bbox(m)
    p = max(3, int(0.08 * max(w, h)))
    x0, y0, x1, y1 = max(0, x - p), max(0, y - p), min(W, x + w + p), min(H, y + h + p)
    crop = rgb[y0:y1, x0:x1].astype(np.float32)
    edge = np.concatenate([crop[0], crop[-1], crop[:, 0], crop[:, -1]])
    paper = np.median(edge, axis=0)
    if np.abs(edge - paper).max(axis=1).mean() > 25:   # not on a plain ground
        return m
    ink = np.linalg.norm(crop - paper, axis=2) > 30
    ink = cv2.morphologyEx(ink.astype(np.uint8), cv2.MORPH_CLOSE, disk(2)) > 0
    seed = cv2.dilate(m[y0:y1, x0:x1].astype(np.uint8), disk(3)) > 0
    n, lab = cv2.connectedComponents(ink.astype(np.uint8))
    keep = np.isin(lab, np.unique(lab[seed & ink])) & (lab > 0)
    out = m.copy()
    out[y0:y1, x0:x1] |= fill_holes(keep)
    return out


def extras(rgb, specs, taken):
    """SAM 3 instances for the extra names, minus anything the matte
    already has."""
    if not specs:
        return []
    names = list(dict.fromkeys(n for n, _ in specs))
    with ThreadPoolExecutor(len(names)) as ex:
        found = dict(zip(names, ex.map(lambda n: sam_any(rgb, n), names)))
    print("[build_scene] extras found: " + ", ".join(f"{n} x{len(found[n])}" for n in names), file=sys.stderr)
    out = []
    used = taken.copy()
    for name, pt in specs:
        inst = found[name]
        if pt is not None and inst:
            inst = [min(inst, key=lambda ms: dist_to(ms[0], pt))]
        for m, _ in sorted(inst, key=lambda ms: -ms[0].sum()):
            # A badge comes back as its printed ring and letters: close the
            # gaps and fill it, or its middle stays in the background.
            x, y, w, h = bbox(m)
            m = fill_holes(cv2.morphologyEx(m.astype(np.uint8), cv2.MORPH_CLOSE, disk(max(2, int(0.06 * min(w, h))))) > 0)
            m = solid_graphic(rgb, m)
            if (m & used).sum() > 0.5 * m.sum():
                continue
            m &= ~used
            if m.sum() >= MIN_AREA:
                out.append({"name": name.split("|")[0].strip(), "mask": m})
                used |= m
    return out


def text_relations(lines, glyphs, objects):
    """Per line: (objects the text is in front of, objects in front of it),
    each as [(index, overlap area)]."""
    rel = []
    for ln, g in zip(lines, glyphs):
        x, y, w, h = ln["box"]
        box = np.zeros(g.shape, bool)
        box[y:y + h, x:x + w] = True
        g = g & box  # a glyph mask can run into the next line's letters
        above, below = [], []
        for i, ob in enumerate(objects):
            region = box & ob["mask"]
            area = int(region.sum())
            if area < 0.01 * w * h:
                continue
            # Glyph ink visible inside the object: printed over it.
            (above if (g & ob["mask"]).sum() > 0.12 * area else below).append((i, area))
        rel.append((above, below))
    return rel

def text_erase_masks(lines, glyphs, rel, objects, med):
    """Display type: the glyphs, slightly grown (a box would wipe whatever
    sits between huge letters). Normal type: (glyphs grown, padded box) —
    the first for a local fill, the box for the eraser model, which turns
    anti-aliased edges left around a glyph mask into ghost text."""
    parts = []
    for ln, g, (above, below) in zip(lines, glyphs, rel):
        x, y, w, h = ln["box"]
        if h > 2.5 * med:
            m = dilate(g, max(3, min(8, int(round(0.03 * h)))))
            for i, _ in above:  # over an object: erase tightly, keep its edges
                om = objects[i]["mask"]
                m = (m & ~om) | (dilate(g, 2) & om)
        else:
            tight = dilate(g, max(2, int(round(0.15 * h))))
            p = max(3, int(round(0.35 * h)))
            m = np.zeros(g.shape, bool)
            m[max(0, y - p):y + h + p, max(0, x - p):x + w + p] = True
            m |= tight
        for i, _ in below:  # behind an object: never erase the object
            m &= ~objects[i]["mask"]
        if h <= 2.5 * med:
            parts.append((tight & m, m))
        else:
            # Big letters one by one: most of them sit on plain paper and
            # are filled locally (exact); a whole headline handed to the
            # eraser in one piece comes back with pale letter ghosts.
            n, lab = cv2.connectedComponents(m.astype(np.uint8))
            parts += [lab == k for k in range(1, n)]
    return parts

def save_overlay(rgb, objects, path):
    """objects.png: every object tinted and numbered, for a quick review."""
    palette = [(230, 25, 75), (60, 180, 75), (0, 130, 200), (245, 130, 48), (145, 30, 180), (70, 240, 240),
               (240, 50, 230), (210, 245, 60), (0, 128, 128), (170, 110, 40), (128, 0, 0), (0, 0, 128)]
    out = rgb.astype(np.float32) * 0.45
    for k, ob in enumerate(objects):
        c = np.array(palette[k % len(palette)], np.float32)
        out[ob["mask"]] = rgb[ob["mask"]] * 0.35 + c * 0.65
    out = out.astype(np.uint8).copy()
    for k, ob in enumerate(objects):
        x, y, w, h = bbox(ob["mask"])
        cv2.putText(out, f"{k} {ob['name']}", (x + 3, y + 16), cv2.FONT_HERSHEY_SIMPLEX, 0.5, (255, 255, 255), 1,
                    cv2.LINE_AA)
    Image.fromarray(out).save(path)

def shape_style(rgb, mask):
    """CSS fill for a flat, rectangle-like object; None otherwise."""
    h, w = mask.shape
    px = rgb[mask].astype(np.float64)
    if len(px) < 16 or px.std(axis=0).max() > SHAPE_STD_MAX or mask.mean() < 0.75:
        return None
    insets = []
    for fy, fx in ((0, 0), (0, 1), (1, 0), (1, 1)):
        for t in range(min(h, w) // 2):
            if mask[t if fy == 0 else h - 1 - t, t if fx == 0 else w - 1 - t]:
                insets.append(t)
                break
    t = float(np.median(insets)) if insets else 0.0
    return {"background": hexc(px.mean(axis=0).round()), "borderRadius": round(min(t / 0.293, min(w, h) / 2), 1)}


def read_text(rgb, min_score=0.7):
    """OCR boxes [x, y, w, h] of readable lines (to catch leftovers)."""
    from rapidocr import RapidOCR
    if not hasattr(read_text, "eng"):
        read_text.eng = RapidOCR()
    res = read_text.eng(rgb)
    out = []
    for quad, text, score in zip(res.boxes if res.boxes is not None else [], res.txts or [], res.scores or []):
        if score < min_score or len(str(text).strip()) < 2:
            continue
        xs, ys = [q[0] for q in quad], [q[1] for q in quad]
        out.append([int(min(xs)), int(min(ys)), int(max(xs) - min(xs)), int(max(ys) - min(ys))])
    return out


def erase_boxes(rgb, boxes):
    """Erase whole padded boxes (leftover text a model didn't remove)."""
    H, W = rgb.shape[:2]
    parts = []
    for x, y, w, h in boxes:
        p = max(3, int(0.35 * h))
        m = np.zeros((H, W), bool)
        m[max(0, y - p):y + h + p, max(0, x - p):x + w + p] = True
        parts.append(m)
    if not parts:
        return rgb
    mask = np.zeros((H, W), bool)
    for m in parts:
        mask |= m
    return erase(rgb, mask, parts)


def erase_text_locally(src, lines, glyphs, med, merge_px, min_area, out_dir):
    """Fallback when the editing model's result doesn't line up: decide per
    line what is in front from a matte of the source, erase the glyphs."""
    H, W = src.shape[:2]
    alpha0, _ = matte(src)
    letters = np.zeros((H, W), bool)
    for ln, g in zip(lines, glyphs):
        x, y, w, h = ln["box"]
        box = np.zeros((H, W), bool)
        box[y:y + h, x:x + w] = True
        letters |= dilate(g & box, 2)
    rough = [{"mask": m} for m in components(np.where(letters, 0, alpha0).astype(np.uint8), merge_px, min_area)]
    rel = text_relations(lines, glyphs, rough)
    parts = text_erase_masks(lines, glyphs, rel, rough, med)
    text_mask = np.zeros((H, W), bool)
    for m in parts:
        text_mask |= m[1] if isinstance(m, tuple) else m
    if out_dir:
        Image.fromarray(text_mask.astype(np.uint8) * 255).save(os.path.join(out_dir, "text_mask.png"))
    return erase(src, text_mask, parts)


def grow_objects(clean, plate, objects, merge_px, log):
    """Add to each photo object the parts of the picture next to it that
    the empty background removed as well (a plate, a napkin), as long as
    they are not bigger than the object itself."""
    d = np.abs(plate.astype(np.int16) - clean.astype(np.int16)).max(axis=2) > 40
    d = cv2.morphologyEx(d.astype(np.uint8), cv2.MORPH_OPEN, disk(2)) > 0
    d = fill_holes(cv2.morphologyEx(d.astype(np.uint8), cv2.MORPH_CLOSE, disk(3)) > 0)
    others = np.zeros(d.shape, bool)
    for ob in objects:
        others |= ob["mask"]
    n, lab = cv2.connectedComponents((d & ~others).astype(np.uint8))
    for ob in objects:
        if ob["name"] != "object":
            continue
        near = dilate(ob["mask"], merge_px)
        ids = [i for i in np.unique(lab[near & (lab > 0)])]
        add = np.isin(lab, ids) & (lab > 0) if ids else np.zeros(d.shape, bool)
        area = int(ob["mask"].sum())
        if add.any() and add.sum() <= area:
            ob["mask"] = fill_holes(ob["mask"] | add)
            lab[add] = 0
            log(f"  object at {bbox(ob['mask'])}: +{int(add.sum() * 100 / area)}% from the empty background")


def match_tone(model, src, text):
    """The model redraws the whole picture a shade lighter or warmer here
    and there; pasted back only around the letters, that shift would trace
    their outline on a plain ground. Take out the smooth part of the
    difference, measured where nothing was meant to change."""
    d = np.abs(model.astype(np.int16) - src.astype(np.int16)).max(axis=2)
    w = (~dilate(text, 4) & (d < 30)).astype(np.float32)
    sigma = max(8.0, 0.02 * min(src.shape[:2]))
    delta = (src.astype(np.float32) - model.astype(np.float32)) * w[..., None]
    corr = np.zeros_like(delta)
    have = np.zeros(w.shape + (1,), bool)
    # Fine first; inside big letters, far from any reference pixel, a
    # coarser estimate.
    for sg in (sigma, 4 * sigma):
        num = cv2.GaussianBlur(delta, (0, 0), sg)
        den = cv2.GaussianBlur(w, (0, 0), sg)[..., None]
        ok = (den > 0.05) & ~have
        corr = np.where(ok, num / np.maximum(den, 1e-3), corr)
        have |= ok
    return np.clip(model.astype(np.float32) + corr, 0, 255).round().astype(np.uint8)


def text_left(cand, lines, glyphs):
    """Per line: is it still there in `cand`? Upright lines are read again
    with OCR (a pixel test misjudges small type on a photo); a sideways
    line counts as left when most of its ink keeps the text colour."""
    found = read_text(cand, 0.5)
    left = []
    for ln, g in zip(lines, glyphs):
        x, y, w, h = ln["box"]
        if ln.get("rotate"):
            c = hex2rgb(ln["color"])
            ink = g & (np.linalg.norm(cand.astype(np.float32) - c, axis=2) < 40)
            left.append(bool(ink.sum() > 0.5 * max(1, g.sum())) if g.any() else False)
            continue
        hit = False
        for bx, by, bw, bh in found:
            ix = max(0, min(x + w, bx + bw) - max(x, bx))
            iy = max(0, min(y + h, by + bh) - max(y, by))
            if ix * iy > 0.3 * min(w * h, bw * bh):
                hit = True
                break
        left.append(hit)
    return left


def keep_text_removal(src, models, lines, glyphs, med, merge_px, min_area, out_dir, log, retry=None):
    """Merge text-free pictures from the model into the source, line by line.

    For each line the first candidate that did the job is used, only
    around that line's letters (feathered); everywhere else stays the
    source exactly — lettering the OCR missed (the ring of text on a
    badge) is part of that graphic. A candidate fails a line when the line
    can still be read in it, or, for display type, when it drew something
    new where the letters were (a logo where a headline was) or changed
    what is around them. Lines no candidate gets are asked for again by
    name (`retry`), then erased locally."""
    H, W = src.shape[:2]
    src_edges = cv2.Canny(cv2.cvtColor(src, cv2.COLOR_RGB2GRAY), 60, 150) > 0
    zones, boxes = [], []
    for ln, g in zip(lines, glyphs):
        x, y, w, h = ln["box"]
        r = max(4, int(0.15 * min(w, h)))
        p = max(3, int(0.3 * min(w, h)))   # letters can reach past a tight OCR box
        box = np.zeros((H, W), bool)
        box[max(0, y - p):y + h + p, max(0, x - p):x + w + p] = True
        boxes.append(box)
        zones.append(dilate(g & box, r))
    every = np.zeros((H, W), bool)
    for z in zones:
        every |= z
    out = src.copy()
    text_mask = np.zeros((H, W), bool)
    todo = list(range(len(lines)))
    why = {k: [] for k in todo}
    for attempt in range(2):
        for i, m in enumerate(models):
            Image.fromarray(m).save(os.path.join(out_dir, f"notext{attempt + 1}_{i + 1}.png"))
            raw = m
            m = match_tone(m, src, every)
            edges = cv2.Canny(cv2.cvtColor(m, cv2.COLOR_RGB2GRAY), 60, 150) > 0
            d = np.abs(m.astype(np.int16) - src.astype(np.int16)).max(axis=2)
            left = text_left(m, lines, glyphs)
            for k in list(todo):
                ln, z, box = lines[k], zones[k], boxes[k]
                x, y, w, h = ln["box"]
                display = min(w, h) > 2.5 * med
                if left[k]:
                    why[k].append("left it")
                    continue
                if display:
                    # Detail the new picture may have where the letters
                    # were: as much as around the line, or as much as the
                    # source shows between the letters (a scoop the
                    # headline runs over continues behind it).
                    ring = dilate(box, max(6, int(0.5 * min(w, h)))) & ~every & ~box
                    between = z & ~dilate(glyphs[k], 2)
                    around = max(float(src_edges[ring].mean()) if ring.any() else 0.0,
                                 float(src_edges[between].mean()) if between.sum() > 50 else 0.0)
                    inner = float(edges[z].mean())
                    around_box = box & ~dilate(every, 2)
                    moved = float((d[around_box] > 40).mean()) if around_box.any() else 0.0
                    if inner > 2 * around + 0.01:
                        why[k].append(f"drew something new (edges {inner:.3f} vs {around:.3f})")
                        continue
                    if moved > 0.05:
                        why[k].append(f"changed {moved:.0%} of what is around it")
                        continue
                    # A faint copy of the letters (a model sometimes only
                    # fades them): on a plain ground, the letter shapes must
                    # not differ in brightness from the ground between them.
                    gl = glyphs[k] & box
                    plain = ring.any() and float(src_edges[ring].mean()) < 0.01
                    if gl.sum() > 50 and between.sum() > 50 and plain:
                        lum = cv2.cvtColor(m, cv2.COLOR_RGB2LAB)[..., 0].astype(np.float32)
                        ghost = abs(float(np.median(lum[gl])) - float(np.median(lum[between])))
                        if ghost > 3:
                            why[k].append(f"left a faint copy (contrast {ghost:.1f})")
                            continue
                    # Where the letters stood on a plain ground, what replaces
                    # them must be that ground, not a patch in another
                    # colour (a grey band where a sideways word stood).
                    # (Judged on the model's own colours: the tone match
                    # would pull a patch toward the ground.)
                    paper = dilate(gl, max(4, int(0.15 * min(w, h))) + 8) & ~dilate(gl, 4)
                    if gl.sum() > 50 and paper.sum() > 50:
                        ref = np.median(src[paper].astype(np.float32), axis=0)
                        uniform = float((np.abs(src[paper].astype(np.float32) - ref).max(axis=1) < 20).mean())
                        patch = float(np.abs(np.median(raw[gl].astype(np.float32), axis=0) - ref).max())
                        if uniform > 0.75 and patch > 12:
                            why[k].append(f"painted a patch (colour off by {patch:.0f})")
                            continue
                    # Display type: the whole neighbourhood of the letters,
                    # so no faint outline of them is left.
                    tm = dilate(z, 2) & box
                else:
                    changed = cv2.morphologyEx(((d > 20) & box).astype(np.uint8), cv2.MORPH_OPEN,
                                               np.ones((2, 2), np.uint8)) > 0
                    tm = dilate(changed, 3) & box
                wgt = cv2.GaussianBlur(tm.astype(np.float32), (0, 0), 1.5)[..., None]
                wgt = np.maximum(wgt, tm[..., None] * 1.0)
                out = (m * wgt + out * (1 - wgt)).round().astype(np.uint8)
                text_mask |= tm
                todo.remove(k)
        # Display type the model missed is worth asking for again (a local
        # erase of big letters over a photo shows); small type is erased
        # locally just fine.
        big = [k for k in todo if min(lines[k]["box"][2:]) > 2.5 * med]
        if not big or retry is None or attempt == 1:
            break
        log(f"  {len(big)} headline(s) not removed; asking again for them by name")
        models = retry(out, [lines[k]["text"] for k in big])
        src = out   # the second round edits the merged picture
    for k in todo:
        log(f"  text removal failed at '{lines[k]['text'][:20]}': {'; '.join(why[k][-2:]) or 'no result'}")
    if todo:
        out = erase_text_locally(out, [lines[k] for k in todo], [glyphs[k] for k in todo], med, merge_px, min_area,
                                 None)
    Image.fromarray(text_mask.astype(np.uint8) * 255).save(os.path.join(out_dir, "text_mask.png"))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("source")
    ap.add_argument("ocr")
    ap.add_argument("out_dir")
    ap.add_argument("--extras", default="",
                    help='flat graphics to cut out too, ";"-separated, e.g. "logo; round badge|badge; icon"')
    a = ap.parse_args()
    if not os.environ.get("FAL_KEY"):
        sys.exit("FAL_KEY is not set")
    out_dir = a.out_dir
    os.makedirs(out_dir, exist_ok=True)
    src = np.asarray(Image.open(a.source).convert("RGB"))
    H, W = src.shape[:2]
    ocr = json.load(open(a.ocr))
    lines = [l for l in ocr["lines"] if l["confidence"] >= 0.3 and l["text"].strip()]
    med = float(np.median([l["box"][3] for l in lines])) if lines else 0.0
    t0 = time.time()
    log = lambda s: print(f"[build_scene {time.time() - t0:4.0f}s] {s}", file=sys.stderr)  # noqa: E731
    merge_px = max(3, int(0.012 * min(H, W)))
    min_area = max(MIN_AREA, int(0.0008 * H * W))

    glyphs = []
    boxes = np.zeros((H, W), bool)
    for ln in lines:
        g = line_mask(src, ln, ln["box"][3] > 2.5 * med)
        ln.update(style_of(src, g, ln))
        glyphs.append(g)
        x, y, w, h = ln["box"]
        p = max(3, int(0.3 * h))
        boxes[max(0, y - p):y + h + p, max(0, x - p):x + w + p] = True

    # 1. Text-free picture from an image-editing model, checked against the
    # source: anything away from the text must come back unchanged (and
    # the model must have done something — it sometimes returns the input).
    ink = np.zeros((H, W), bool)
    for ln, g_ in zip(lines, glyphs):
        x, y, w, h = ln["box"]
        ink[y:y + h, x:x + w] |= g_[y:y + h, x:x + w]
    models = gen.candidates(src, gen.REMOVE_TEXT, ~dilate(boxes, 6), 2, 12.0, 1.5, log, ink, 0.1)
    if not models:
        log("text removal by model failed the alignment check; erasing locally")
        clean = erase_text_locally(src, lines, glyphs, med, merge_px, min_area, out_dir)
    else:
        def retry(img, texts):
            keep_ = ~dilate(boxes, 6)
            return gen.candidates(img, gen.remove_named(texts), keep_, 2, 12.0, 1.5, log)
        clean = keep_text_removal(src, models, lines, glyphs, med, merge_px, min_area, out_dir, log, retry)
    # The model can miss small type in a busy layout: whatever OCR still
    # reads where the text was is erased locally.
    left = [b for b in read_text(clean) if (boxes[b[1]:b[1] + b[3], b[0]:b[0] + b[2]]).mean() > 0.5]
    if left:
        log(f"{len(left)} text line(s) left by the model; erasing locally")
        clean = erase_boxes(clean, left)
    Image.fromarray(clean).save(os.path.join(out_dir, "clean.png"))
    log("text removed")
    # The empty background is asked of the text-free picture, so text
    # can't come back in it. It runs while the objects are cut out.
    pool = ThreadPoolExecutor(2)
    f_plates = [pool.submit(gen._call, gen.MODELS[0][0], clean, gen.EMPTY_PLATE) for _ in range(2)]

    # 2. Whole objects from the matte of the text-free picture.
    alpha, fg_rgb = matte(clean)
    objects = [{"name": "object", "mask": m} for m in components(alpha, merge_px, min_area)]
    taken = np.zeros((H, W), bool)
    for ob in objects:
        taken |= ob["mask"]
    # Flat graphics the matte leaves in the background.
    graphics = extras(clean, parse_extras(a.extras), dilate(taken, 2))
    # Then what lies right beside the photo objects (powder, crumbs) —
    # never a graphic that was named on its own.
    attach_nearby(clean, objects, graphics)
    objects += graphics
    log(f"{len(objects)} objects")

    # 3. Background: the text-free picture, with the empty plate where the
    # objects were. Generous holes: any glow or soft edge left around one
    # is something a fill would continue — a ghost where it was.
    g = max(4, int(0.008 * min(H, W)))

    def holes():
        photo, flat = np.zeros((H, W), bool), np.zeros((H, W), bool)
        for ob in objects:
            (photo if ob["name"] == "object" else flat)[...] |= ob["mask"]
        soft = (alpha >= 16) & dilate(photo, merge_px)
        return dilate(photo | soft, g) | dilate(flat, int(2.5 * g))
    hole = holes()
    def gather(futures, first):
        out = []
        for i, f in enumerate(futures):
            try:
                plate = f.result()
            except fal.AccountError:
                raise
            except Exception as e:  # noqa: BLE001
                log(f"empty background failed: {e}")
                continue
            Image.fromarray(plate).save(os.path.join(out_dir, f"plate{first + i}.png"))
            diff, shift = gen.misalignment(clean, plate, ~dilate(hole, 3 * g))
            log(f"empty background {first + i}: diff {diff:.1f}, shift {shift:.1f}px")
            if diff <= 14 and shift <= 1.5:
                out.append(plate)
        return out
    ok_plates = gather(f_plates, 1)
    pool.shutdown()
    photo_all = np.zeros((H, W), bool)
    for ob in objects:
        if ob["name"] == "object":
            photo_all |= ob["mask"]
    if ok_plates:
        # What the plate took away next to an object belongs to it: the
        # matte often keeps the food but drops the dish or napkin under it,
        # which would stay behind in the background as a ring. Read from
        # the plate that removed the most.
        best = max(ok_plates, key=lambda p_: float(
            np.abs(p_.astype(np.int16) - clean.astype(np.int16)).max(axis=2)[photo_all].mean()) if photo_all.any() else 0)
        grow_objects(clean, best, objects, merge_px, log)
        hole = holes()
        taken = np.zeros((H, W), bool)
        for ob in objects:
            taken |= ob["mask"]
            if ob["name"] == "object":
                photo_all |= ob["mask"]
    bg = clean
    rest = hole
    good = np.zeros((H, W), bool)
    # Use a plate only where it really removed the object (in a busy layout
    # the model leaves some products standing; its output also varies, so
    # each hole takes the first plate that got it right, and holes of
    # photo objects no plate got are asked for once more); the rest is
    # erased locally. Judged by looking at the plate itself: no foreground
    # left in a hole (a matte of the plate), and no new structure (edges)
    # that the surroundings don't have — a model sometimes draws a frame
    # where a cup was.
    src_edges = cv2.Canny(cv2.cvtColor(clean, cv2.COLOR_RGB2GRAY), 60, 150) > 0
    # Lines the picture already has outside the objects (a grid, a frame)
    # are expected in a plate too.
    every_ob = np.zeros((H, W), bool)
    for ob in objects:
        every_ob |= ob["mask"]
    known = dilate(src_edges & ~dilate(every_ob, 2 * g), 2)
    # One region per object, so one product the plate kept doesn't cost
    # the whole row of them sharing a hole.
    regions, region_ob = [], []
    for k_, ob in enumerate(objects):
        if ob["name"] == "object":
            r_ = dilate(ob["mask"] | ((alpha >= 16) & dilate(ob["mask"], merge_px)), g)
        else:
            r_ = dilate(ob["mask"], int(2.5 * g))
        r_ &= hole
        if r_.any():
            regions.append(r_)
            region_ob.append(k_)
    plain_e = src_edges[~dilate(hole, 3 * g)].mean() if (~hole).any() else 0.0
    judged = [(p_, matte(p_)[0], cv2.Canny(cv2.cvtColor(p_, cv2.COLOR_RGB2GRAY), 60, 150) > 0)
              for p_ in ok_plates]
    failed = []
    for region, k_ in zip(regions, region_ob):
        ring = dilate(region, 3 * g) & ~region & ~hole
        ring_e = src_edges[ring].mean() if ring.sum() > 100 else plain_e
        why = []
        for plate, plate_alpha, edges in judged:
            # What stood there and still stands (not a dark corner the
            # matte of a busy plate happens to pick up).
            fg_left = ((plate_alpha >= 128) & (alpha >= 128))[region].mean()
            inner_e = (edges & ~known)[region].mean()
            # A plate with no structure at all where the object was, in
            # the colour of its surroundings, is empty even if the matte
            # reads some "foreground" into a big plain patch.
            plain = inner_e <= ring_e + 0.002 and fg_left < 0.4 and float(np.abs(
                np.median(plate[region].astype(np.float32), axis=0)
                - np.median(clean[ring].astype(np.float32), axis=0)).max() if ring.sum() > 100 else 99) <= 25
            if (fg_left < 0.03 and inner_e <= 2 * ring_e + 0.004) or plain:
                # The plate lines up with the picture, so a soft edge
                # is all the blending it needs (Poisson blending would
                # drag in the colours of whatever the hole touches at
                # the image border).
                w = cv2.GaussianBlur(dilate(region, g).astype(np.float32), (0, 0), g)[..., None] \
                    * dilate(region, 2 * g)[..., None]
                w = np.maximum(w, region[..., None] * 1.0)
                bg = (plate * w + bg * (1 - w)).round().astype(np.uint8)
                good |= region
                break
            why.append(f"foreground {fg_left:.2f}, edges {inner_e:.3f} vs {ring_e:.3f}")
        else:
            log(f"  plates rejected for a hole at {bbox(region)}: {' | '.join(why) or 'no plate'}")
            failed.append((region, k_))
    # Flat graphics are kept by a plate on purpose; a photo object's
    # hole the plates didn't get is filled on a crop around it, where
    # the model looks at that one object.
    retry = [(r_, k_) for r_, k_ in failed if (r_ & photo_all).sum() > 0.3 * r_.sum()]
    if retry:
        log(f"  filling {len(retry)} hole(s) on a crop")
        with ThreadPoolExecutor(min(4, len(retry))) as ex:
            fills = list(ex.map(lambda rk: gen.inpaint(bg, rk[0], matte, log), retry))
        for (m, k_), (f, filled) in zip(retry, fills):
            if f is not None:
                bg = np.where(filled[..., None], f, bg).astype(np.uint8)
                good |= filled
                hole |= filled
                extra = filled & ~m & ~every_ob
                if extra.any():   # the rest of the dish, the napkin: part of the object
                    objects[k_]["mask"] = fill_holes(objects[k_]["mask"] | extra)
                    every_ob |= extra
                    taken |= extra
    log(f"empty background used for {int(good.sum() * 100 / max(1, hole.sum()))}% of the holes")
    rest = hole & ~good
    if rest.any():
        # One part per object, so each is erased with its own surroundings.
        obj_parts = [(dilate(ob["mask"] | ((alpha >= 16) & dilate(ob["mask"], merge_px)), g)
                      if ob["name"] == "object" else dilate(ob["mask"], int(2.5 * g))) & rest for ob in objects]
        bg = erase(bg, rest, [m for m in obj_parts if m.any()])
    # Nothing of an object may be left in the background: a dedicated
    # eraser sometimes paints a big product straight back in. Those holes
    # are repainted by masked inpainting instead.
    if rest.any():
        left_alpha, _ = matte(bg)
        redo = [m for m in obj_parts if m.any() and (left_alpha[m] >= 128).mean() > 0.1]
        if redo:
            log(f"{len(redo)} object(s) still in the background; inpainting them")
            with ThreadPoolExecutor(min(4, len(redo))) as ex:
                fills = list(ex.map(lambda m: gen.inpaint(bg, m, matte, log), redo))
            for m, (f, filled) in zip(redo, fills):
                if f is not None:
                    bg = np.where(filled[..., None], f, bg).astype(np.uint8)
    # Nothing readable may be left in the background (a plate can keep the
    # print of a cup it removed).
    # Only where an object or a text element was: lettering that isn't an
    # element (a curved slogan the OCR skipped) is part of the design.
    gone = hole | boxes
    left = [b_ for b_ in read_text(bg, 0.8)
            if gone[b_[1]:b_[1] + b_[3], b_[0]:b_[0] + b_[2]].mean() > 0.3]
    if left:
        log(f"{len(left)} text line(s) in the background; erasing")
        bg = erase_boxes(bg, left)
    Image.fromarray(bg).save(os.path.join(out_dir, "bg.png"))
    save_overlay(src, objects, os.path.join(out_dir, "objects.png"))
    log("background ready")

    # Which letters are hidden behind an object: only pixels of the text's
    # own colour count as its ink (a loose glyph mask also catches caramel
    # on a scoop next to red type).
    srcf = src.astype(np.float32)
    for k, ln in enumerate(lines):
        if not ln.get("color") or ln.get("gradient"):
            continue
        c = np.array([int(ln["color"][i:i + 2], 16) for i in (1, 3, 5)], np.float32)
        ys, xs = np.nonzero(glyphs[k] & taken)
        far = np.linalg.norm(srcf[ys, xs] - c, axis=1) > 28
        glyphs[k] = glyphs[k].copy()
        glyphs[k][ys[far], xs[far]] = False

    # Elements. Matte objects keep the soft edge (alpha from the matte,
    # refined colours where it is partly transparent); extras are cut hard.
    elements = []
    order = sorted(range(len(objects)), key=lambda i: -objects[i]["mask"].sum())  # big ones at the back
    z_of = {}
    for k, i in enumerate(order, start=1):
        ob = objects[i]
        region = dilate(ob["mask"], 2)
        x, y, w, h = bbox(region)
        win = np.s_[y:y + h, x:x + w]
        if ob["name"] == "object":
            # The matte's soft edge where it has one; pixels attached on
            # top of the matte (powder, crumbs) are cut with a 1px feather.
            hard = (cv2.GaussianBlur(ob["mask"][win].astype(np.float32), (3, 3), 0) * 255).astype(np.uint8)
            a_ = np.where(region[win], np.maximum(alpha[win], np.where(alpha[win] >= 16, 0, hard)), 0).astype(np.uint8)
            rgb = np.where(((a_ < 250) & (alpha[win] >= 16))[..., None], fg_rgb[win], clean[win])
        else:
            m = ob["mask"][win]
            a_ = (cv2.GaussianBlur(m.astype(np.float32), (3, 3), 0) * 255).round().astype(np.uint8)
            a_[m] = 255
            rgb = clean[win]
        crop = np.dstack([rgb, a_]).astype(np.uint8)
        el = {"id": f"el{k}", "name": ob["name"], "z": float(k), "box": [x, y, w, h]}
        style = shape_style(crop[:, :, :3], crop[:, :, 3] >= 200)
        if style:
            el.update(type="shape", style=style)
        else:
            el.update(type="image", src=f"el{k}.png")
            Image.fromarray(crop).save(os.path.join(out_dir, el["src"]), optimize=True)
        z_of[i] = float(k)
        elements.append(el)

    # Text: on top, except where an object hides it.
    final_rel = text_relations(lines, glyphs, objects)
    top = float(len(order) + 1)
    for n, (ln, (above, under)) in enumerate(zip(lines, final_rel)):
        lo = max([z_of[i] for i, _ in above], default=0.0)
        hi = min([z_of[i] for i, _ in under], default=top)
        if lo >= hi:
            if sum(a_ for _, a_ in above) >= sum(a_ for _, a_ in under):
                hi = top
            else:
                lo = 0.0
        z = (lo + hi) / 2 if hi < top else top + n * 0.001
        el = {"id": f"t{n + 1}", "type": "text", "z": z, "box": ln["box"], "text": ln["text"],
              "color": ln.get("color", "#000000"), "font_size": ln["font_size"],
              "font_weight": ln.get("font_weight", 400)}
        if ln.get("gradient"):
            el["gradient"] = ln["gradient"]
        if ln.get("rotate"):
            el["rotate"] = ln["rotate"]
        elements.append(el)

    for i, el in enumerate(sorted(elements, key=lambda e: e["z"]), start=1):
        el["z"] = i
    json.dump({"width": W, "height": H, "background": "bg.png", "elements": elements},
              open(os.path.join(out_dir, "scene.json"), "w"), ensure_ascii=False, indent=1)
    kinds = {}
    for e in elements:
        kinds[e["type"]] = kinds.get(e["type"], 0) + 1
    print(f"scene: {kinds} -> {out_dir}/scene.json")
    # Skip interpreter teardown: onnxruntime (OCR) and the HTTP worker
    # threads sometimes abort there on macOS ("recursive_mutex lock failed").
    sys.stdout.flush()
    sys.stderr.flush()
    os._exit(0)


if __name__ == "__main__":
    main()
