#!/usr/bin/env python3
"""Edit a generated design in conversation.

  scene.py <cmd> <out_dir> [args]      (out_dir = the "<stem>-editable" folder)

  status                       elements, version, and the user's manual edits since last time
  set <id> key=value ...       text, color, font_family, font_weight, font_size,
                               box=x,y,w,h, x=, y=, z=front|back|<n>, hidden=1, opacity=
  add-text "<text>" box=x,y,w,h [color= font_family= font_weight=]
  remove <id> [<id> ...]
  edit <id> "<instruction>" [--model nano-banana-2|gpt-image-2]
                               repaint one object in place (in context)
  edit-bg "<instruction>" [--model ...]
                               repaint the background (objects and text untouched)
  add "<what>" box=x,y,w,h [--model ...]
                               paint a new object into that area, as its own layer
  restyle "<instruction>" [--model ...]
                               repaint everything but the text, then re-split into layers
  undo                         back to the previous version
  render                       rebuild the HTML and the PNG preview

Every command first merges the edits the user made by hand in the page
(scene/edits.json, written by the FastClaw chat), then applies its change,
keeps the previous scene.json in scene/history/, and re-renders: the page
<stem>.html and a flat preview previews/<stem>-v<version>.png. Layer images are never
overwritten — each edit writes new files — so undo is just the old JSON.

The final stdout line is a JSON summary: html, preview, version, changes.
Text is never sent to an image model: it stays live HTML, so manual text
edits survive any image edit.
"""
import argparse
import glob
import json
import os
import shutil
import subprocess
import sys
import time

import cv2
import numpy as np
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import fal  # noqa: E402
from build_scene import attach_nearby, components, dilate, disk, matte, save_overlay  # noqa: E402
from erase import erase  # noqa: E402

MODELS = {"nano-banana-2": "fal-ai/nano-banana-2/edit", "gpt-image-2": "fal-ai/gpt-image-2/edit"}
NO_TEXT = " Do not add any text, letters, numbers, logos or watermarks."


# --- scene files ---------------------------------------------------------

class Design:
    def __init__(self, out_dir):
        self.out = os.path.abspath(out_dir)
        self.dir = os.path.join(self.out, "scene")
        self.meta = json.load(open(os.path.join(self.out, "meta.json")))
        self.path = os.path.join(self.dir, "scene.json")
        self.scene = json.load(open(self.path))
        self.scene.setdefault("version", 1)
        self.W, self.H = self.scene["width"], self.scene["height"]
        self.changes = []

    @property
    def elements(self):
        return self.scene["elements"]

    def get(self, eid):
        for el in self.elements:
            if el["id"] == eid:
                return el
        raise SystemExit(f"no element {eid!r} (run `status` for the list)")

    def new_id(self, prefix):
        n = 1
        ids = {e["id"] for e in self.elements}
        while f"{prefix}{n}" in ids:
            n += 1
        return f"{prefix}{n}"

    def asset(self, stem, ext="png"):
        """A fresh file name in the scene dir: layers are never overwritten."""
        return f"{stem}_v{self.scene['version'] + 1}_{int(time.time() * 1000) % 100000}.{ext}"

    def save(self):
        hist = os.path.join(self.dir, "history")
        os.makedirs(hist, exist_ok=True)
        shutil.copy(self.path, os.path.join(hist, f"v{self.scene['version']}.json"))
        self.scene["version"] += 1
        json.dump(self.scene, open(self.path, "w"), ensure_ascii=False, indent=1)

    def top_z(self, kind=None):
        zs = [e["z"] for e in self.elements if kind is None or (e["type"] == "text") == (kind == "text")]
        return max(zs, default=0)

    def renumber(self):
        for i, el in enumerate(sorted(self.elements, key=lambda e: e["z"]), start=1):
            el["z"] = i


def sync(d):
    """Merge the user's manual edits from the page (scene/edits.json)."""
    p = os.path.join(d.dir, "edits.json")
    if not os.path.exists(p):
        return []
    try:
        ed = json.load(open(p))
    except ValueError:
        return []
    notes = []
    ids = {e["id"]: e for e in d.elements}
    for eid, ch in (ed.get("changed") or {}).items():
        el = ids.get(eid)
        if not el:
            continue
        for k, v in ch.items():
            if k in ("box", "z", "hidden", "opacity", "text", "color", "font_weight", "css", "style"):
                if el.get(k) != v:
                    el[k] = v
        what = ", ".join(sorted(ch))
        label = el.get("text") or el.get("name") or eid
        notes.append(f"user changed {eid} ({label[:30]}): {what}")
    for eid in ed.get("deleted") or []:
        if eid in ids:
            d.elements.remove(ids[eid])
            notes.append(f"user deleted {eid}")
    os.replace(p, os.path.join(d.dir, f"edits.applied.{int(time.time())}.json"))
    if notes:
        d.renumber()
        d.save()
    return notes


