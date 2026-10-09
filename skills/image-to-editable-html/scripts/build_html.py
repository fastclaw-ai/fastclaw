#!/usr/bin/env python3
"""Assemble scene.json + assets into one self-contained, editable HTML file.

Usage: build_html.py <scene_dir> <out.html> [--title TITLE]

Every element is absolutely positioned on a canvas the size of the source
image. The page embeds a small editor: click to select, drag to move,
handles to resize, double-click text to edit, a layers panel, a property
panel, undo, and "Export HTML" which saves a clean copy without the editor
chrome.
"""
import argparse
import base64
import html
import json
import mimetypes
import os
import urllib.parse

# A web font first so the page looks the same on macOS, Windows and the
# headless Linux browser used for the render check; system CJK fonts are
# the fallback when Google Fonts is unreachable.
FONT_LINK = ('<link rel="preconnect" href="https://fonts.googleapis.com">'
             '<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>'
             '<link href="https://fonts.googleapis.com/css2?family=Noto+Sans+SC:wght@400;500;700;900&display=swap" rel="stylesheet">')
FONT_STACK = ('"Noto Sans SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", '
              '-apple-system, BlinkMacSystemFont, "Helvetica Neue", Arial, sans-serif')


def data_uri(path):
    mime = mimetypes.guess_type(path)[0] or "image/png"
    with open(path, "rb") as f:
        return f"data:{mime};base64,{base64.b64encode(f.read()).decode()}"


def turn(rotate):
    """CSS that lays a sideways line along its tall box (transform-origin is
    the top left corner): -90 reads bottom to top, 90 top to bottom."""
    if not rotate:
        return "none"
    return "translateY(100%) rotate(-90deg)" if rotate < 0 else "translateX(100%) rotate(90deg)"


def element_html(el, scene_dir):
    x, y, w, h = el["box"]
    name = html.escape(el.get("name") or el["id"])
    hidden = " fc-hidden" if el.get("hidden") else ""
    opacity = f'opacity:{el["opacity"]};' if el.get("opacity") not in (None, 1, 1.0) else ""
    rot = f' data-rotate="{int(el["rotate"])}"' if el["type"] == "text" and el.get("rotate") else ""
    base = (f'class="fc-el fc-{el["type"]}{hidden}" data-id="{el["id"]}" data-name="{name}"{rot} '
            f'style="left:{x}px;top:{y}px;width:{w}px;height:{h}px;z-index:{el["z"]};{opacity}')
    if el["type"] == "image":
        src = data_uri(os.path.join(scene_dir, el["src"]))
        return f'<div {base}"><img src="{src}" alt="{name}" draggable="false"></div>'
    if el["type"] == "shape":
        st = el["style"]
        return (f'<div {base}background:{st["background"]};'
                f'border-radius:{st.get("borderRadius", 0)}px;"></div>')
    # text
    weight = el.get("font_weight", 400)
    align = el.get("align", "left")
    paint = f'color:{el["color"]};'
    if el.get("gradient"):
        top, bot = el["gradient"]
        paint += (f'background:linear-gradient(180deg,{top},{bot});'
                  '-webkit-background-clip:text;background-clip:text;-webkit-text-fill-color:transparent;')
    family = ""
    if el.get("font_family"):
        family = f'font-family:&quot;{html.escape(el["font_family"])}&quot;, {FONT_STACK.replace(chr(34), "&quot;")};'
    css = el.get("css")
    if css:
        # Laid out by hand in the editor: keep exactly what the user saw.
        fit = (f'font-size:{css.get("fontSize", str(el["font_size"]) + "px")};'
               f'line-height:{css.get("lineHeight", str(h) + "px")};'
               f'letter-spacing:{css.get("letterSpacing", "normal")};'
               f'transform:{css.get("transform", "none")};" data-frozen="1">')
    else:
        fit = (f'font-size:{el["font_size"]}px;line-height:{w if rot else h}px;'
               f'transform:{turn(el.get("rotate"))};" data-fit="1">')
    return (f'<div {base}{paint}{family}font-weight:{weight};text-align:{align};{fit}'
            f'{html.escape(el["text"])}</div>')


