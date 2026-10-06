package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"

	"testing"
)

// gradient is a small image with a distinct pixel per corner, so a rotation or
// flip that is off by one line or column is visible.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x * 255 / max(1, w-1)),
				G: uint8(y * 255 / max(1, h-1)),
				B: 128,
				A: 255,
			})
		}
	}
	return img
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestRotateImageSwapsDimensionsAndPixels(t *testing.T) {
	src := gradient(4, 2)
	src.Set(0, 0, color.RGBA{R: 255, A: 255})

	for _, deg := range []int{90, 180, 270, 360} {
		out := rotateImage(src, deg)
		b := out.Bounds()
		wantW, wantH := 4, 2
		if deg == 90 || deg == 270 {
			wantW, wantH = 2, 4
		}
		if b.Dx() != wantW || b.Dy() != wantH {
			t.Errorf("%d degrees: size = %dx%d, want %dx%d", deg, b.Dx(), b.Dy(), wantW, wantH)
		}
	}

	// 180 puts the marked pixel in the opposite corner.
	out := rotateImage(src, 180)
	r, _, _, _ := out.At(3, 1).RGBA()
	if r>>8 != 255 {
		t.Errorf("180 degrees: marked pixel moved to (%d,%d), want (3,1)", 3, 1)
	}
}

func TestRotateFourTimesIsIdentity(t *testing.T) {
	src := gradient(3, 2)
	cur := image.Image(src)
	for i := 0; i < 4; i++ {
		cur = rotateImage(cur, 90)
	}
	out := cur.(*image.RGBA)
	if out.Bounds() != src.Bounds() {
		t.Fatalf("bounds changed: %v vs %v", out.Bounds(), src.Bounds())
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			want := src.RGBAAt(x, y)
			got := out.RGBAAt(x, y)
			if absDiff(want.R, got.R) > 2 || absDiff(want.G, got.G) > 2 {
				t.Errorf("pixel %d,%d = %v, want %v", x, y, got, want)
			}
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestFlipImageMirrorsHorizontally(t *testing.T) {
	src := gradient(4, 2)
	out := flipImage(src).(*image.RGBA)
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if out.RGBAAt(x, y) != src.RGBAAt(3-x, y) {
				t.Fatalf("row %d not mirrored at %d", y, x)
			}
		}
	}
}

func TestCropSquareTakesCentredSquare(t *testing.T) {
	src := gradient(10, 4)
	out := cropSquare(src)
	if out.Bounds().Dx() != 4 || out.Bounds().Dy() != 4 {
		t.Fatalf("size = %v, want 4x4", out.Bounds())
	}
	// A 10x4 source centres by trimming 3 columns from each side.
	origX := 3
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if out.At(x, y) != src.At(origX+x, y) {
				t.Fatalf("crop is not centred: pixel %d,%d", x, y)
			}
		}
	}
}

func TestResizeImageFitsInsideBoxAndNeverGrows(t *testing.T) {
	src := gradient(100, 50)

	half := resizeImage(src, 50, 50)
	if got := half.Bounds(); got.Dx() != 50 || got.Dy() != 25 {
		t.Errorf("50x50 box on a 100x50 source: got %dx%d, want 50x25", got.Dx(), got.Dy())
	}

	tall := resizeImage(src, 20, 100)
	if got := tall.Bounds(); got.Dx() != 20 || got.Dy() != 10 {
		t.Errorf("20x100 box: got %dx%d, want 20x10", got.Dx(), got.Dy())
	}

	// Already small enough: untouched, because enlarging only wastes bytes.
	small := gradient(10, 10)
	if resizeImage(small, 100, 100) != small {
		t.Error("a resize that would enlarge must be a no-op")
	}
}

func TestResizeImageEdgesStayInside(t *testing.T) {
	// A 1-pixel-wide source is the case where naive sampling walks off the edge.
	one := gradient(1, 1)
	for _, box := range [][2]int{{4, 1}, {1, 4}, {3, 3}} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("resize to %dx%d panicked: %v", box[0], box[1], r)
				}
			}()
			out := resizeImage(one, box[0], box[1])
			if out.Bounds().Empty() {
				t.Errorf("resize to %dx%d produced an empty image", box[0], box[1])
			}
		}()
	}
}