# --- rendering -----------------------------------------------------------

def load_rgba(d, name):
    return np.asarray(Image.open(os.path.join(d.dir, name)).convert("RGBA"))


def composite(d, exclude=(), text=False):
    """The design flattened without its text (what image models see)."""
    out = Image.open(os.path.join(d.dir, d.scene["background"])).convert("RGBA").resize((d.W, d.H))
    for el in sorted(d.elements, key=lambda e: e["z"]):
        if el["id"] in exclude or el.get("hidden"):
            continue
        x, y, w, h = [int(round(v)) for v in el["box"]]
        if w <= 0 or h <= 0:
            continue
        if el["type"] == "image":
            im = Image.open(os.path.join(d.dir, el["src"])).convert("RGBA").resize((w, h), Image.LANCZOS)
        elif el["type"] == "shape":
            c = el["style"]["background"].lstrip("#")
            if len(c) != 6:
                continue
            im = Image.new("RGBA", (w, h), tuple(int(c[i:i + 2], 16) for i in (0, 2, 4)) + (255,))
        else:
            continue
        if el.get("opacity") not in (None, 1):
            a = np.asarray(im).copy()
            a[:, :, 3] = (a[:, :, 3] * float(el["opacity"])).astype(np.uint8)
            im = Image.fromarray(a)
        layer = Image.new("RGBA", out.size)
        layer.paste(im, (x, y))
        out = Image.alpha_composite(out, layer)
    return np.asarray(out.convert("RGB"))


def render(d):
    stem = d.meta["stem"]
    html_path = os.path.join(d.out, stem + ".html")
    # One picture per version: earlier replies in the chat keep showing
    # what they showed then.
    os.makedirs(os.path.join(d.out, "previews"), exist_ok=True)
    png_path = os.path.join(d.out, "previews", f"{stem}-v{d.scene['version']}.png")
    r = subprocess.run([sys.executable, os.path.join(HERE, "build_html.py"), d.dir, html_path,
                        "--title", d.meta.get("title") or stem], capture_output=True, text=True)
    if r.returncode:
        raise SystemExit(r.stderr or r.stdout)
    from playwright.sync_api import sync_playwright
    with sync_playwright() as p:
        b = p.chromium.launch()
        pg = b.new_page(viewport={"width": d.W + 50, "height": d.H + 50})
        pg.goto("file://" + html_path)
        pg.add_style_tag(content="#fc-panel,#fc-sel{display:none!important}#fc-scaler{transform:none!important}"
                                 "#fc-viewport{padding:0!important}")
        pg.evaluate("() => document.fonts.ready.then(() => 0)")
        pg.wait_for_timeout(500)
        pg.locator("#fc-canvas").screenshot(path=png_path)
        b.close()
    return html_path, png_path


# --- image models --------------------------------------------------------

def paint(images, prompt, model, size):
    """One image-model call; returns HxWx3 at `size` (w, h)."""
    endpoint = MODELS.get(model)
    if not endpoint:
        raise SystemExit(f"unknown model {model!r}; use one of {', '.join(MODELS)}")
    urls = [fal.data_url(im) for im in images]
    if model == "nano-banana-2":
        res = "2K" if max(size) > 1100 else "1K"
        payload = {"image_urls": urls, "prompt": prompt, "resolution": res, "aspect_ratio": "auto",
                   "output_format": "png", "num_images": 1, "sync_mode": True}
    else:
        payload = {"image_urls": urls, "prompt": prompt, "image_size": "auto", "quality": "high",
                   "output_format": "png", "num_images": 1, "sync_mode": True}
    r = fal.run(endpoint, payload, timeout=600)
    im = fal.load(r["images"][0]["url"]).resize(size, Image.LANCZOS)
    return np.asarray(im)


def crop_box(d, box, pad_frac=0.35, min_pad=48):
    x, y, w, h = box
    p = max(min_pad, int(pad_frac * max(w, h)))
    x0, y0 = max(0, int(x - p)), max(0, int(y - p))
    x1, y1 = min(d.W, int(x + w + p)), min(d.H, int(y + h + p))
    return x0, y0, x1, y1


