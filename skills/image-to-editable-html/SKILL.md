---
name: image-to-editable-html
description: Turn an image of a poster, flyer, menu, banner or UI screenshot into a single editable HTML file — every text line becomes real editable text, every object (a product with everything on and around it, an illustration, a logo, an icon) is cut from the background as one movable piece, and the page has a built-in editor (drag, resize, edit text, layers panel, export). Use when the user sends an image and asks to make it editable, split it into layers/components, "convert to HTML", or "拆图层 / 转成可编辑的网页".
metadata:
  fastclaw:
    requires:
      anyBins: [uv, python3]
    env:
      - name: FAL_KEY
        description: fal.ai API key. Image editing (nano-banana-2), foreground cut-out (BiRefNet), segmentation (SAM 3) and the fallback eraser (Bria) run on fal.ai.
        required: true
        secret: true
---

# Image → editable HTML

Input: one image (poster, menu or UI screenshot). Output: one self-contained
`.html` file that looks like the image, where every element can be selected,
moved, resized, edited or hidden, and which exports a clean copy.

Granularity: **all text is live, editable text; everything else that stands
in front of the background comes out as whole objects** — an ice-cream cup
with its scoop, spoon, wafer and the fruit beside it is one piece, not five.
Moving it reveals a clean background.

How it works: OCR finds the text (sideways lines too); an image-editing
model (nano-banana-2) removes the text and paints the empty background,
two samples at a time since its output varies; a foreground-matting model
(BiRefNet) cuts out the objects; flat graphics you name (logos, icons,
divider lines) are found with SAM 3. Every model result is checked line by
line and object by object, and whatever fails falls back to a local fill.

## 1. Find the image file

The user's image arrives as an attachment line such as
`[Attached: /workspace/image_x1y2z_0.jpg]`. The file is in your Working
Directory under that filename (on the host, use the Working Directory path
from Runtime info, not `/workspace`). If there is no attachment, ask the user
to send the image.

## 2. Name the flat graphics (optional)

Photographic objects are found automatically. Flat graphics usually count
as background for a matting model, so name the ones that should also move:
logos, badges, icons, divider lines, card shapes or buttons in UI
screenshots. Separate names with `;`. One name finds every instance (`icon`
gets all icons). SAM 3 doesn't know every word, so give alternatives with
`|`: `avatar|profile picture`, `round badge|badge`. `name @ x,y` (percent
of width, height) keeps only the instance nearest that point.

Examples:
- menu / poster: `planet logo|logo; round badge|badge; round icon|icon; thin gold line`
- chat UI: `search box; chat bubble; avatar|profile picture|person; red notification badge|red circle; icon`

Leave it empty when there is nothing like that.

## 3. Run the pipeline in the background

It takes 2–4 minutes, more for busy images like menus (the very first run also spends a minute or two
installing its Python environment). Tell the user in one short line that
you're on it, then start it with `exec` and `run_in_background: true`:

```bash
bash {baseDir}/scripts/run.sh run "<Working Directory>/<image file>" --extras "<names from step 2>" --title "<short title>"
```

Poll with `bash_output` until it exits. If it stops with
`FAL_ACCOUNT_ERROR` (the image service's balance is used up or its key is
invalid), nothing will work until that is fixed: tell the user in one plain
sentence that the image service account needs topping up, and don't retry. The final stdout is a JSON summary:

- `html` — the generated page (inside `<image stem>-editable/` next to the image)
- `scene` — `scene.json`, the editable source of the page
- `objects_preview` — the source with every object tinted and labelled
- `objects` / `texts` — every element: `id`, `name` or `text`, `box` [x, y, w, h]
- `check.static_ssim` — unedited page vs the image (1.0 = identical; text set
  in a web font usually lands around 0.75–0.85)
- `check.editability` — counts of problems found by moving and hiding every
  element: ghost text left in the background, text fragments inside image
  layers, broken areas revealed when an element is moved. All should be 0.

## 4. Review the objects

If you can view images, open `objects_preview` (every object tinted and
numbered) and compare it with the image. Photo objects are named `object`;
graphics carry the name you gave. A logo, icon or line that should move but
isn't tinted → run step 3 again with it (or another word for it) in
`--extras`.

## 5. Proof-read the text and set the fonts

OCR is good but not perfect, especially on stylised titles. Compare every
entry in `texts` with the image and fix mistakes by editing `scene.json`
(`edit_file` on the path from `scene`):

- wrong or missing characters → correct the `text` field
- a line that isn't real text (OCR noise on an icon) → delete that element
- one word split into pieces because something covers part of it (e.g.
  "SW" + "ET" for SWEET behind an object) → keep one element, set its
  `text` to the whole word and its `box` to the union of the pieces, delete
  the others
