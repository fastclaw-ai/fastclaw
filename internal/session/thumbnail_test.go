package session

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestListThumbnailShrinksInlineImages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for y := 0; y < 900; y++ {
		for x := 0; x < 1600; x++ {
			src.Set(x, y, color.RGBA{uint8(x * y), uint8(x ^ y), uint8(x + y*7), 255}) // busy, like a screenshot
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())

	thumb := listThumbnail(url)
	if !strings.HasPrefix(thumb, "data:image/jpeg;base64,") || len(thumb) >= len(url)/10 {
		t.Fatalf("thumbnail = %d bytes from %d", len(thumb), len(url))
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(thumb, "data:image/jpeg;base64,"))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != thumbMaxSide || cfg.Height != 54 {
		t.Fatalf("thumbnail size = %dx%d (%v)", cfg.Width, cfg.Height, err)
	}
	if again := listThumbnail(url); again != thumb {
		t.Fatal("second call should come from the cache")
	}
}

func TestListThumbnailKeepsLinksAndDropsUndecodable(t *testing.T) {
	if got := listThumbnail("/api/files/a.png"); got != "/api/files/a.png" {
		t.Fatalf("link changed: %q", got)
	}
	if got := listThumbnail("data:image/avif;base64,AAAAHGZ0eXA="); got != "" {
		t.Fatalf("undecodable inline image should give no thumbnail, got %q", got)
	}
}