def cut_layer(d, rgb, alpha, mask, offset, stem):
    """Save rgb/alpha under mask as a new layer file; returns (src, box)."""
    m = dilate(mask, 2)
    ys, xs = np.nonzero(m)
    x0, y0, x1, y1 = xs.min(), ys.min(), xs.max() + 1, ys.max() + 1
    a = np.where(m[y0:y1, x0:x1], alpha[y0:y1, x0:x1], 0).astype(np.uint8)
    hard = (cv2.GaussianBlur(mask[y0:y1, x0:x1].astype(np.float32), (3, 3), 0) * 255).astype(np.uint8)
    a = np.maximum(a, np.where(alpha[y0:y1, x0:x1] >= 16, 0, hard))
    crop = np.dstack([rgb[y0:y1, x0:x1], a]).astype(np.uint8)
    name = d.asset(stem)
    Image.fromarray(crop).save(os.path.join(d.dir, name), optimize=True)
    return name, [int(offset[0] + x0), int(offset[1] + y0), int(x1 - x0), int(y1 - y0)]


def pick_object(alpha, rgb, near, min_area):
    """The matte component that best matches `near` (a mask of where the
    object should be), with whatever lies right next to it."""
    h, w = alpha.shape
    comps = components(alpha, max(3, int(0.012 * min(h, w))), min_area)
    if not comps:
        return None
    best = max(comps, key=lambda m: (m & near).sum() / max(1, m.sum()) + (m & near).sum() / max(1, near.sum()))
    if not (best & near).any():
        return None
    obs = [{"name": "object", "mask": best}]
    attach_nearby(rgb, obs)
    return obs[0]["mask"]


# --- commands --------------------------------------------------------------

def cmd_edit(d, a):
    el = d.get(a.id)
    if el["type"] == "text":
        raise SystemExit("text is edited with `set`, not an image model")
    x0, y0, x1, y1 = crop_box(d, el["box"])
    base = composite(d)[y0:y1, x0:x1]
    prompt = (f"This is a crop of a designed poster. Edit only the object in the middle of the image: {a.instruction}. "
              "Keep the object at the same position and roughly the same size, and keep everything else — "
              "the background, the framing, the camera angle and the lighting — exactly as it is." + NO_TEXT)
    out = paint([base], prompt, a.model, (x1 - x0, y1 - y0))
    alpha, fg = matte(out)
    near = np.zeros(alpha.shape, bool)
    bx, by, bw, bh = [int(v) for v in el["box"]]
    near[max(0, by - y0):by - y0 + bh, max(0, bx - x0):bx - x0 + bw] = True
    mask = pick_object(alpha, out, near, max(40, int(0.01 * near.sum())))
    if mask is None:
        raise SystemExit("the edited image has no object where the old one was; try rephrasing")
    rgb = np.where((alpha >= 250)[..., None] | (alpha < 16)[..., None], out, fg)
    el["src"], el["box"] = cut_layer(d, rgb, alpha, mask, (x0, y0), el["id"])
    el["type"] = "image"
    el.pop("style", None)
    d.changes.append(f"repainted {el['id']}: {a.instruction}")


def cmd_edit_bg(d, a):
    bg = np.asarray(Image.open(os.path.join(d.dir, d.scene["background"])).convert("RGB").resize((d.W, d.H)))
    prompt = (f"This is the empty background plate of a poster layout (the products and the text are separate layers "
              f"placed on top later). {a.instruction}. Keep the composition, perspective and the light direction, and keep every line, "
              "border, frame, divider and decorative graphic exactly where it is (restyle them only if asked). "
              "Don't put any products, people or new objects in it." + NO_TEXT)
    out = paint([bg], prompt, a.model, (d.W, d.H))
    name = d.asset("bg")
    Image.fromarray(out).save(os.path.join(d.dir, name))
    d.scene["background"] = name
    d.changes.append(f"repainted the background: {a.instruction}")