- an obviously wrong weight or colour on a headline → adjust `font_weight`
  (400/600/700/900) or `color`
- a line with `rotate` (-90 / 90) is set sideways; a word partly hidden
  behind a product may be read short ("FFLE" for WAFFLE) — correct `text`

Then match the typography: look at each distinct style in the image and
set `font_family` (any Google Fonts family name) plus a `font_weight` that
family actually has. The page scales each line to its box, so pick the face
whose letter shapes match; width is adjusted automatically. Good defaults:
condensed heavy display → `Anton` (400) or `Oswald` (700); classic serif
headline → `Playfair Display`; book serif body → `EB Garamond`; wide all-caps
serif logo → `Cinzel`; geometric sans → `Montserrat`; clean sans / UI text →
`Inter`; Chinese → `Noto Sans SC` or `Noto Serif SC`. Leave `font_family`
unset when the default sans already matches.

Don't touch image elements or other `box` values. Then rebuild:

```bash
bash {baseDir}/scripts/run.sh rebuild "<out dir containing scene/>"
```

(foreground `exec` is fine — it takes ~20 s). Skip the rebuild if nothing
needed fixing.

## 6. Deliver

The person you're talking to is not technical. Never mention HTML, pages,
files, layers, scene.json, element ids, versions, scores, models or
commands — speak about "the picture", "product 03", "the background", "the
title".

Reply with the result picture (`preview` in the summary) shown inline and
the editable version (`html`) linked, both by path relative to your Working
Directory:

```
![效果图](poster-editable/previews/poster-v1.png)
[打开可编辑版本](poster-editable/poster.html)
```

then one or two plain sentences, e.g. "拆好了：文字都能直接改，每个商品都能
单独替换或挪动。想改什么直接告诉我（比如把 03 换成草莓味、背景换成大理石），
也可以打开可编辑版本，双击文字直接改、拖动调整位置。"

If `check.editability` isn't all 0 or `static_ssim` is below ~0.7, say in
plain words where it may look off ("左下角背景补得不太自然") and offer to fix
it — not the numbers.

## 7. Follow-up changes in conversation

Once a design exists, never run the pipeline again for changes — edit the
design with `scene.py` (foreground `exec`; seconds for text, 20–40 s per
image edit, about a minute for a restyle):

```bash
bash {baseDir}/scripts/run.sh scene <command> "<out dir>" [args]
```

`<out dir>` is the `<image stem>-editable` folder. Start every follow-up
turn with `status`: it lists every element (`id`, `type`, `box`, `text` or
`name`) and **merges the edits the user made by hand in the page**
(`user_edits` says what they changed — mention it if it matters, never undo
it). If you can view images, look at `preview` to locate things.

| The user wants… | Command |
|---|---|
| different wording, colour, font, size, position of a text | `set <id> text="…" color=#… font_family="…" font_weight=700 box=x,y,w,h` |
| a new text line | `add-text "<text>" x,y,w,h --color "#…" --font-family "…"` |
| one product / object changed ("换成草莓味") | `edit <id> "<instruction in English>"` |
| the background changed | `edit-bg "<instruction>"` |
| something added ("加一枝薄荷") | `add "<what>" x,y,w,h` — a free area next to where it belongs |
| something removed | `remove <id> [<id> …]` |
| an object moved / resized / brought forward | `set <id> box=x,y,w,h` / `set <id> z=front` |
| an overall new look ("改成圣诞风") | `restyle "<instruction>"` (objects get new ids) |
| "撤销" / go back | `undo` (repeatable) |

- Text is never repainted by an image model: change wording with `set`,
  and when an image edit changes what a label should say (chocolate →
  strawberry), also `set` the matching text.
- Image instructions work best in English, specific and visual: "replace the
  chocolate gelato with strawberry gelato and put two fresh strawberries
  beside the cup". `--model gpt-image-2` is the alternative when
  `nano-banana-2` (default) doesn't follow an instruction.
- A message ending in `〔选中：图片 el9〕` or `〔选中：文字 t17「…」〕` was typed
  by the user with that element selected in the editable version — that id
  is the element to change. Never repeat ids back to the user.
- Photo objects are all named `object`; tell them apart by `box` (row and
  column on the page) or the preview.

Every command re-renders the picture. Reply with the new picture inline
(`![效果图](…)` with the new `preview` path — each change gets its own
picture) and one plain line on what changed
("03 换成草莓味了，品名也改成了 Strawberry Fudge"); link the editable
version again only if they may want to tweak it by hand. Don't explain how
it was done. The editable version open in the chat refreshes by itself.
