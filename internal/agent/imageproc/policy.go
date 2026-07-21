package imageproc

import "strings"

// AcceptedMIMEs is the canonical allowlist of image MIME types that may
// be inlined as vision `image_url` content parts. Everything else is
// downgraded to a workspace-file breadcrumb.
var AcceptedMIMEs = []string{
	"image/png",
	"image/jpeg",
	"image/gif",
	"image/webp",
}

// acceptedFormat reports whether a sniffed format is in the allowlist.
func acceptedFormat(f Format) bool {
	switch f {
	case FormatPNG, FormatJPEG, FormatGIF, FormatWebP:
		return true
	}
	return false
}

// NormalizeMIME lowercases a MIME type, strips any parameters
// ("image/png; charset=x" → "image/png"), and folds the non-canonical
// "image/jpg" alias onto "image/jpeg".
func NormalizeMIME(m string) string {
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimSpace(strings.ToLower(m))
	if m == "image/jpg" {
		m = "image/jpeg"
	}
	return m
}

// Decision is the policy verdict for one uploaded image.
type Decision int

const (
	// DecisionDowngrade means the bytes must NOT be inlined as an
	// `image_url` content part. The original bytes are still stored in
	// the workspace and surface to the agent via the
	// `[Attached: /workspace/<file>]` breadcrumb.
	DecisionDowngrade Decision = iota
	// DecisionAccept means the sniffed format is in the allowlist and
	// the (possibly re-encoded) bytes are safe for vision inlining.
	DecisionAccept
)

func (d Decision) String() string {
	if d == DecisionAccept {
		return "accept"
	}
	return "downgrade"
}
