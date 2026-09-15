package imageproc

// This is the ONLY file in the package allowed to import third-party
// imaging codecs (disintegration/imaging, golang.org/x/image/webp). A
// future swap to stdlib-only codecs should touch this file and nothing
// else.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	_ "image/gif" // GIF decode registration for imaging.Decode
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp" // WebP decode registration for imaging.Decode
)

// Tunable limits, overridable by environment. Read once, lazily, on the
// first Process call (tests can reset via resetLimitsForTest).
var (
	limitsOnce sync.Once
	// maxEdgePx caps the longest side of an inlined image. 2000px is
	// comfortably above every provider's internal downscale target
	// (Claude ~1568px, GPT-4o high-detail 2048px), so nothing the model
	// could have perceived is lost.
	maxEdgePx = 2000
	// byteBudget caps the RAW (decoded, pre-base64) byte size of an
	// inlined image. 3.75 MB base64-inflates to ~5 MB on the wire,
	// matching Anthropic's per-image API limit with headroom.
	byteBudget = 3_750_000
)

// Decompression-bomb guards are fixed constants, not env-tunable: they
// exist to protect the gateway process itself, not to tune quality.
const (
	// maxDecodeInputBytes refuses to decode inputs larger than 64 MB.
	// (The outer attachment cap is 25 MB, so this is a second belt.)
	maxDecodeInputBytes = 64 << 20
	// maxDecodePixels refuses to decode images whose header claims more
	// than 100 megapixels — a malicious or corrupt header could
	// otherwise force a multi-GB allocation.
	maxDecodePixels = 100_000_000
)

// loadLimits reads the env overrides exactly once. Invalid or
// non-positive values are ignored, keeping the defaults.
func loadLimits() {
	limitsOnce.Do(func() {
		if v, err := strconv.Atoi(os.Getenv("FASTCLAW_IMAGE_MAX_EDGE_PX")); err == nil && v > 0 {
			maxEdgePx = v
		}
		if v, err := strconv.Atoi(os.Getenv("FASTCLAW_IMAGE_BYTE_BUDGET")); err == nil && v > 0 {
			byteBudget = v
		}
	})
}

// resetLimitsForTest restores default limits and re-arms the lazy env
// read. Only for tests in this package.
func resetLimitsForTest() {
	maxEdgePx = 2000
	byteBudget = 3_750_000
	limitsOnce = sync.Once{}
}

// Result is the outcome of processing one uploaded image.
type Result struct {
	Decision Decision // Accept → Bytes may be inlined; Downgrade → store only
	MIME     string   // effective MIME of Bytes (sniffed format wins)
	Bytes    []byte   // final bytes: original on passthrough/downgrade, re-encoded otherwise
	// OrigWidth/OrigHeight are the header-claimed dimensions of the
	// input (0 when the header could not be parsed).
	OrigWidth  int
	OrigHeight int
	// Width/Height are the dimensions of Bytes (equals Orig* on
	// passthrough and downgrade).
	Width  int
	Height int
	// Reencoded is true when Bytes differ from the input (re-encoded /
	// resized). Callers should store Bytes instead of the original and
	// derive the file extension from MIME.
	Reencoded bool
	// Note is a short human-readable explanation (surfaced in logs).
	Note string
}

