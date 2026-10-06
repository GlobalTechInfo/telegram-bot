package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// The sticker PNG has to satisfy Telegram before sendSticker is even reached:
// 512 on exactly one side, neither side over, PNG, under 512KB.
func TestToStickerPNGMeetsTelegramRules(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 300, 100))
	for x := 0; x < 300; x++ {
		for y := 0; y < 100; y++ {
			src.Set(x, y, color.RGBA{200, 30, 30, 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, src); err != nil {
		t.Fatal(err)
	}

	out, err := toStickerPNG(raw.Bytes())
	if err != nil {
		t.Fatalf("toStickerPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output is not a valid PNG: %v", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w != 512 && h != 512 {
		t.Errorf("exactly one side must be 512, got %dx%d", w, h)
	}
	if w > 512 || h > 512 {
		t.Errorf("no side may exceed 512, got %dx%d", w, h)
	}
	if len(out) > 512*1024 {
		t.Errorf("PNG is %d bytes, over the 512KB limit", len(out))
	}
}