def cmd_add(d, a):
    x, y, w, h = parse_box(a.box)
    x0, y0, x1, y1 = crop_box(d, [x, y, w, h], pad_frac=0.3)
    base = composite(d)[y0:y1, x0:x1]
    cx, cy = (x + w / 2 - x0) / (x1 - x0), (y + h / 2 - y0) / (y1 - y0)
    prompt = (f"Add {a.what} to this image, centred at about {cx:.0%} from the left and {cy:.0%} from the top, "
              f"about {w / (x1 - x0):.0%} of the image wide, sitting naturally in the scene with matching lighting, "
              "perspective and style. Leave everything else exactly as it is." + NO_TEXT)
    out = paint([base], prompt, a.model, (x1 - x0, y1 - y0))
    alpha, fg = matte(out)
    # Where the image changed is where the new object is.
    changed = np.linalg.norm(out.astype(np.float32) - base.astype(np.float32), axis=2) > 40
    changed = cv2.morphologyEx(changed.astype(np.uint8), cv2.MORPH_OPEN, disk(2)) > 0
    near = np.zeros(alpha.shape, bool)
    near[max(0, y - y0):y - y0 + h, max(0, x - x0):x - x0 + w] = True
    mask = pick_object(alpha, out, near & changed if (near & changed).any() else near, 40)
    if mask is None:
        raise SystemExit("couldn't find the new object in the generated image; try rephrasing")
    rgb = np.where((alpha >= 250)[..., None] | (alpha < 16)[..., None], out, fg)
    eid = d.new_id("el")
    src, box = cut_layer(d, rgb, alpha, mask, (x0, y0), eid)
    images = [e for e in d.elements if e["type"] != "text"]
    z = max([e["z"] for e in images], default=0) + 0.5
    d.elements.append({"id": eid, "name": a.what[:40], "type": "image", "src": src, "box": box, "z": z})
    d.renumber()
    d.changes.append(f"added {eid}: {a.what}")


def cmd_restyle(d, a):
    base = composite(d)
    prompt = (f"{a.instruction}. Keep the layout: every product and graphic stays where it is with roughly the same "
              "size, and empty areas stay empty (text will be placed there later)." + NO_TEXT)
    out = paint([base], prompt, a.model, (d.W, d.H))
    alpha, fg = matte(out)
    merge_px = max(3, int(0.012 * min(d.W, d.H)))
    obs = [{"name": "object", "mask": m} for m in components(alpha, merge_px, max(40, int(0.0008 * d.W * d.H)))]
    attach_nearby(out, obs)
    union = np.zeros((d.H, d.W), bool)
    for ob in obs:
        union |= ob["mask"]
    g = max(4, int(0.008 * min(d.W, d.H)))
    soft = (alpha >= 16) & dilate(union, merge_px)
    bg = erase(out, dilate(union | soft, g))
    name = d.asset("bg")
    Image.fromarray(bg).save(os.path.join(d.dir, name))
    d.scene["background"] = name
    texts = [e for e in d.elements if e["type"] == "text"]
    rgb = np.where((alpha >= 250)[..., None] | (alpha < 16)[..., None], out, fg)
    # New ids: the old objects are gone, and a stale id in the chat must
    # not silently point at a different object.
    v = d.scene["version"] + 1
    new = []
    for i, ob in enumerate(sorted(obs, key=lambda o: -o["mask"].sum()), start=1):
        src, box = cut_layer(d, rgb, alpha, ob["mask"], (0, 0), f"v{v}o{i}")
        new.append({"id": f"v{v}o{i}", "name": "object", "type": "image", "src": src, "box": box, "z": float(i)})
    for t in texts:
        t["z"] = t["z"] + len(new) + 1
    d.scene["elements"] = new + texts
    d.renumber()
    save_overlay(out, [{"name": e["id"], "mask": ob["mask"]} for e, ob in
                       zip(new, sorted(obs, key=lambda o: -o["mask"].sum()))], os.path.join(d.dir, "objects.png"))
    d.changes.append(f"restyled ({len(new)} objects re-split): {a.instruction}")


def parse_box(s):
    v = [float(t) for t in s.replace("box=", "").split(",")]
    if len(v) != 4:
        raise SystemExit("box must be x,y,w,h")
    return [int(round(t)) for t in v]


def cmd_set(d, a):
    el = d.get(a.id)
    for kv in a.pairs:
        k, _, v = kv.partition("=")
        if k == "box":
            el["box"] = parse_box(v)
        elif k in ("x", "y", "w", "h"):
            el["box"]["xywh".index(k)] = int(float(v))
        elif k == "z":
            if v == "front":
                el["z"] = d.top_z() + 1
            elif v == "back":
                el["z"] = -1
            else:
                el["z"] = float(v)
            d.renumber()
        elif k in ("hidden",):
            el["hidden"] = v not in ("0", "false", "")
        elif k in ("opacity", "font_size"):
            el[k] = float(v)
        elif k == "font_weight":
            el[k] = int(v)
        elif k in ("text", "color", "font_family"):
            el[k] = v
            if k == "color":
                el.pop("gradient", None)
        elif k == "fill" and el["type"] == "shape":
            el["style"]["background"] = v
        else:
            raise SystemExit(f"can't set {k!r} on {el['type']}")
        if el["type"] == "text" and k in ("text", "font_family", "font_size", "font_weight", "box"):
            el.pop("css", None)  # re-fit the line to its (new) box
    d.changes.append(f"set {a.id}: {' '.join(a.pairs)}")