// Process sniffs, policy-gates and (when needed) compresses one image.
// declaredMIME is the caller-supplied Content-Type; the sniffed bytes
// always win over it. Process never returns an error and never panics:
// any failure downgrades to the original bytes.
func Process(data []byte, declaredMIME string) Result {
	loadLimits()
	declared := NormalizeMIME(declaredMIME)
	format := SniffImageFormat(data)

	// Stage 1+2: sniff + allowlist. Sniffed bytes win over the declared
	// MIME; anything not in the allowlist is downgraded untouched.
	if !acceptedFormat(format) {
		note := "not an image"
		switch {
		case format != FormatUnknown:
			note = "image format " + format.String() + " not in allowlist"
		case strings.HasPrefix(declared, "image/"):
			note = "declared " + declared + " but bytes are not a supported image"
		}
		return Result{
			Decision: DecisionDowngrade,
			MIME:     declared,
			Bytes:    data,
			Note:     note,
		}
	}
	mime := format.MIME()
	origW, origH, dimsOK := ImageDimensions(data, format)
	res := Result{
		Decision:   DecisionAccept,
		MIME:       mime,
		Bytes:      data,
		OrigWidth:  origW,
		OrigHeight: origH,
		Width:      origW,
		Height:     origH,
	}

	// GIF always passes through unchanged: re-encoding would flatten the
	// animation to its first frame. The outer 25 MB attachment cap is
	// the only size guard for GIFs.
	if format == FormatGIF {
		res.Note = "gif passthrough (animation preserved)"
		return res
	}

	// Already within both budgets → no codec work at all.
	if len(data) <= byteBudget && (!dimsOK || max(origW, origH) <= maxEdgePx) {
		res.Note = "within budgets, passthrough"
		return res
	}

	// Decompression-bomb guard: refuse to decode when the input is huge
	// or the header claims an absurd pixel count.
	if exceedsBombLimits(len(data), origW, origH, dimsOK) {
		res.Decision = DecisionDowngrade
		res.Note = "decompression-bomb guard: refusing to decode"
		return res
	}

	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		// Header parsed but the payload is corrupt — store, don't inline.
		res.Decision = DecisionDowngrade
		res.Note = "decode failed: " + err.Error()
		return res
	}
	return compressImage(img, format, res)
}

// exceedsBombLimits reports whether decoding this input would risk an
// outsized allocation. dimsOK=false (unparseable header) is not itself
// grounds to refuse — the decoder will fail on its own if the payload
// is corrupt.
func exceedsBombLimits(dataLen, w, h int, dimsOK bool) bool {
	if dataLen > maxDecodeInputBytes {
		return true
	}
	return dimsOK && w > 0 && h > 0 && w*h > maxDecodePixels
}

// compressImage runs the resize + re-encode ladder on a decoded image.
// res carries the already-filled accept fields; Bytes/MIME/dims are
// replaced with the best produced encoding.
func compressImage(img image.Image, format Format, res Result) Result {
	srcBounds := img.Bounds()
	srcW, srcH := srcBounds.Dx(), srcBounds.Dy()

	// Ladder policy (documented in issue #106):
	//   - Sources that were PNG or WebP are usually screenshots / UI
	//     captures where JPEG artifacts on text genuinely hurt, so each
	//     edge rung tries lossless PNG first and accepts it when it fits
	//     the byte budget.
	//   - Everything then falls to a JPEG quality ladder. Alpha is
	//     flattened over white before JPEG encoding — JPEG has no alpha
	//     channel and naive conversion renders transparent regions black.
	//   - If nothing fits the budget, the smallest produced encoding is
	//     returned (best effort beats dropping the image).
	preferPNG := format == FormatPNG || format == FormatWebP

	var best []byte
	bestMIME := ""
	for i, edge := range ladderEdges(maxEdgePx) {
		cur := img
		if max(srcW, srcH) > edge {
			cur = imaging.Fit(img, edge, edge, imaging.Lanczos)
		} else if i > 0 {
			continue // already tried this size at an earlier rung
		}
		if preferPNG {
			if out, err := encodePNG(cur); err == nil {
				if len(out) <= byteBudget {
					return finishCompression(res, cur, out, "image/png")
				}
				best, bestMIME = smaller(best, bestMIME, out, "image/png")
			}
		}
		flat := flattenAlpha(cur)
		for _, q := range []int{80, 60, 40, 20} {
			out, err := encodeJPEG(flat, q)
			if err != nil {
				break
			}
			if len(out) <= byteBudget {
				return finishCompression(res, cur, out, "image/jpeg")
			}
			best, bestMIME = smaller(best, bestMIME, out, "image/jpeg")
		}
	}

	if best == nil {
		// Every encoder failed — should be impossible; degrade safely.
		res.Decision = DecisionDowngrade
		res.Note = "all encoders failed"
		return res
	}
	res.Bytes = best
	res.MIME = bestMIME
	res.Reencoded = true
	// Final dims are approximate here (we kept only the bytes), so
	// report the header dims — the exact value no longer matters once
	// the budget could not be met.
	res.Note = "re-encoded best effort (still over budget)"
	return res
}