def font_links(elements):
    """One Google Fonts stylesheet per family (weights it is used with), so
    an unknown family or missing weight only drops that family back to the
    default stack instead of breaking every font."""
    used = {}
    for el in elements:
        if el["type"] == "text" and el.get("font_family"):
            used.setdefault(el["font_family"], set()).add(int(el.get("font_weight", 400)))
    out = []
    for fam, weights in sorted(used.items()):
        q = urllib.parse.quote_plus(fam)
        out.append(f'<link href="https://fonts.googleapis.com/css2?family={q}:wght@'
                   f'{";".join(str(w) for w in sorted(weights))}&display=swap" rel="stylesheet">')
    return "".join(out)


TEMPLATE = r"""<!doctype html>
<html lang="zh">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>__TITLE__</title>
__FONTLINK__
<style>
:root { --accent: #3b82f6; }
* { box-sizing: border-box; }
html, body { margin: 0; height: 100%; background: #1f2023; font-family: __FONT__; }
#fc-app { display: flex; height: 100%; }
#fc-viewport { flex: 1; overflow: auto; display: flex; align-items: flex-start; justify-content: center; padding: 24px; }
#fc-scaler { transform-origin: top left; }
#fc-canvas { position: relative; width: __W__px; height: __H__px; background: center/100% 100% no-repeat; overflow: hidden; box-shadow: 0 8px 40px rgba(0,0,0,.45); }
.fc-el { position: absolute; user-select: none; }
.fc-el img { width: 100%; height: 100%; display: block; pointer-events: none; }
.fc-text { white-space: pre; overflow: visible; font-family: __FONT__; transform-origin: left center; }
.fc-text[data-rotate] { transform-origin: 0 0; }
.fc-text[contenteditable="true"] { user-select: text; cursor: text; outline: 2px dashed var(--accent); }
.fc-hidden { display: none !important; }
body.fc-editing .fc-el { cursor: move; }
body.fc-editing .fc-el:hover { outline: 1px solid rgba(59,130,246,.6); }
#fc-sel { position: absolute; border: 1.5px solid var(--accent); pointer-events: none; z-index: 100000; display: none; }
#fc-sel i { position: absolute; width: 10px; height: 10px; background: #fff; border: 1.5px solid var(--accent); pointer-events: auto; }
#fc-sel i[data-h="nw"] { left: -6px; top: -6px; cursor: nwse-resize; }
#fc-sel i[data-h="ne"] { right: -6px; top: -6px; cursor: nesw-resize; }
#fc-sel i[data-h="sw"] { left: -6px; bottom: -6px; cursor: nesw-resize; }
#fc-sel i[data-h="se"] { right: -6px; bottom: -6px; cursor: nwse-resize; }
#fc-panel { width: 280px; background: #2a2b2f; color: #e5e7eb; font-size: 13px; display: flex; flex-direction: column; border-left: 1px solid #3a3b40; }
#fc-panel h3 { margin: 0; padding: 12px 14px 8px; font-size: 12px; text-transform: uppercase; letter-spacing: .06em; color: #9ca3af; }
#fc-toolbar { display: flex; flex-wrap: wrap; gap: 6px; padding: 12px 14px; border-bottom: 1px solid #3a3b40; }
#fc-toolbar button, #fc-props button { background: #3a3b40; color: #e5e7eb; border: 0; border-radius: 6px; padding: 6px 10px; font-size: 12px; cursor: pointer; }
#fc-toolbar button:hover, #fc-props button:hover { background: #4a4b52; }
#fc-toolbar button.primary { background: var(--accent); color: #fff; }
#fc-props { padding: 0 14px 12px; display: grid; grid-template-columns: 70px 1fr; gap: 6px 8px; align-items: center; border-bottom: 1px solid #3a3b40; }
#fc-props input { width: 100%; background: #1f2023; color: #e5e7eb; border: 1px solid #3a3b40; border-radius: 6px; padding: 4px 6px; font-size: 12px; }
#fc-props input[type=color] { padding: 0; height: 26px; }
#fc-props .empty { grid-column: 1 / -1; color: #6b7280; }
#fc-layers { flex: 1; overflow: auto; padding: 0 6px 12px; }
.fc-layer { display: flex; align-items: center; gap: 6px; padding: 5px 8px; border-radius: 6px; cursor: pointer; }
.fc-layer:hover { background: #34353a; }
.fc-layer.active { background: rgba(59,130,246,.25); }
.fc-layer .kind { font-size: 10px; color: #9ca3af; width: 32px; }
.fc-layer .label { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.fc-layer .eye { opacity: .7; }
#fc-ask { padding: 10px 14px 12px; border-bottom: 1px solid #3a3b40; display: none; }
body.fc-embedded #fc-ask { display: block; }
#fc-ask .hint { color: #9ca3af; font-size: 12px; margin-bottom: 6px; }
#fc-ask textarea { width: 100%; min-height: 54px; resize: vertical; background: #1f2023; color: #e5e7eb; border: 1px solid #3a3b40; border-radius: 6px; padding: 6px 8px; font: inherit; font-size: 12px; }
#fc-ask button { margin-top: 6px; width: 100%; background: var(--accent); color: #fff; border: 0; border-radius: 6px; padding: 7px 10px; font-size: 12px; cursor: pointer; }
#fc-ask button:disabled { opacity: .5; cursor: default; }
#fc-saved { padding: 0 14px 8px; font-size: 11px; color: #6b7280; display: none; }
body.fc-embedded #fc-saved { display: block; }
@media (max-width: 760px) { #fc-app { flex-direction: column; } #fc-panel { width: 100%; height: 45%; } }
</style>
</head>
<body class="fc-editing">
<div id="fc-app">
  <div id="fc-viewport"><div id="fc-scaler"><div id="fc-canvas" data-version="__VERSION__" style="background-image:url(__BG__)">
__ELEMENTS__
  <div id="fc-sel"><i data-h="nw"></i><i data-h="ne"></i><i data-h="sw"></i><i data-h="se"></i></div>
  </div></div></div>
  <aside id="fc-panel">
    <div id="fc-toolbar">
      <button id="fc-undo" title="Cmd/Ctrl+Z">撤销</button>
      <button id="fc-up" title="上移一层">上移</button>
      <button id="fc-down" title="下移一层">下移</button>
      <button id="fc-del" title="Delete">删除</button>
      <button id="fc-bg" title="隐藏/显示背景">背景</button>
      <button id="fc-export" class="primary">下载</button>
    </div>
    <div id="fc-saved"></div>
    <div id="fc-ask">
      <div class="hint" id="fc-ask-hint">让 AI 修改整张图</div>
      <textarea id="fc-ask-text" placeholder="例如：把背景换成大理石桌面"></textarea>
      <button id="fc-ask-send">发送给 AI</button>
    </div>
    <h3>属性</h3>
    <div id="fc-props"><div class="empty">点击画布中的元素进行编辑</div></div>
    <h3>图层</h3>
    <div id="fc-layers"></div>
  </aside>
</div>
<script>
(() => {
  const canvas = document.getElementById('fc-canvas');
  const scaler = document.getElementById('fc-scaler');
  const viewport = document.getElementById('fc-viewport');
  const sel = document.getElementById('fc-sel');
  const props = document.getElementById('fc-props');
  const layers = document.getElementById('fc-layers');
  const W = __W__, H = __H__;
  let scale = 1, current = null, history = [];
  const bgUrl = canvas.style.backgroundImage;

  const els = () => [...canvas.querySelectorAll('.fc-el')];
  const kind = el => el.classList.contains('fc-text') ? '文字' : el.classList.contains('fc-shape') ? '形状' : '图片';
  const label = el => el.classList.contains('fc-text') ? el.textContent
    : (!el.dataset.name || el.dataset.name === 'object' ? '图片' : el.dataset.name);
  const num = v => parseFloat(v) || 0;

  function fit() {
    const avail = viewport.clientWidth - 48;
    scale = Math.min(1, avail / W);
    scaler.style.transform = `scale(${scale})`;
    scaler.style.width = W * scale + 'px';
    scaler.style.height = H * scale + 'px';
    place();
  }
  function snapshot() {
    history.push(canvas.innerHTML);
    if (history.length > 50) history.shift();
  }
  function undo() {
    if (!history.length) return;
    const id = current && current.dataset.id;
    canvas.innerHTML = history.pop();
    rebind();
    select(id ? canvas.querySelector(`[data-id="${id}"]`) : null);
  }
  function place() {
    if (!current) { sel.style.display = 'none'; return; }
    sel.style.display = 'block';
    for (const k of ['left', 'top', 'width', 'height']) sel.style[k] = current.style[k];
  }
  function select(el) {
    if (current && current !== el) current.removeAttribute('contenteditable');
    current = el || null;
    place();
    renderProps();
    renderLayers();
  }
  function field(name, value, type, onInput) {
    const l = document.createElement('label'); l.textContent = name;
    const i = document.createElement('input'); i.type = type; i.value = value;
    i.addEventListener('focus', snapshot);
    i.addEventListener('input', () => { onInput(i.value); place(); renderLayers(); });
    props.append(l, i);
  }
  function toHex(c) {
    const m = c.match(/\d+/g); if (!m) return c || '#000000';
    return '#' + m.slice(0, 3).map(n => (+n).toString(16).padStart(2, '0')).join('');
  }
  function renderProps() {
    props.innerHTML = '';
    if (!current) { props.innerHTML = '<div class="empty">点击画布中的元素进行编辑</div>'; return; }
    const s = current.style;
    field('X', num(s.left), 'number', v => s.left = v + 'px');
    field('Y', num(s.top), 'number', v => s.top = v + 'px');
    field('宽', num(s.width), 'number', v => s.width = v + 'px');
    field('高', num(s.height), 'number', v => s.height = v + 'px');
    if (current.classList.contains('fc-text')) {
      field('文字', current.textContent, 'text', v => current.textContent = v);
      field('字号', num(s.fontSize), 'number', v => s.fontSize = v + 'px');
      field('颜色', toHex(getComputedStyle(current).color), 'color', v => s.color = v);
      field('粗细', s.fontWeight || 400, 'number', v => s.fontWeight = v);
    } else if (current.classList.contains('fc-shape')) {
      field('填充', toHex(getComputedStyle(current).backgroundColor), 'color', v => s.background = v);
      field('圆角', num(s.borderRadius), 'number', v => s.borderRadius = v + 'px');
    }
    field('透明度', s.opacity === '' ? 1 : s.opacity, 'number', v => s.opacity = v);
  }
  function renderLayers() {
    layers.innerHTML = '';
    els().sort((a, b) => num(b.style.zIndex) - num(a.style.zIndex)).forEach(el => {
      const row = document.createElement('div');
      row.className = 'fc-layer' + (el === current ? ' active' : '');
      row.innerHTML = `<span class="kind"></span><span class="label"></span><span class="eye"></span>`;
      row.querySelector('.kind').textContent = kind(el);
      row.querySelector('.label').textContent = label(el);
      const eye = row.querySelector('.eye');
      eye.textContent = el.classList.contains('fc-hidden') ? '◌' : '●';
      eye.onclick = e => { e.stopPropagation(); snapshot(); el.classList.toggle('fc-hidden'); renderLayers(); };
      row.onclick = () => select(el);
      layers.append(row);
    });
  }
  function toCanvas(e) {
    const r = canvas.getBoundingClientRect();
    return { x: (e.clientX - r.left) / scale, y: (e.clientY - r.top) / scale };
  }
  function drag(e, onMove) {
    e.preventDefault();
    snapshot();
    const start = toCanvas(e);
    const s = current.style;
    const orig = { x: num(s.left), y: num(s.top), w: num(s.width), h: num(s.height), fs: num(s.fontSize), lh: num(s.lineHeight) };
    const move = ev => { const p = toCanvas(ev); onMove(p.x - start.x, p.y - start.y, orig, ev); place(); };
    const up = () => { removeEventListener('pointermove', move); removeEventListener('pointerup', up); renderProps(); };
    addEventListener('pointermove', move); addEventListener('pointerup', up);
  }
  function rebind() {
    els().forEach(el => {
      el.onpointerdown = e => {
        if (el.getAttribute('contenteditable') === 'true') return;
        select(el);
        drag(e, (dx, dy, o) => { el.style.left = o.x + dx + 'px'; el.style.top = o.y + dy + 'px'; });
      };
      if (el.classList.contains('fc-text')) {
        el.ondblclick = () => {
          snapshot();
          el.setAttribute('contenteditable', 'true');
          el.focus();
          document.getSelection().selectAllChildren(el);
        };
        el.oninput = () => { renderLayers(); };
        el.onblur = () => { el.removeAttribute('contenteditable'); renderProps(); };
      }
    });
    renderLayers();
  }
  sel.querySelectorAll('i').forEach(h => h.onpointerdown = e => {
    e.stopPropagation();
    const k = h.dataset.h;
    drag(e, (dx, dy, o, ev) => {
      let x = o.x, y = o.y, w = o.w, hh = o.h;
      if (k.includes('e')) w = o.w + dx;
      if (k.includes('s')) hh = o.h + dy;
      if (k.includes('w')) { w = o.w - dx; x = o.x + dx; }
      if (k.includes('n')) { hh = o.h - dy; y = o.y + dy; }
      if (ev.shiftKey || current.classList.contains('fc-image')) {   // keep aspect ratio
        const r = o.w / o.h;
        if (Math.abs(w - o.w) / o.w > Math.abs(hh - o.h) / o.h) hh = w / r; else w = hh * r;
        if (k.includes('n')) y = o.y + o.h - hh;
        if (k.includes('w')) x = o.x + o.w - w;
      }
      const s = current.style;
      s.left = x + 'px'; s.top = y + 'px'; s.width = Math.max(4, w) + 'px'; s.height = Math.max(4, hh) + 'px';
      if (current.classList.contains('fc-text') && o.fs) {
        // Type scales with the line's height — its width when it is sideways.
        const r = current.dataset.rotate ? Math.max(4, w) / o.w : Math.max(4, hh) / o.h;
        s.fontSize = o.fs * r + 'px';
        s.lineHeight = (o.lh || (current.dataset.rotate ? o.w : o.h)) * r + 'px';
      }
    });
  });
  canvas.addEventListener('pointerdown', e => { if (e.target === canvas) select(null); });
  document.getElementById('fc-undo').onclick = undo;
  document.getElementById('fc-del').onclick = () => { if (current) { snapshot(); current.remove(); select(null); } };
  document.getElementById('fc-up').onclick = () => { if (current) { snapshot(); current.style.zIndex = num(current.style.zIndex) + 1; renderLayers(); } };
  document.getElementById('fc-down').onclick = () => { if (current) { snapshot(); current.style.zIndex = Math.max(0, num(current.style.zIndex) - 1); renderLayers(); } };
  document.getElementById('fc-bg').onclick = () => { canvas.style.backgroundImage = canvas.style.backgroundImage ? '' : bgUrl; };
  document.getElementById('fc-export').onclick = () => {
    select(null);
    const clone = canvas.cloneNode(true);
    clone.querySelector('#fc-sel').remove();
    clone.querySelectorAll('[contenteditable]').forEach(n => n.removeAttribute('contenteditable'));
    clone.querySelectorAll('.fc-hidden').forEach(n => n.remove());
    const css = [...document.querySelectorAll('style')][0].textContent;
    const doc = `<!doctype html><html lang="zh"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${document.title}</title>${[...document.querySelectorAll('link[href*="fonts.g"]')].map(l => l.outerHTML).join('')}<style>${css} html,body{background:#fff} #fc-canvas{box-shadow:none;margin:0 auto}</style></head><body>${clone.outerHTML}</body></html>`;
    const a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([doc], { type: 'text/html' }));
    a.download = (document.title || 'design') + '.html';
    a.click();
  };
  addEventListener('keydown', e => {
    if (document.activeElement && (document.activeElement.isContentEditable || document.activeElement.tagName === 'INPUT')) return;
    if ((e.metaKey || e.ctrlKey) && e.key === 'z') { e.preventDefault(); undo(); return; }
    if (!current) return;
    const step = e.shiftKey ? 10 : 1, s = current.style;
    const moves = { ArrowLeft: [-step, 0], ArrowRight: [step, 0], ArrowUp: [0, -step], ArrowDown: [0, step] };
    if (moves[e.key]) { e.preventDefault(); snapshot(); s.left = num(s.left) + moves[e.key][0] + 'px'; s.top = num(s.top) + moves[e.key][1] + 'px'; place(); renderProps(); }
    if (e.key === 'Delete' || e.key === 'Backspace') { e.preventDefault(); snapshot(); current.remove(); select(null); }
    if (e.key === 'Escape') select(null);
  });
  // Match each line's rendered width to the source box once (system fonts
  // differ from the original); the result is baked into the styles, so
  // exported copies keep it.
  function fitText() {
    // Match the glyph ink to the OCR box: the font size makes the ink as
    // tall as the box, line-height puts it at the box's vertical centre,
    // letter-spacing absorbs small width differences and a horizontal
    // scale the rest (a condensed poster face set in a wider font).
    const ctx = document.createElement('canvas').getContext('2d');
    canvas.querySelectorAll('.fc-text[data-fit]').forEach(el => {
      el.removeAttribute('data-fit');
      const rot = +el.dataset.rotate || 0, turn = !rot ? '' : rot < 0 ? 'translateY(100%) rotate(-90deg) ' : 'translateX(100%) rotate(90deg) ';
      let bw = num(el.style.width), bh = num(el.style.height);
      if (rot) [bw, bh] = [bh, bw];   // a sideways line runs along the height
      const text = el.textContent, n = [...text].length;
      if (!bw || !bh || !text.trim()) return;
      const cs = getComputedStyle(el);
      const measure = fs => { ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${fs}px ${cs.fontFamily}`; return ctx.measureText(text); };
      let fs = num(el.style.fontSize), m = measure(fs);
      const inkH = m.actualBoundingBoxAscent + m.actualBoundingBoxDescent;
      if (inkH > 0) { fs *= Math.min(1.8, Math.max(0.6, bh / inkH)); m = measure(fs); }
      const asc = m.actualBoundingBoxAscent, desc = m.actualBoundingBoxDescent;
      const baseline = asc + (bh - asc - desc) / 2;
      const lh = 2 * baseline - m.fontBoundingBoxAscent + m.fontBoundingBoxDescent;
      const inkW = m.actualBoundingBoxLeft + m.actualBoundingBoxRight;
      let ls = n > 1 ? (bw - inkW) / (n - 1) : 0, sx = 1;
      const lim = fs * 0.05;
      if (Math.abs(ls) > lim) {
        ls = Math.sign(ls) * lim;
        sx = Math.min(1.6, Math.max(0.45, bw / (inkW + ls * (n - 1))));
      }
      el.style.fontSize = fs.toFixed(1) + 'px';
      if (lh > 0) el.style.lineHeight = lh.toFixed(1) + 'px';
      el.style.letterSpacing = ls.toFixed(2) + 'px';
      if (sx !== 1 || rot) el.style.transform = turn + (sx !== 1 ? `scaleX(${sx.toFixed(3)})` : '');
    });
  }
  // Inside the FastClaw chat: hand every manual edit to the host page,
  // which saves it next to scene.json so the agent's next change starts
  // from what the user sees; and let the user give the agent instructions
  // about the selected element. Standalone (opened as a file) none of
  // this shows.
  const embedded = window.parent !== window;
  if (embedded) document.body.classList.add('fc-embedded');
  const version = +canvas.dataset.version || 1;
  const textCss = el => ({ fontSize: el.style.fontSize, lineHeight: el.style.lineHeight,
    letterSpacing: el.style.letterSpacing || 'normal', transform: el.style.transform || 'none' });
  function stateOf(el) {
    const s = el.style, st = { box: [num(s.left), num(s.top), num(s.width), num(s.height)].map(v => Math.round(v)),
      z: num(s.zIndex), hidden: el.classList.contains('fc-hidden'), opacity: s.opacity === '' ? 1 : +s.opacity };
    if (el.classList.contains('fc-text')) {
      Object.assign(st, { text: el.textContent, color: s.color, font_weight: +(s.fontWeight || 400), css: textCss(el) });
    } else if (el.classList.contains('fc-shape')) {
      st.style = { background: s.background || s.backgroundColor, borderRadius: num(s.borderRadius) };
    }
    return st;
  }
  let baseline = null, saveTimer = null;
  const savedNote = document.getElementById('fc-saved');
  function collect() {
    const now = {}, changed = {}, deleted = [];
    els().forEach(el => { now[el.dataset.id] = stateOf(el); });
    for (const [id, was] of Object.entries(baseline)) {
      const cur = now[id];
      if (!cur) { deleted.push(id); continue; }
      const diff = {};
      for (const k of Object.keys(cur)) if (JSON.stringify(cur[k]) !== JSON.stringify(was[k])) diff[k] = cur[k];
      if (Object.keys(diff).length) {
        // Text the user touched keeps its exact on-screen layout.
        if (cur.css && (diff.box || diff.text || diff.css)) { diff.css = cur.css; diff.box = cur.box; }
        changed[id] = diff;
      }
    }
    return { changed, deleted };
  }
  function scheduleSave() {
    if (!embedded || !baseline) return;
    clearTimeout(saveTimer);
    saveTimer = setTimeout(() => {
      const { changed, deleted } = collect();
      window.parent.postMessage({ type: 'fastclaw:scene-edits', version, changed, deleted }, '*');
      const n = Object.keys(changed).length + deleted.length;
      savedNote.textContent = n ? `已保存你的 ${n} 处修改` : '';
    }, 600);
  }
  function startSync() {
    if (!embedded) return;
    baseline = {};
    els().forEach(el => { baseline[el.dataset.id] = stateOf(el); });
    new MutationObserver(muts => {
      if (muts.some(m => !sel.contains(m.target) && m.target !== sel)) scheduleSave();
    }).observe(canvas, { attributes: true, attributeFilter: ['style', 'class'], childList: true, characterData: true, subtree: true });
  }
  const askText = document.getElementById('fc-ask-text');
  const askHint = document.getElementById('fc-ask-hint');
  const askSend = document.getElementById('fc-ask-send');
  function refreshAsk() {
    if (!current) {
      askHint.textContent = '让 AI 修改整张图';
      askText.placeholder = '例如：把背景换成大理石桌面';
    } else {
      askHint.textContent = current.classList.contains('fc-text')
        ? `让 AI 修改选中的文字「${label(current).slice(0, 16)}」` : `让 AI 修改选中的${kind(current)}`;
      askText.placeholder = current.classList.contains('fc-text') ? '例如：改成更醒目的金色' : '例如：换成草莓味，加两颗草莓';
    }
  }
  askSend.onclick = () => {
    const text = askText.value.trim();
    if (!text) return;
    // The element id rides along at the end for the agent; the user reads
    // their own words first.
    const what = current ? (current.classList.contains('fc-text') ? `文字 ${current.dataset.id}「${label(current).slice(0, 20)}」` : `${kind(current)} ${current.dataset.id}`) : '';
    window.parent.postMessage({ type: 'fastclaw:ask', text: what ? `${text} 〔选中：${what}〕` : text }, '*');
    askText.value = '';
    askSend.disabled = true;
    askSend.textContent = '已发送，改好后这里会自动更新';
  };
  askText.addEventListener('input', () => { askSend.disabled = false; askSend.textContent = '发送给 AI'; });
  askText.addEventListener('keydown', e => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) askSend.onclick(); });
  const selectBase = select;
  select = el => { selectBase(el); refreshAsk(); };
  addEventListener('resize', fit);
  rebind();
  (document.fonts ? document.fonts.ready : Promise.resolve()).then(() => { fitText(); place(); startSync(); });
  fit();
})();
</script>
</body>
</html>
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("scene_dir")
    ap.add_argument("out")
    ap.add_argument("--title", default="可编辑设计稿")
    a = ap.parse_args()
    scene = json.load(open(os.path.join(a.scene_dir, "scene.json")))
    body = "\n".join(element_html(e, a.scene_dir) for e in sorted(scene["elements"], key=lambda e: e["z"]))
    bg = data_uri(os.path.join(a.scene_dir, scene["background"]))
    out = (TEMPLATE.replace("__TITLE__", html.escape(a.title))
           .replace("__FONTLINK__", FONT_LINK + font_links(scene["elements"])).replace("__FONT__", FONT_STACK)
           .replace("__W__", str(scene["width"])).replace("__H__", str(scene["height"]))
           .replace("__BG__", bg).replace("__ELEMENTS__", body)
           .replace("__VERSION__", str(scene.get("version", 1))))
    with open(a.out, "w") as f:
        f.write(out)
    print(f"{len(scene['elements'])} elements -> {a.out} ({os.path.getsize(a.out) // 1024} KB)")


if __name__ == "__main__":
    main()