def cmd_add_text(d, a):
    eid = d.new_id("t")
    x, y, w, h = parse_box(a.box)
    el = {"id": eid, "type": "text", "text": a.text, "box": [x, y, w, h], "z": d.top_z() + 1,
          "color": a.color, "font_size": round(h / 0.9, 1), "font_weight": a.font_weight}
    if a.font_family:
        el["font_family"] = a.font_family
    d.elements.append(el)
    d.renumber()
    d.changes.append(f"added text {eid}: {a.text}")


def cmd_remove(d, a):
    for eid in a.ids:
        d.elements.remove(d.get(eid))
        d.changes.append(f"removed {eid}")


def cmd_undo(d, a):
    hist = sorted(glob.glob(os.path.join(d.dir, "history", "v*.json")),
                  key=lambda p: int(os.path.basename(p)[1:-5]))
    if not hist:
        raise SystemExit("nothing to undo")
    prev = json.load(open(hist[-1]))
    os.remove(hist[-1])
    prev["version"] = d.scene["version"] + 1  # keep versions increasing for the page
    json.dump(prev, open(d.path, "w"), ensure_ascii=False, indent=1)
    d.scene = prev
    d.changes.append(f"undid back to the state before v{d.scene['version'] - 1}")


def summary(d, html_path=None, png_path=None, notes=()):
    els = []
    for e in sorted(d.elements, key=lambda e: -e["z"]):
        row = {"id": e["id"], "type": e["type"], "box": [int(v) for v in e["box"]]}
        if e["type"] == "text":
            row["text"] = e["text"]
        else:
            row["name"] = e.get("name")
        if e.get("hidden"):
            row["hidden"] = True
        els.append(row)
    out = {"version": d.scene["version"], "user_edits": list(notes), "changes": d.changes, "elements": els}
    if html_path:
        out.update(html=html_path, preview=png_path)
    return out


def dumps(out):
    """Readable JSON with one element per line."""
    rows = ",\n  ".join(json.dumps(e, ensure_ascii=False) for e in out.pop("elements"))
    head = json.dumps(out, ensure_ascii=False, indent=1)[:-2]
    return head + ',\n "elements": [\n  ' + rows + "\n ]\n}"


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)

    def add(name, **kw):
        p = sub.add_parser(name, **kw)
        p.add_argument("out_dir")
        return p

    add("status")
    add("render")
    add("undo")
    p = add("set")
    p.add_argument("id")
    p.add_argument("pairs", nargs="+")
    p = add("add-text")
    p.add_argument("text")
    p.add_argument("box")
    p.add_argument("--color", default="#111111")
    p.add_argument("--font-family", dest="font_family", default="")
    p.add_argument("--font-weight", dest="font_weight", type=int, default=400)
    p = add("remove")
    p.add_argument("ids", nargs="+")
    for name, args in (("edit", ["id", "instruction"]), ("edit-bg", ["instruction"]),
                       ("add", ["what", "box"]), ("restyle", ["instruction"])):
        p = add(name)
        for x in args:
            p.add_argument(x)
        p.add_argument("--model", default="nano-banana-2", choices=list(MODELS))
    a = ap.parse_args()
    if a.cmd in ("edit", "edit-bg", "add", "restyle") and not os.environ.get("FAL_KEY"):
        raise SystemExit("FAL_KEY is not set")

    d = Design(a.out_dir)
    notes = sync(d)
    if a.cmd == "status":
        print(dumps(summary(d, notes=notes)))
        return
    handlers = {"set": cmd_set, "add-text": cmd_add_text, "remove": cmd_remove, "edit": cmd_edit,
                "edit-bg": cmd_edit_bg, "add": cmd_add, "restyle": cmd_restyle}
    if a.cmd == "undo":
        cmd_undo(d, a)
    elif a.cmd in handlers:
        handlers[a.cmd](d, a)
        d.save()
    html_path, png_path = render(d)
    print(dumps(summary(d, html_path, png_path, notes)))


if __name__ == "__main__":
    main()
