"""Generative clean-up with an image-editing model (nano-banana-2 on fal.ai,
gpt-image-2 as the fallback).

Removing text that overlaps a product, or everything in front of the
background, is a judgement call — which pixels are letters, what the scoop
looks like behind the "E" — that masks and an eraser get wrong in visible
ways. An editing model redraws the whole picture coherently; we only keep
its result when it lines up with the source pixel for pixel.
"""
from concurrent.futures import ThreadPoolExecutor

import cv2
import numpy as np
from PIL import Image

import fal

MODELS = [
    ("fal-ai/nano-banana-2/edit", "nano-banana-2"),
    ("fal-ai/gpt-image-2/edit", "gpt-image-2"),
]

REMOVE_TEXT = (
    "Remove all text from this image: every word, letter, number, price and caption, including text printed on "
    "products or packaging. Where text overlapped a product, show the product as it naturally continues underneath. "
    "Keep everything else exactly as it is — same products, same positions and sizes, same colours and lighting, "
    "same lines, frames and borders, same icons and logo symbols (only remove their letters). Text set sideways or "
    "vertically counts too. Where text sat on a plain background, leave plain background — do not add logos, "
    "symbols, lines or any other graphics.")

EMPTY_PLATE = (
    "This is a graphic design (poster, menu or flyer). Remove only the photographed products from it — food, drinks, "
    "their plates, cups and garnish, and props such as napkins — and show the plain background behind them, continuing "
    "its colour, texture and lighting. Every graphic element of the design stays exactly where it is and as it is: "
    "logos, icons, frames, boxes, outlines, lines and panels. Do not redraw or change the rest of the image.")


def remove_named(texts):
    quoted = ", ".join(f'"{t}"' for t in texts)
    return ("Remove all the remaining text from this image — in particular the big lettering (it reads like "
            + quoted + ", possibly only part of a word; remove the whole word, every letter of it). It may be set "
            "sideways or partly hidden behind an object. Show what is behind it — the plain background, or the "
            "object continuing. Keep everything else exactly as it is, and do not add anything.")


def _call(endpoint, rgb, prompt):
    H, W = rgb.shape[:2]
    url = fal.data_url(rgb)
    if "nano-banana" in endpoint:
        payload = {"image_urls": [url], "prompt": prompt, "resolution": "2K" if max(W, H) > 1000 else "1K",
                   "aspect_ratio": "auto", "output_format": "png", "num_images": 1, "sync_mode": True}
    else:
        payload = {"image_urls": [url], "prompt": prompt, "image_size": "auto", "quality": "high",
                   "output_format": "png", "num_images": 1, "sync_mode": True}
    r = fal.run(endpoint, payload, timeout=600)
    return np.asarray(fal.load(r["images"][0]["url"]).resize((W, H), Image.LANCZOS))


def misalignment(a, b, keep):
    """(mean abs difference on `keep`, global shift in px) between a and b."""
    ga = cv2.cvtColor(a, cv2.COLOR_RGB2GRAY).astype(np.float32)
    gb = cv2.cvtColor(b, cv2.COLOR_RGB2GRAY).astype(np.float32)
    (dx, dy), _ = cv2.phaseCorrelate(ga, gb)
    diff = np.abs(a.astype(np.float32) - b.astype(np.float32)).mean(axis=2)
    return float(diff[keep].mean()) if keep.any() else 0.0, float(np.hypot(dx, dy))


def edit(rgb, prompt, keep, max_diff=12.0, max_shift=1.5, log=print, must_change=None, min_changed=0.5):
    """Run the prompt through the models in turn; return the first result
    that matches `rgb` on the `keep` mask (what must not change) and, if
    given, really changed most of `must_change` (a model sometimes returns
    the picture untouched), or None."""
    for endpoint, name in MODELS:
        try:
            out = _call(endpoint, rgb, prompt)
        except Exception as e:  # noqa: BLE001 — a failed call just means "try the next model"
            log(f"{name} failed: {e}")
            continue
        diff, shift = misalignment(rgb, out, keep)
        changed = 1.0
        if must_change is not None and must_change.any():
            d = np.abs(out.astype(np.int16) - rgb.astype(np.int16)).max(axis=2)
            changed = float((d[must_change] > 30).mean())
        log(f"{name}: diff {diff:.1f}, shift {shift:.1f}px, changed {changed:.2f}")
        if diff <= max_diff and shift <= max_shift and changed >= min_changed:
            return out
    return None


