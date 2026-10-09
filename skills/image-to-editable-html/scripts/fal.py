"""Minimal fal.ai queue client (stdlib only). Needs FAL_KEY."""
import base64
import io
import json
import os
import time
import urllib.error
import urllib.request

import numpy as np
from PIL import Image


class AccountError(RuntimeError):
    """The fal.ai account can't run anything (balance exhausted, key
    revoked): no fallback will help, so callers let it through."""


def _retry(fn, tries=4):
    """Transient network errors (timeouts, resets, 5xx) are retried."""
    for i in range(tries):
        try:
            return fn()
        except urllib.error.HTTPError as e:
            if e.code in (401, 403):
                try:
                    detail = json.loads(e.read()).get("detail", "")
                except Exception:  # noqa: BLE001
                    detail = ""
                raise AccountError(f"fal.ai refused the request ({e.code}): {detail or e.reason}") from e
            if e.code < 500 and e.code != 429 or i == tries - 1:
                raise
        except (urllib.error.URLError, TimeoutError, ConnectionError, OSError):
            if i == tries - 1:
                raise
        time.sleep(1.5 * (i + 1))


def _call(url, payload=None):
    def go():
        req = urllib.request.Request(url, method="POST" if payload is not None else "GET")
        req.add_header("Authorization", "Key " + os.environ["FAL_KEY"])
        data = None
        if payload is not None:
            req.add_header("Content-Type", "application/json")
            data = json.dumps(payload).encode()
        with urllib.request.urlopen(req, data, timeout=60) as r:
            return json.loads(r.read())
    return _retry(go)


def fetch(url):
    if url.startswith("data:"):
        return base64.b64decode(url.split(",", 1)[1])

    def go():
        with urllib.request.urlopen(url, timeout=30) as r:
            return r.read()
    return _retry(go)


def data_url(img):
    """PIL image or HxW[x3|4] uint8 array -> PNG data URL."""
    if isinstance(img, np.ndarray):
        img = Image.fromarray(img)
    buf = io.BytesIO()
    img.save(buf, "PNG")
    return "data:image/png;base64," + base64.b64encode(buf.getvalue()).decode()


def load(url, mode="RGB"):
    return Image.open(io.BytesIO(fetch(url))).convert(mode)


def run(endpoint, payload, timeout=600):
    """Submit to the queue and wait for the result."""
    job = _call("https://queue.fal.run/" + endpoint, payload)
    t = time.time()
    while True:
        st = _call(job["status_url"])
        if st.get("status") == "COMPLETED":
            return _call(job["response_url"])
        if st.get("status") not in ("IN_QUEUE", "IN_PROGRESS"):
            raise RuntimeError(f"fal {endpoint} failed: {st}")
        if time.time() - t > timeout:
            raise TimeoutError(f"fal {endpoint} timed out")
        time.sleep(1.5)
