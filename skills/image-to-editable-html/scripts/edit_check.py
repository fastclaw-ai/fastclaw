#!/usr/bin/env python3
"""Editability stress test: does the page survive being edited?

Usage: edit_check.py <page.html> <source.png> <out_dir>

Also the static check: static.png vs the source (SSIM, MAE, the cells that
differ most).

A static render can match the source while the layers underneath are a
mess. This test edits the page the way a user would and inspects what is
exposed:

  * bg_only.png        background alone (all elements hidden)
  * no_text.png        everything except the text elements
  * scattered.png      every element moved away from its spot
  * elements/<id>.png  each element alone on a checkerboard

Scores (printed as JSON):
  static_ssim, mae   unedited render vs source
  worst_regions      cells [x, y, w, h] where the render differs most
  ghost_text         OCR lines found where text was, with all text hidden
                     (should be 0: text was not removed from the layers)
  bg_ghost_text      same, on the background alone
  dirty_elements     image elements that contain text-like fragments
  exposed_artifacts  elements whose hidden-state reveals a region that
                     differs sharply from its surroundings (flat white
                     fills, holes)
"""
import json
import logging
import os
import sys

import numpy as np
from PIL import Image
from playwright.sync_api import sync_playwright
from skimage.metrics import structural_similarity

logging.getLogger("RapidOCR").setLevel(logging.WARNING)

HIDE_CHROME = ("#fc-panel,#fc-sel{display:none!important}#fc-scaler{transform:none!important}"
               "#fc-viewport{padding:0!important}")


def ocr_lines(img, min_score=0.6):
    from rapidocr import RapidOCR
    if not hasattr(ocr_lines, "eng"):
        ocr_lines.eng = RapidOCR()
    res = ocr_lines.eng(np.asarray(img.convert("RGB")))
    out = []
    for quad, text, score in zip(res.boxes if res.boxes is not None else [], res.txts or [], res.scores or []):
        if score < min_score or len(str(text).strip()) < 2:
            continue
        xs, ys = [p[0] for p in quad], [p[1] for p in quad]
        out.append({"text": str(text), "box": [int(min(xs)), int(min(ys)), int(max(xs) - min(xs)), int(max(ys) - min(ys))]})
    return out


def overlaps(a, b, frac=0.3):
    ax, ay, aw, ah = a
    bx, by, bw, bh = b
    ix = max(0, min(ax + aw, bx + bw) - max(ax, bx))
    iy = max(0, min(ay + ah, by + bh) - max(ay, by))
    return ix * iy >= frac * min(aw * ah, bw * bh)