def candidates(rgb, prompt, keep, n=2, max_diff=12.0, max_shift=1.5, log=print, must_change=None, min_changed=0.1):
    """`n` nano-banana-2 results at once (its output varies from call to
    call: one misses a line another gets right), keeping those that match
    `rgb` on `keep`; gpt-image-2 only when none does."""
    def one(endpoint, name):
        try:
            out = _call(endpoint, rgb, prompt)
        except fal.AccountError:
            raise
        except Exception as e:  # noqa: BLE001
            log(f"{name} failed: {e}")
            return None
        diff, shift = misalignment(rgb, out, keep)
        changed = 1.0
        if must_change is not None and must_change.any():
            d = np.abs(out.astype(np.int16) - rgb.astype(np.int16)).max(axis=2)
            changed = float((d[must_change] > 30).mean())
        log(f"{name}: diff {diff:.1f}, shift {shift:.1f}px, changed {changed:.2f}")
        return out if diff <= max_diff and shift <= max_shift and changed >= min_changed else None
    with ThreadPoolExecutor(n) as ex:
        outs = [o for o in ex.map(lambda _: one(*MODELS[0]), range(n)) if o is not None]
    if not outs:
        o = one(*MODELS[1])
        outs = [o] if o is not None else []
    return outs


REMOVE_IN_CROP = (
    "Remove the main object in the middle of this image (and everything that belongs to it — its garnish, crumbs, "
    "shadow) and show the empty background behind it, continuing the surrounding surface, colours and lighting "
    "naturally. Keep everything near the edges of the image exactly as it is. No new objects, no text.")


def _fits(res, rgb, mask, matte_fn):
    """A fill is usable when nothing is left standing in the hole and its
    colours carry on from the surroundings (models sometimes paint a hole
    flat black)."""
    ring = (cv2.dilate(mask.astype(np.uint8), np.ones((25, 25), np.uint8)) > 0) & ~mask
    if ring.any():
        inner = res[mask].astype(np.float32).mean(axis=0)
        outer = rgb[ring].astype(np.float32).mean(axis=0)
        if np.abs(inner - outer).max() > 45:
            _fits.why = f"colour off by {np.abs(inner - outer).max():.0f}"
            return False
    alpha, _ = matte_fn(res)
    left = float((alpha[mask] >= 128).mean())
    if left >= 0.2:   # a prop the object overlapped (a napkin) may stay
        _fits.why = f"{left:.0%} of the hole still foreground"
        return False
    return True


def inpaint(rgb, mask, matte_fn, log=print, n=2):
    """Fill one hole the empty background didn't get (a large product):
    nano-banana-2 on a crop around it, asked to remove the object, `n`
    samples at once (its output varies), the first that passes `_fits`
    kept. Returns the new image, or None. Pixels outside the mask are
    kept. (gpt-image-2's masked edit paints such holes flat black.)
    Also returns the hole as filled — it can grow over what belongs to
    the object beyond the mask."""
    H, W = rgb.shape[:2]
    ys, xs = np.nonzero(mask)
    x, y, w, h = xs.min(), ys.min(), xs.max() - xs.min() + 1, ys.max() - ys.min() + 1
    p = int(max(w, h) * 0.3) + 16
    x0, y0, x1, y1 = max(0, x - p), max(0, y - p), min(W, x + w + p), min(H, y + h + p)
    crop, cm = rgb[y0:y1, x0:x1], mask[y0:y1, x0:x1]

    def one(_):
        try:
            return _call(MODELS[0][0], crop, REMOVE_IN_CROP)
        except fal.AccountError:
            raise
        except Exception as e:  # noqa: BLE001
            log(f"  nano-banana-2 inpaint failed: {e}")
            return None
    with ThreadPoolExecutor(n) as ex:
        outs = list(ex.map(one, range(n)))
    for out in outs:
        if out is None:
            continue
        # What the model also took away right next to the hole (the rest
        # of a dish, a napkin the matte cut through) belongs to the object:
        # left in place, half of it would stand at the hole's edge.
        d = np.abs(out.astype(np.int16) - crop.astype(np.int16)).max(axis=2) > 40
        d = cv2.morphologyEx(d.astype(np.uint8), cv2.MORPH_OPEN, np.ones((5, 5), np.uint8))
        n, lab = cv2.connectedComponents(d)
        near = cv2.dilate(cm.astype(np.uint8), np.ones((15, 15), np.uint8)) > 0
        ids = [i for i in np.unique(lab[near & (lab > 0)])]
        grown = cm | (np.isin(lab, ids) & (lab > 0)) if ids else cm
        if grown.sum() > 1.6 * cm.sum():
            grown = cm
        grown = cv2.dilate(grown.astype(np.uint8), np.ones((5, 5), np.uint8)) > 0
        res = crop.copy()
        res[grown] = out[grown]
        full = rgb.copy()
        full[y0:y1, x0:x1] = res
        big = np.zeros(mask.shape, bool)
        big[y0:y1, x0:x1] = grown
        # Judged on the whole picture: a matte of a small crop calls
        # whatever sits in its middle foreground, empty or not.
        if _fits(full, rgb, big, matte_fn):
            return full, big
        log(f"  nano-banana-2 fill rejected: {getattr(_fits, 'why', '')}")
    return None, None