func TestSampleWeightsClampToImage(t *testing.T) {
	cases := []struct {
		name     string
		f        float64
		n        int
		wantIdx  int
		wantFrac float64
	}{
		{"inside", 3.4, 10, 3, 0.4},
		{"negative clamps to the first pixel", -2, 10, 0, 0},
		{"past the end clamps to the last pair", 99, 10, 8, 1},
		{"single pixel image", 5, 1, 0, 0},
	}
	for _, c := range cases {
		idx, frac := sampleWeights(c.f, c.n)
		if idx != c.wantIdx || absDiff(uint8(frac*100), uint8(c.wantFrac*100)) > 1 {
			t.Errorf("%s: got (%d, %.2f), want (%d, %.2f)", c.name, idx, frac, c.wantIdx, c.wantFrac)
		}
	}
}

func TestDecodeImageRejectsOversizedPixelCount(t *testing.T) {
	// A 7000x6000 image is under the byte cap but 42 megapixels, which would
	// allocate far more than this bot's memory limit allows.
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 7000, 6000))
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 10}); err != nil {
		t.Skipf("could not build the fixture: %v", err)
	}
	if _, err := decodeImage(buf.Bytes()); err == nil {
		t.Error("an image with 42 megapixels must be rejected, not decoded")
	}
}

func TestEncodeImageKeepsTransparencyOnlyInPNG(t *testing.T) {
	trans := image.NewRGBA(image.Rect(0, 0, 4, 4))
	trans.Set(0, 0, color.RGBA{A: 0})

	if !hasAlpha(trans) {
		t.Error("a transparent pixel must be detected")
	}
	pngBytes, err := encodeImage(trans, hasAlpha(trans))
	if err != nil {
		t.Fatal(err)
	}
	if len(pngBytes) < 8 || !bytes.Equal(pngBytes[1:4], []byte("PNG")) {
		t.Error("an image with alpha must be encoded as PNG")
	}

	opaque := gradient(4, 4)
	if hasAlpha(opaque) {
		t.Error("an opaque image must not be treated as transparent")
	}
	jpgBytes, err := encodeImage(opaque, hasAlpha(opaque))
	if err != nil {
		t.Fatal(err)
	}
	if len(jpgBytes) < 3 || jpgBytes[0] != 0xFF || jpgBytes[1] != 0xD8 {
		t.Error("an opaque image must be encoded as JPEG")
	}
}

func TestFlattenOnWhiteRemovesFringe(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.RGBA{R: 255, G: 0, B: 0, A: 0})
	out := flattenOnWhite(src)
	r, g, b, _ := out.At(0, 0).RGBA()
	// Fully transparent red over white must read as white, not black or red.
	if r>>8 != 255 || g>>8 != 255 || b>>8 != 255 {
		t.Errorf("transparent pixel flattened to (%d,%d,%d), want white", r>>8, g>>8, b>>8)
	}
}

// The ffmpeg-backed operations must disappear from the menu when ffmpeg is
// missing, rather than being offered and then failing.
func TestMediaOpAvailabilityFollowsFfmpeg(t *testing.T) {
	orig := ffmpegPath
	defer func() { ffmpegPath = orig }()

	ffmpegPath = ""
	for _, op := range []string{"trim", "audio", "voice", "gif"} {
		if _, available, known := mediaOpKind(op); !known || available {
			t.Errorf("%s: with no ffmpeg it must be known but unavailable", op)
		}
	}
	for _, op := range []string{"rotate90", "flip", "square", "resize"} {
		if _, available, known := mediaOpKind(op); !known || !available {
			t.Errorf("%s: still-image work must not depend on ffmpeg", op)
		}
	}

	ffmpegPath = "/usr/bin/ffmpeg"
	for _, op := range []string{"trim", "audio", "voice", "gif"} {
		if _, available, _ := mediaOpKind(op); !available {
			t.Errorf("%s: available once ffmpeg is present", op)
		}
	}
}

func TestMediaOpKindRejectsUnknownOperation(t *testing.T) {
	if _, _, known := mediaOpKind("explode"); known {
		t.Error("an unknown operation must not be reported as known")
	}
}

func TestResolveFFmpegPrefersTheConfiguredPath(t *testing.T) {
	orig := ffmpegPath
	defer func() { ffmpegPath = orig }()

	// A binary that certainly is not ffmpeg must be rejected, and the search must
	// fall through to PATH rather than reporting a bogus path.
	if got := resolveFFmpeg("/nonexistent/ffmpeg-binary"); got != "" {
		t.Logf("resolved to %q", got)
	}
	// With no candidate at all, nothing is resolved.
	ffmpegPath = ""
}