def main():
    page_path, src_path, out_dir = sys.argv[1:4]
    os.makedirs(os.path.join(out_dir, "elements"), exist_ok=True)
    src = Image.open(src_path).convert("RGB")
    W, H = src.size
    scene = json.load(open(os.path.join(os.path.dirname(page_path), "scene", "scene.json")))
    els = {e["id"]: e for e in scene["elements"]}
    text_boxes = [e["box"] for e in scene["elements"] if e["type"] == "text"]

    with sync_playwright() as p:
        b = p.chromium.launch()
        pg = b.new_page(viewport={"width": W + 50, "height": H + 50})
        pg.goto("file://" + os.path.abspath(page_path))
        pg.add_style_tag(content=HIDE_CHROME)
        pg.evaluate("() => document.fonts.ready.then(() => 0)")
        pg.wait_for_timeout(800)
        # Text elements may carry a fitting transform (scaleX): keep it.
        pg.evaluate("() => document.querySelectorAll('.fc-el').forEach(e => e.dataset.t0 = e.style.transform)")
        canvas = pg.locator("#fc-canvas")

        def shot(name, js):
            pg.evaluate("() => { document.querySelectorAll('.fc-el').forEach(e => { e.style.visibility = ''; e.style.transform = e.dataset.t0 || ''; }); document.getElementById('fc-canvas').style.backgroundImage = window.__bg || (window.__bg = document.getElementById('fc-canvas').style.backgroundImage); }")
            pg.evaluate(js)
            path = os.path.join(out_dir, name)
            canvas.screenshot(path=path)
            return Image.open(path).convert("RGB").resize((W, H))

        static = shot("static.png", "() => {}")
        bg_only = shot("bg_only.png", "() => document.querySelectorAll('.fc-el').forEach(e => e.style.visibility = 'hidden')")
        no_text = shot("no_text.png", "() => document.querySelectorAll('.fc-text').forEach(e => e.style.visibility = 'hidden')")
        shot("scattered.png", f"""() => document.querySelectorAll('.fc-el').forEach((e, i) => {{
            const dx = (i % 2 ? 1 : -1) * {W} * 0.18, dy = ((i >> 1) % 2 ? 1 : -1) * {H} * 0.08;
            e.style.transform = `translate(${{dx}}px, ${{dy}}px) ${{e.dataset.t0 || ''}}`; }})""")

        # Each element alone on a checkerboard.
        pg.add_style_tag(content="#fc-canvas.solo{background:repeating-conic-gradient(#ccc 0 25%,#fff 0 50%) 0 0/20px 20px!important}")
        for eid, e in els.items():
            if e["type"] == "text":
                continue
            pg.evaluate(f"""() => {{ const c = document.getElementById('fc-canvas'); c.classList.add('solo');
                document.querySelectorAll('.fc-el').forEach(n => n.style.visibility = n.dataset.id === '{eid}' ? '' : 'hidden'); }}""")
            x, y, w, h = e["box"]
            full = os.path.join(out_dir, "elements", f"{eid}.png")
            canvas.screenshot(path=full)
            Image.open(full).convert("RGB").resize((W, H)).crop((x, y, x + w, y + h)).save(full)

        # Hide each image element in turn; look at what it exposes.
        exposed = []
        pg.evaluate("() => document.getElementById('fc-canvas').classList.remove('solo')")
        for eid, e in els.items():
            if e["type"] == "text":
                continue
            # Hide the element and all text: anything text-like left in the
            # exposed area is a leftover.
            img = shot(f"hide_{eid}.png", f"() => {{ document.querySelectorAll('.fc-text').forEach(t => t.style.visibility = 'hidden'); const n = document.querySelector('[data-id=\"{eid}\"]'); if (n) n.style.visibility = 'hidden'; }}")
            x, y, w, h = e["box"]
            if w * h < 0.002 * W * H:
                os.remove(os.path.join(out_dir, f"hide_{eid}.png"))
                continue
            a = np.asarray(img).astype(np.float32)
            inner = a[y:y + h, x:x + w].reshape(-1, 3)
            m = max(8, int(0.06 * max(w, h)))
            ring = np.concatenate([a[max(0, y - m):y, x:x + w].reshape(-1, 3), a[y + h:y + h + m, x:x + w].reshape(-1, 3),
                                   a[y:y + h, max(0, x - m):x].reshape(-1, 3), a[y:y + h, x + w:x + w + m].reshape(-1, 3)])
            near_white = float(((inner > 245).all(axis=1)).mean()) - float(((ring > 245).all(axis=1)).mean()) if len(ring) else 0.0
            # Smooth filled patches can fool OCR at low confidence; a real
            # leftover reads clearly.
            ghost = ocr_lines(img.crop((x, y, x + w, y + h)), min_score=0.85)
            white_ring = bool(len(ring)) and bool((np.median(ring, axis=0) > 235).all())
            exposed.append({"id": eid, "flat_white_excess": round(near_white, 3), "white_ring": white_ring,
                            "ghost_text": [g["text"] for g in ghost]})
            os.remove(os.path.join(out_dir, f"hide_{eid}.png"))
        b.close()

    a, r = np.asarray(src).astype(np.float32), np.asarray(static).astype(np.float32)
    static_ssim = structural_similarity(a, r, channel_axis=2, data_range=255)
    err = np.abs(a - r).mean(axis=2)
    cell = max(16, min(W, H) // 24)
    cells = sorted(((float(err[y:y + cell, x:x + cell].mean()), [x, y, cell, cell])
                    for y in range(0, H, cell) for x in range(0, W, cell)), reverse=True)
    ghost = [l for l in ocr_lines(no_text) if any(overlaps(l["box"], tb) for tb in text_boxes)]
    bg_ghost = [l for l in ocr_lines(bg_only) if any(overlaps(l["box"], tb) for tb in text_boxes)]
    dirty = []
    for eid, e in els.items():
        if e["type"] != "image":
            continue
        crop = Image.open(os.path.join(out_dir, "elements", f"{eid}.png"))
        frags = [l["text"] for l in ocr_lines(crop)]
        if frags:
            dirty.append({"id": eid, "text": frags})
    # Revealing white is only suspicious where the surroundings aren't white
    # themselves (a card on a white UI should reveal white).
    artifacts = [x for x in exposed if (x["flat_white_excess"] > 0.25 and not x["white_ring"]) or x["ghost_text"]]
    report = {
        "static_ssim": round(float(static_ssim), 4),
        "mae": round(float(err.mean()), 2),
        "worst_regions": [{"mae": round(m, 1), "box": b} for m, b in cells[:6]],
        "ghost_text": [g["text"] for g in ghost],
        "bg_ghost_text": [g["text"] for g in bg_ghost],
        "dirty_elements": dirty,
        "exposed_artifacts": artifacts,
        "score": {
            "ghost_lines": len(ghost), "bg_ghost_lines": len(bg_ghost),
            "dirty_elements": len(dirty), "exposed_artifacts": len(artifacts),
        },
    }
    print(json.dumps(report, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
    # Skip interpreter teardown: onnxruntime (OCR) sometimes aborts there
    # on macOS ("recursive_mutex lock failed").
    sys.stdout.flush()
    sys.stderr.flush()
    os._exit(0)
