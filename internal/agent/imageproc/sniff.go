package imageproc

import "encoding/binary"

// Format is a detected image container format. Detection is by magic
// bytes only — never by the caller-declared MIME.
type Format int

const (
	FormatUnknown Format = iota
	FormatPNG
	FormatJPEG
	FormatGIF
	FormatWebP
)

// MIME returns the canonical MIME type for the format, or "" for
// FormatUnknown.
func (f Format) MIME() string {
	switch f {
	case FormatPNG:
		return "image/png"
	case FormatJPEG:
		return "image/jpeg"
	case FormatGIF:
		return "image/gif"
	case FormatWebP:
		return "image/webp"
	}
	return ""
}

func (f Format) String() string {
	switch f {
	case FormatPNG:
		return "png"
	case FormatJPEG:
		return "jpeg"
	case FormatGIF:
		return "gif"
	case FormatWebP:
		return "webp"
	}
	return "unknown"
}

// SniffImageFormat detects PNG, JPEG, GIF and WebP from magic bytes.
// It inspects at most the first 12 bytes and never decodes pixels.
func SniffImageFormat(b []byte) Format {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return FormatPNG
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return FormatJPEG
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return FormatGIF
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return FormatWebP
	}
	return FormatUnknown
}

// ImageDimensions reads pixel dimensions from the image header WITHOUT
// decoding the full image. It feeds the decompression-bomb guard, which
// must know the claimed pixel count before committing to a decode.
// ok is false when the header is truncated or malformed.
func ImageDimensions(b []byte, f Format) (w, h int, ok bool) {
	switch f {
	case FormatPNG:
		return pngDimensions(b)
	case FormatJPEG:
		return jpegDimensions(b)
	case FormatGIF:
		return gifDimensions(b)
	case FormatWebP:
		return webpDimensions(b)
	}
	return 0, 0, false
}

// pngDimensions reads IHDR: 8-byte signature, 4-byte length, "IHDR",
// then big-endian width/height.
func pngDimensions(b []byte) (w, h int, ok bool) {
	if len(b) < 24 || string(b[12:16]) != "IHDR" {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), true
}

// jpegDimensions walks JPEG marker segments looking for a Start Of Frame
// marker, which carries the frame dimensions.
func jpegDimensions(b []byte) (w, h int, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, 0, false
	}
	i := 2
	for i+1 < len(b) {
		// Markers are preceded by 0xFF fill bytes; skip them.
		if b[i] != 0xFF {
			i++
			continue
		}
		for i < len(b) && b[i] == 0xFF {
			i++
		}
		if i >= len(b) {
			break
		}
		marker := b[i]
		// Standalone markers (no length field): SOI, EOI, RSTn, TEM.
		if marker == 0xD8 || marker == 0xD9 || marker == 0x01 ||
			(marker >= 0xD0 && marker <= 0xD7) {
			i++
			continue
		}
		if i+3 >= len(b) {
			break
		}
		segLen := int(binary.BigEndian.Uint16(b[i+1 : i+3]))
		if segLen < 2 {
			break
		}
		// SOFn markers carry dimensions — except DHT (C4), JPG (C8),
		// DAC (CC) which share the Cx range.
		if marker >= 0xC0 && marker <= 0xCF &&
			marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if i+8 >= len(b) {
				return 0, 0, false
			}
			h = int(binary.BigEndian.Uint16(b[i+4 : i+6]))
			w = int(binary.BigEndian.Uint16(b[i+6 : i+8]))
			return w, h, w > 0 && h > 0
		}
		i += 1 + segLen
	}
	return 0, 0, false
}

// gifDimensions reads the Logical Screen Descriptor.
func gifDimensions(b []byte) (w, h int, ok bool) {
	if len(b) < 10 {
		return 0, 0, false
	}
	return int(binary.LittleEndian.Uint16(b[6:8])), int(binary.LittleEndian.Uint16(b[8:10])), true
}

// webpDimensions handles all three WebP container variants: lossy (VP8),
// lossless (VP8L) and extended (VP8X).
func webpDimensions(b []byte) (w, h int, ok bool) {
	if len(b) < 16 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, false
	}
	switch string(b[12:16]) {
	case "VP8 ":
		// 3-byte frame tag at 20, 0x9D012A sync at 23, then 14-bit
		// little-endian width/height.
		if len(b) < 30 || b[23] != 0x9D || b[24] != 0x01 || b[25] != 0x2A {
			return 0, 0, false
		}
		w = int(binary.LittleEndian.Uint16(b[26:28]) & 0x3FFF)
		h = int(binary.LittleEndian.Uint16(b[28:30]) & 0x3FFF)
		return w, h, w > 0 && h > 0
	case "VP8L":
		// 0x2F signature at 20, then a 32-bit little-endian word packing
		// 14-bit (width-1) and 14-bit (height-1).
		if len(b) < 25 || b[20] != 0x2F {
			return 0, 0, false
		}
		v := binary.LittleEndian.Uint32(b[21:25])
		w = int(v&0x3FFF) + 1
		h = int((v>>14)&0x3FFF) + 1
		return w, h, true
	case "VP8X":
		// Canvas (width-1) and (height-1) as 24-bit little-endian at 24.
		if len(b) < 30 {
			return 0, 0, false
		}
		w = int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h = int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, true
	}
	return 0, 0, false
}