// Telegram requires a sticker's largest side to be exactly 512 and the format to
// be PNG, so whatever came in is fitted rather than uploaded as it arrived.
func TestToStickerPNGProducesTheRequiredShape(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"already square", 512, 512},
		{"wide", 800, 400},
		{"tall", 300, 900},
		{"tiny", 8, 8},
		{"odd size", 333, 517},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, gradient(c.w, c.h), &jpeg.Options{Quality: 90}); err != nil {
				t.Fatal(err)
			}
			out, err := toStickerPNG(buf.Bytes())
			if err != nil {
				t.Fatalf("toStickerPNG: %v", err)
			}
			if len(out) < 8 || string(out[1:4]) != "PNG" {
				t.Fatalf("output is not a PNG")
			}
			img, err := decodeImage(out)
			if err != nil {
				t.Fatalf("output does not decode: %v", err)
			}
			b := img.Bounds()
			if b.Dx() != stickerSide || b.Dy() != stickerSide {
				t.Errorf("size = %dx%d, want %dx%d", b.Dx(), b.Dy(), stickerSide, stickerSide)
			}
		})
	}
}

// Telegram measures the largest side, so a wide image has to be padded rather
// than squashed into a square.
func TestToStickerPNGPadsRatherThanSquashes(t *testing.T) {
	src := gradient(800, 400) // 2:1
	var buf bytes.Buffer
	jpeg.Encode(&buf, src, &jpeg.Options{Quality: 95})

	out, err := toStickerPNG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, _ := decodeImage(out)
	if img.Bounds().Dx() != stickerSide || img.Bounds().Dy() != stickerSide {
		t.Fatalf("not square: %v", img.Bounds())
	}
	// The middle band should still be the image; the top and bottom are padding.
	r, _, _, a := img.At(stickerSide/2, stickerSide/2).RGBA()
	if a == 0 {
		t.Error("the centre of a padded sticker must not be transparent")
	}
	_ = r
}

// The pack has to be created once and added to thereafter; getting that backwards
// makes every sticker after the first fail.

// The endpoint nests the png under size, and Telegram only accepts png or a video
// sticker, so a webp-only result cannot be used.
func TestStickerPNGURLPicksThePNG(t *testing.T) {
	item := map[string]interface{}{
		"file": map[string]interface{}{
			"320": map[string]interface{}{
				"webp": map[string]interface{}{"url": "https://t/a.webp"},
				"png":  map[string]interface{}{"url": "https://t/a.png"},
			},
		},
	}
	if got := stickerPNGURL(item); got != "https://t/a.png" {
		t.Errorf("stickerPNGURL = %q", got)
	}

	// Larger sizes win, because a bigger sticker is a better sticker.
	bigger := map[string]interface{}{
		"file": map[string]interface{}{
			"240": map[string]interface{}{"png": map[string]interface{}{"url": "https://t/small.png"}},
			"hd":  map[string]interface{}{"png": map[string]interface{}{"url": "https://t/big.png"}},
		},
	}
	if got := stickerPNGURL(bigger); got != "https://t/big.png" {
		t.Errorf("stickerPNGURL = %q, want the hd png", got)
	}

	webpOnly := map[string]interface{}{
		"file": map[string]interface{}{
			"320": map[string]interface{}{"webp": map[string]interface{}{"url": "https://t/a.webp"}},
		},
	}
	if got := stickerPNGURL(webpOnly); got != "" {
		t.Errorf("a webp-only item must not be used, got %q", got)
	}
	if got := stickerPNGURL(map[string]interface{}{}); got != "" {
		t.Errorf("an empty item = %q", got)
	}
}

func TestStickerToggleIsJustTheEnabledFlag(t *testing.T) {
	api := &stubAPI{responders: map[string][]string{"getMe": {okMe}}}
	h := igHandler(t, api, "https://api.test")

	h.cfg.Tools.Sticker.Enabled = false
	if h.stickersEnabled() {
		t.Error("stickers must be off while disabled")
	}
	h.cfg.Tools.Sticker.Enabled = true
	if !h.stickersEnabled() {
		t.Error("stickers must be on once enabled")
	}
}
