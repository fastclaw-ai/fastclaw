package session

import (
	"bytes"
	"encoding/base64"
	"hash/fnv"
	"image"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	_ "image/png"
	"strings"
	"sync"

	_ "golang.org/x/image/webp"
)

// thumbMaxSide is the longest side of a session-list thumbnail. The
// sidebar draws them at ~40px, so this is plenty on a 2x screen.
const thumbMaxSide = 96

// thumbCacheMax bounds the cache; it is simply cleared when full.
const thumbCacheMax = 4096

var (
	thumbMu    sync.Mutex
	thumbCache = map[uint64]string{}
)

// listThumbnail returns the image URL to show for a session in a list.
// Chat images are usually inline data: URLs of the full picture (often
// megabytes), and session lists are polled every few seconds, so those
// are scaled down to a small JPEG here. Other URLs pass through. An
// inline image that can't be decoded (e.g. AVIF) gets no thumbnail
// rather than shipping the original.
func listThumbnail(url string) string {
	if !strings.HasPrefix(url, "data:") {
		return url
	}
	h := fnv.New64a()
	h.Write([]byte(url))
	key := h.Sum64()

	thumbMu.Lock()
	if t, ok := thumbCache[key]; ok {
		thumbMu.Unlock()
		return t
	}
	thumbMu.Unlock()

	t := makeThumbnail(url)
	thumbMu.Lock()
	if len(thumbCache) >= thumbCacheMax {
		thumbCache = map[uint64]string{}
	}
	thumbCache[key] = t
	thumbMu.Unlock()
	return t
}

func makeThumbnail(dataURL string) string {
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 || !strings.Contains(dataURL[:comma], ";base64") {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return ""
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	b := src.Bounds()
	w, hgt := b.Dx(), b.Dy()
	if w <= 0 || hgt <= 0 {
		return ""
	}
	scale := float64(thumbMaxSide) / float64(max(w, hgt))
	if scale > 1 {
		scale = 1
	}
	tw, th := max(1, int(float64(w)*scale)), max(1, int(float64(hgt)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	// Box filter: average every source pixel that falls in a target
	// pixel, so screenshots don't alias into noise.
	for y := 0; y < th; y++ {
		y0, y1 := b.Min.Y+y*hgt/th, b.Min.Y+(y+1)*hgt/th
		for x := 0; x < tw; x++ {
			x0, x1 := b.Min.X+x*w/tw, b.Min.X+(x+1)*w/tw
			var r, g, bl, a, n uint64
			for sy := y0; sy < max(y1, y0+1); sy++ {
				for sx := x0; sx < max(x1, x0+1); sx++ {
					cr, cg, cb, ca := src.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			// Composite onto white: JPEG has no alpha.
			alpha := a / n
			dst.Pix[i+0] = uint8((r/n + (0xffff - alpha)) >> 8)
			dst.Pix[i+1] = uint8((g/n + (0xffff - alpha)) >> 8)
			dst.Pix[i+2] = uint8((bl/n + (0xffff - alpha)) >> 8)
			dst.Pix[i+3] = 0xff
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
}
