#!/usr/bin/env python3
"""Image -> editable single-file HTML.

  pipeline.py run <image> [--extras "logo; icon; ..."] [--out-dir DIR] [--title T]
  pipeline.py rebuild <out_dir> [--title T]

run:     OCR, text removal and empty background (nano-banana-2),
         foreground cut-out (BiRefNet; SAM 3 for --extras), HTML, checks.
         Needs FAL_KEY. Each step's log goes to <out_dir>/log.txt.
rebuild: after editing <out_dir>/scene/scene.json, re-assemble and re-check.

Prints a JSON summary: the HTML path, every element (so the caller can
proof-read text and review objects), and the check scores.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
PY = sys.executable
LOG = None  # every step's stderr is appended here (<out dir>/log.txt)


def step(name, *args):
    t = time.time()
    r = subprocess.run([PY, os.path.join(HERE, name), *args], capture_output=True, text=True)
    if LOG:
        with open(LOG, "a") as f:
            f.write(f"== {name}\n{r.stderr}")
    if r.returncode != 0:
        sys.stderr.write(r.stdout + r.stderr)
        if "AccountError" in r.stderr:
            line = [l for l in r.stderr.splitlines() if "AccountError" in l][-1]
            raise SystemExit("FAL_ACCOUNT_ERROR: " + line.split(":", 1)[-1].strip())
        raise SystemExit(f"{name} failed (exit {r.returncode})")
    print(f"[{name}] {r.stdout.strip().splitlines()[-1] if r.stdout.strip() else 'ok'} ({time.time() - t:.0f}s)",
          file=sys.stderr)
    return r.stdout


def build_and_check(out_dir, title):
    global LOG
    LOG = os.path.join(out_dir, "log.txt")
    meta = json.load(open(os.path.join(out_dir, "meta.json")))
    scene_dir = os.path.join(out_dir, "scene")
    html_path = os.path.join(out_dir, meta["stem"] + ".html")
    step("build_html.py", scene_dir, html_path, "--title", title or meta["stem"])
    check = json.loads(step("edit_check.py", html_path, meta["source"], os.path.join(out_dir, "check")))
    # A flat preview for chats that can't open HTML (WeChat, Feishu, ...).
    os.makedirs(os.path.join(out_dir, "previews"), exist_ok=True)
    version = json.load(open(os.path.join(out_dir, "scene", "scene.json"))).get("version", 1)
    preview = os.path.join(out_dir, "previews", f'{meta["stem"]}-v{version}.png')
    shutil.copy(os.path.join(out_dir, "check", "static.png"), preview)
    scene = json.load(open(os.path.join(scene_dir, "scene.json")))
    counts = {}
    for e in scene["elements"]:
        counts[e["type"]] = counts.get(e["type"], 0) + 1
    print(json.dumps({
        "html": os.path.abspath(html_path),
        "preview": os.path.abspath(preview),
        "scene": os.path.abspath(os.path.join(scene_dir, "scene.json")),
        "objects_preview": os.path.abspath(os.path.join(scene_dir, "objects.png")),
        "check_images": os.path.abspath(os.path.join(out_dir, "check")),
        "elements": counts,
        "check": {
            "static_ssim": check["static_ssim"],
            "worst_regions": check["worst_regions"],
            "editability": check["score"],
            "problems": {k: check[k] for k in ("ghost_text", "bg_ghost_text", "dirty_elements", "exposed_artifacts")
                         if check[k]},
        },
        "objects": [{"id": e["id"], "name": e.get("name"), "type": e["type"], "box": e["box"]}
                    for e in scene["elements"] if e["type"] != "text"],
        "texts": [{"id": e["id"], "text": e["text"], "box": e["box"]}
                  for e in scene["elements"] if e["type"] == "text"],
    }, ensure_ascii=False, indent=1))


def run(a):
    if not os.environ.get("FAL_KEY"):
        raise SystemExit("FAL_KEY is not set (the models run on fal.ai)")
    src = os.path.abspath(a.image)
    stem = os.path.splitext(os.path.basename(src))[0]
    out = os.path.abspath(a.out_dir or os.path.join(os.path.dirname(src), stem + "-editable"))
    os.makedirs(out, exist_ok=True)
    global LOG
    LOG = os.path.join(out, "log.txt")
    open(LOG, "w").close()
    json.dump({"source": src, "stem": stem, "title": a.title or stem, "extras": a.extras}, open(os.path.join(out, "meta.json"), "w"))
    ocr = os.path.join(out, "ocr.json")
    step("ocr.py", src, ocr)
    step("build_scene.py", src, ocr, os.path.join(out, "scene"), "--extras", a.extras)
    build_and_check(out, a.title)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run")
    r.add_argument("image")
    r.add_argument("--extras", default="",
                   help='flat graphics to cut out besides the photo objects, ";"-separated (logo; badge; icon)')
    r.add_argument("--out-dir")
    r.add_argument("--title")
    b = sub.add_parser("rebuild")
    b.add_argument("out_dir")
    b.add_argument("--title")
    a = ap.parse_args()
    if a.cmd == "run":
        run(a)
    else:
        build_and_check(os.path.abspath(a.out_dir), a.title)


if __name__ == "__main__":
    main()