// finishCompression fills res for a successful ladder hit.
func finishCompression(res Result, img image.Image, out []byte, mime string) Result {
	b := img.Bounds()
	res.Bytes = out
	res.MIME = mime
	res.Width, res.Height = b.Dx(), b.Dy()
	res.Reencoded = true
	res.Note = "re-encoded to fit budgets"
	return res
}

// smaller keeps the smaller of two candidate encodings.
func smaller(best []byte, bestMIME string, out []byte, mime string) ([]byte, string) {
	if best == nil || len(out) < len(best) {
		return out, mime
	}
	return best, bestMIME
}

// ladderEdges returns the descending edge rungs to try, always <=
// maxEdge. The floor (512px) matches the smallest size at which vision
// models still extract useful detail.
func ladderEdges(maxEdge int) []int {
	var out []int
	for _, e := range []int{maxEdge, 1000, 768, 512} {
		if e <= maxEdge && (len(out) == 0 || e < out[len(out)-1]) {
			out = append(out, e)
		}
	}
	return out
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.PNG); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(quality)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// flattenAlpha composites images with an alpha channel over white. JPEG
// has no alpha channel; encoding an NRGBA with transparent pixels as
// JPEG would render those regions black.
func flattenAlpha(img image.Image) image.Image {
	nrgba, ok := img.(*image.NRGBA)
	if !ok || !hasTransparency(nrgba) {
		return img
	}
	b := img.Bounds()
	bg := imaging.New(b.Dx(), b.Dy(), color.White)
	return imaging.Overlay(bg, img, image.Point{}, 1.0)
}

// hasTransparency samples pixels on a coarse grid — a full scan of a
// 12 MP image just to check alpha would cost more than the encode.
func hasTransparency(img *image.NRGBA) bool {
	b := img.Bounds()
	stepX, stepY := max(b.Dx()/64, 1), max(b.Dy()/64, 1)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			if img.NRGBAAt(x, y).A < 255 {
				return true
			}
		}
	}
	return false
}

// ProcessDataURL parses a data URL, runs Process on its payload, and
// re-encodes the result as a canonical "data:<effective-mime>;base64,…"
// URL. ok is false when the URL itself could not be parsed (treat as
// downgraded). On downgrade the ORIGINAL data URL is returned unchanged.
func ProcessDataURL(dataURL string) (string, Result, bool) {
	if !strings.HasPrefix(dataURL, "data:") {
		return dataURL, Result{Decision: DecisionDowngrade, Note: "not a data url"}, false
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return dataURL, Result{Decision: DecisionDowngrade, Note: "data url missing comma"}, false
	}
	header := dataURL[5:comma]
	payload := dataURL[comma+1:]

	var mime string
	isB64 := false
	for _, part := range strings.Split(header, ";") {
		switch {
		case part == "base64":
			isB64 = true
		case part == "":
		case mime == "":
			mime = part
		}
	}
	var data []byte
	if isB64 {
		decoded, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return dataURL, Result{Decision: DecisionDowngrade, Note: "base64 decode: " + err.Error()}, false
		}
		data = decoded
	} else {
		// Tolerate urlencoded payloads, mirroring the gateway's existing
		// decodeDataURL behavior.
		decoded, err := url.QueryUnescape(payload)
		if err != nil {
			return dataURL, Result{Decision: DecisionDowngrade, Note: "urlencoded decode: " + err.Error()}, false
		}
		data = []byte(decoded)
	}

	res := Process(data, mime)
	if res.Decision != DecisionAccept {
		return dataURL, res, true
	}
	return "data:" + res.MIME + ";base64," + base64.StdEncoding.EncodeToString(res.Bytes), res, true
}
