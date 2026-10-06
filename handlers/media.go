package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"telegram-bot/keyboards"
	"telegram-bot/localization"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Media editing. The still-image half is done in process because Go's standard
// library covers it and it costs nothing; the video half shells out to ffmpeg and
// is only offered when ffmpeg is actually present, so a host without it sees a
// smaller menu instead of a command that fails.

// ffmpegPath is the resolved ffmpeg binary, empty when it cannot be found.
var ffmpegPath = ""

// ffmpegAvailable reports whether the video operations can be offered at all.
func ffmpegAvailable() bool { return ffmpegPath != "" }

// resolveFFmpeg looks for ffmpeg once at startup. tools.media.ffmpegPath wins over
// PATH so an operator can point at a specific build.
func resolveFFmpeg(configured string) string {
	candidates := []string{configured, "ffmpeg", "/usr/bin/ffmpeg", "/usr/local/bin/ffmpeg"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// mediaSide caps the long edge of a resized image, which is also Telegram's own
// photo limit and keeps the encoding cost bounded.
const mediaSide = 1280

// imageBounds guards against a decompression bomb: a small file that expands to
// gigabytes would otherwise exhaust the memory this bot is explicitly capping.
const imagePixels = 40e6

func decodeImage(body []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if float64(b.Dx())*float64(b.Dy()) > imagePixels {
		return nil, errors.New("image too large to process")
	}
	return img, nil
}

// encodeImage writes JPEG unless the source carried alpha, in which case PNG is
// the only format that will keep it.
func encodeImage(img image.Image, asPNG bool) ([]byte, error) {
	var buf bytes.Buffer
	if asPNG {
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// hasAlpha samples the corners, which is where transparency shows up in practice
// and is far cheaper than walking every pixel.
func hasAlpha(img image.Image) bool {
	b := img.Bounds()
	points := []image.Point{
		b.Min,
		image.Pt(b.Max.X-1, b.Min.Y),
		image.Pt(b.Min.X, b.Max.Y-1),
		image.Pt(b.Max.X/2, b.Max.Y/2),
	}
	for _, p := range points {
		if !p.In(b) {
			continue
		}
		if _, _, _, a := img.At(p.X, p.Y).RGBA(); a < 0xffff {
			return true
		}
	}
	return false
}

// rotateImage turns an image a quarter turn at a time.
func rotateImage(img image.Image, degrees int) image.Image {
	degrees = ((degrees % 360) + 360) % 360
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()

	switch degrees {
	case 90:
		out := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.Set(h-1-y, x, img.At(src.Min.X+x, src.Min.Y+y))
			}
		}
		return out
	case 180:
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.Set(w-1-x, h-1-y, img.At(src.Min.X+x, src.Min.Y+y))
			}
		}
		return out
	case 270:
		out := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.Set(y, w-1-x, img.At(src.Min.X+x, src.Min.Y+y))
			}
		}
		return out
	}
	return img
}

// flipImage mirrors horizontally, which is the mirror most phone photos need.
func flipImage(img image.Image) image.Image {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.Set(w-1-x, y, img.At(src.Min.X+x, src.Min.Y+y))
		}
	}
	return out
}

// fitSide is the target length of the shorter edge.
const fitSide = 512

// gifMaxSeconds and gifFps bound the GIF conversion, the only operation that
// holds every frame in memory at once. An unbounded clip from a 100MB download
// is enough to outgrow a small container, and Go's soft memory limit does not
// cover a subprocess.
const (
	gifMaxSeconds = 15
	gifFps        = 12
)

// ffmpegTimeout caps one encode. A long video is the reason it exists, not a
// corrupt file: without it a stuck decoder holds a download slot until the
// process is killed.
const ffmpegTimeout = 3 * time.Minute

// cropSquare takes the largest centred square and copies it out. A profile
// picture needs one, and it saves a separate crop step.
//
// The copy is deliberate: image.Image has no SubImage, and the decoders that do
// return one share their buffer, which would keep the original pixels alive.
func cropSquare(img image.Image) image.Image {
	b := img.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	offX := b.Min.X + (b.Dx()-side)/2
	offY := b.Min.Y + (b.Dy()-side)/2

	out := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			out.Set(x, y, img.At(offX+x, offY+y))
		}
	}
	return out
}

// resizeImage scales to fit inside a bounding box with bilinear sampling.
// draw.Draw's nearest-neighbour fallback is available everywhere but looks bad on
// anything that will be re-encoded to JPEG.
func resizeImage(img image.Image, maxW, maxH int) image.Image {
	src := img.Bounds()
	w, h := src.Dx(), src.Dy()
	if w <= 0 || h <= 0 || maxW <= 0 || maxH <= 0 {
		return img
	}
	scale := math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	if scale >= 1 {
		return img
	}
	dstW := int(math.Round(float64(w) * scale))
	dstH := int(math.Round(float64(h) * scale))
	if dstW < 1 {
		dstW = 1
	}
	if dstH < 1 {
		dstH = 1
	}

	out := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	sx := float64(w) / float64(dstW)
	sy := float64(h) / float64(dstH)

	for y := 0; y < dstH; y++ {
		fy := (float64(y)+0.5)*sy - 0.5
		y0, wy := sampleWeights(fy, h)
		for x := 0; x < dstW; x++ {
			fx := (float64(x)+0.5)*sx - 0.5
			x0, wx := sampleWeights(fx, w)
			c := bilinear(img, src.Min, x0, y0, wx, wy, w, h)
			out.Set(x, y, c)
		}
	}
	return out
}

// sampleWeights returns the lower pixel index and the fraction towards the next
// one, clamped to the image so the edge cannot read out of bounds.
func sampleWeights(f float64, n int) (int, float64) {
	i := int(math.Floor(f))
	if i < 0 {
		return 0, 0
	}
	if i > n-2 {
		if n < 2 {
			return 0, 0
		}
		return n - 2, 1
	}
	w := f - float64(i)
	if w < 0 {
		w = 0
	}
	if w > 1 {
		w = 1
	}
	return i, w
}

func bilinear(img image.Image, origin image.Point, x0, y0 int, wx, wy float64, w, h int) color.Color {
	clampX := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > w-1 {
			return w - 1
		}
		return v
	}
	clampY := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > h-1 {
			return h - 1
		}
		return v
	}
	x1, y1 := clampX(x0+1), clampY(y0+1)

	c00 := colorAt(img, origin.X+clampX(x0), origin.Y+clampY(y0))
	c10 := colorAt(img, origin.X+x1, origin.Y+clampY(y0))
	c01 := colorAt(img, origin.X+clampX(x0), origin.Y+y1)
	c11 := colorAt(img, origin.X+x1, origin.Y+y1)

	top := lerpColor(c00, c10, wx)
	bottom := lerpColor(c01, c11, wx)
	return lerpColor(top, bottom, wy)
}

func colorAt(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	mix := func(x, y uint8) uint8 {
		return uint8(float64(x) + (float64(y)-float64(x))*t)
	}
	return color.RGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: mix(a.A, b.A)}
}

// flattenOnWhite composites onto white, because JPEG cannot store alpha and
// encoding one directly produces black fringes.
func flattenOnWhite(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(b)
	draw.Draw(out, b, &image.Uniform{image.White}, image.Point{}, draw.Src)
	draw.Draw(out, b, img, b.Min, draw.Over)
	return out
}

// mediaOp is one edit, applied by name so the callbacks stay short.
type mediaOp struct {
	name string
	kind string // "photo" or "video"
	// ffmpeg reports whether this operation needs the binary.
	ffmpeg bool
}

var mediaOps = []mediaOp{
	{name: "rotate90", kind: "photo"},
	{name: "rotate180", kind: "photo"},
	{name: "rotate270", kind: "photo"},
	{name: "flip", kind: "photo"},
	{name: "square", kind: "photo"},
	{name: "resize", kind: "photo"},
	{name: "trim", kind: "video", ffmpeg: true},
	{name: "audio", kind: "video", ffmpeg: true},
	{name: "gif", kind: "video", ffmpeg: true},
	{name: "voice", kind: "video", ffmpeg: true},
}

// mediaOpKind reports the kind an operation belongs to, and whether it is
// available with the current ffmpeg situation.
func mediaOpKind(name string) (kind string, available bool, known bool) {
	for _, op := range mediaOps {
		if op.name != name {
			continue
		}
		if op.ffmpeg && !ffmpegAvailable() {
			return op.kind, false, true
		}
		return op.kind, true, true
	}
	return "", false, false
}

// showMediaMenu explains what /media wants.
func (h *Handler) showMediaMenu(chatID int64, lang string) {
	h.store.SetState(chatID, "awaiting_media")
	h.store.SetSessionData(chatID, make(map[string]interface{}))
	h.sendMsg(chatID, localization.Get("mediaPrompt", lang), keyboards.Back(lang))
}

// showMediaOps lists the operations available for the media just received.
func (h *Handler) showMediaOps(chatID int64, lang, kind string) {
	kb := keyboards.MediaOpsMenu(lang, kind, ffmpegAvailable())
	h.sendMsg(chatID, localization.Get("mediaChoose", lang), kb)
}

// applyMediaOp runs one image operation and sends the result.
func (h *Handler) applyMediaOp(chatID int64, lang, opName string) {
	kind, available, known := mediaOpKind(opName)
	if !known {
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		return
	}
	if !available {
		h.sendMsg(chatID, localization.Get("mediaNoFfmpeg", lang), keyboards.Back(lang))
		return
	}

	sess := h.store.GetOrCreate(chatID)
	fileID, _ := sess.Data["media_file_id"].(string)
	if fileID == "" {
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		return
	}
	body, err := h.downloadMediaFile(fileID)
	if err != nil {
		log.Printf("media: download %s: %v", opName, err)
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		return
	}

	// Video operations are all ffmpeg; the rest are still-image work.
	if kind == "video" {
		// A trim needs two numbers and there is no sensible default, so it asks for
		// them. Running it without them would just copy the whole video back.
		if opName == "trim" && !clipWindowSet(h.store.GetOrCreate(chatID).Data) {
			h.store.SetState(chatID, "awaiting_media_trim")
			h.sendMsg(chatID, localization.Get("mediaTrimPrompt", lang), keyboards.Back(lang))
			return
		}
		h.applyMediaVideo(chatID, lang, opName, body)
		return
	}

	img, err := decodeImage(body)
	if err != nil {
		log.Printf("media: decode: %v", err)
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		return
	}

	alpha := hasAlpha(img)
	var out image.Image
	switch opName {
	case "rotate90":
		out = rotateImage(img, 90)
	case "rotate180":
		out = rotateImage(img, 180)
	case "rotate270":
		out = rotateImage(img, 270)
	case "flip":
		out = flipImage(img)
	case "square":
		out = resizeImage(cropSquare(img), mediaSide, mediaSide)
	case "resize":
		out = resizeImage(img, mediaSide, mediaSide)
	}

	enc, err := encodeImage(out, alpha)
	if err != nil {
		log.Printf("media: encode: %v", err)
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		return
	}
	name := "media_" + opName + mediaExt(ctOf(body), "", "")
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: name, Bytes: enc})
	doc.Caption = localization.Get("mediaDone", lang)
	if _, err := h.bot.Send(doc); err != nil {
		log.Printf("media: send: %v", err)
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
	}
}

// applyMediaVideo shells out to ffmpeg for the operations that cannot be done in
// process.
func (h *Handler) applyMediaVideo(chatID int64, lang, opName string, body []byte) {
	if !ffmpegAvailable() {
		h.sendMsg(chatID, localization.Get("mediaNoFfmpeg", lang), keyboards.Back(lang))
		return
	}
	sess := h.store.GetOrCreate(chatID)
	from, to := mediaClipWindow(sess.Data)

	out, ext, err := runFFmpeg(ffmpegPath, body, opName, from, to)
	if err != nil {
		log.Printf("media: ffmpeg %s: %v", opName, err)
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		return
	}

	// Audio comes back as a voice note rather than a file, which is what people
	// want it for; everything else goes out as a document so it keeps its quality.
	if opName == "voice" {
		vn := tgbotapi.NewVoice(chatID, tgbotapi.FileBytes{Name: "media_voice.ogg", Bytes: out})
		if _, err := h.bot.Send(vn); err != nil {
			log.Printf("media: voice send: %v", err)
			h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
		}
		return
	}

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileBytes{Name: "media_" + opName + ext, Bytes: out})
	doc.Caption = localization.Get("mediaDone", lang)
	if _, err := h.bot.Send(doc); err != nil {
		log.Printf("media: send: %v", err)
		h.sendMsg(chatID, localization.Get("mediaError", lang), keyboards.Back(lang))
	}
}

// mediaClipWindow reads the "from,to" seconds captured for a trim.
func mediaClipWindow(data map[string]interface{}) (from, to string) {
	from, _ = data["media_from"].(string)
	to, _ = data["media_to"].(string)
	return from, to
}

// clipWindowSet reports whether a trim window has actually been captured. It
// needs both ends: -ss alone trims to the end, which is the whole file again.
func clipWindowSet(data map[string]interface{}) bool {
	from, to := mediaClipWindow(data)
	return from != "" && to != ""
}

// handleTrimWindow reads the answer to the trim prompt.
//
// Two accepted shapes: "12" for a twelve second clip from the start, and
// "12-40" for a window. Anything else is reported and the trim is dropped rather
// than silently running on a bad window.
func (h *Handler) handleTrimWindow(chatID int64, text, lang string) {
	text = strings.TrimSpace(text)
	if text == "" {
		h.store.SetState(chatID, "awaiting_media")
		h.sendMsg(chatID, localization.Get("mediaTrimBad", lang), keyboards.Back(lang))
		return
	}

	from, to, ok := strings.Cut(text, "-")
	if !ok {
		// A bare number means "this many seconds from the start".
		from, to = "0", text
	}
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if !isSeconds(from) || !isSeconds(to) {
		h.store.SetState(chatID, "awaiting_media")
		h.sendMsg(chatID, localization.Get("mediaTrimBad", lang), keyboards.Back(lang))
		return
	}

	if err := h.store.SetSessionKeys(chatID, map[string]interface{}{
		"media_from": from,
		"media_to":   to,
	}); err != nil {
		log.Printf("media: trim window: %v", err)
		h.sendMsg(chatID, localization.Get("mediaTrimBad", lang), keyboards.Back(lang))
		return
	}
	h.store.SetState(chatID, "idle")
	h.applyMediaOp(chatID, lang, "trim")
}

// isSeconds accepts a plain non-negative number, optionally with decimals.
func isSeconds(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return false
	}
	return true
}

// runFFmpeg writes the body to a temp file, runs ffmpeg, and reads the result
// back. Everything is cleaned up even on failure.
func runFFmpeg(bin string, body []byte, op, from, to string) ([]byte, string, error) {
	in, err := tempFile("in", ".mp4")
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(in)

	ext := ".mp4"
	if op == "audio" {
		ext = ".m4a"
	}
	if op == "voice" {
		ext = ".ogg"
	}
	if op == "gif" {
		ext = ".gif"
	}
	outPath, err := tempFile("out", ext)
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(outPath)

	if err := os.WriteFile(in, body, 0o600); err != nil {
		return nil, "", err
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-threads", "2"}
	if op == "trim" {
		if from != "" {
			args = append(args, "-ss", from)
		}
		if to != "" {
			args = append(args, "-to", to)
		}
		args = append(args, "-i", in, "-c", "copy", outPath)
	} else {
		args = append(args, "-i", in)
		switch op {
		case "audio":
			args = append(args, "-vn", "-c:a", "aac", "-b:a", "128k", outPath)
		case "voice":
			// 32kbps mono Opus is what a voice note has to be to be accepted.
			args = append(args, "-vn", "-ac", "1", "-ar", "48000",
				"-c:a", "libopus", "-b:a", "32k", outPath)
		case "gif":
			// Bounded on purpose. Every frame at 480px is held in memory at once,
			// so an unbounded clip is the one operation here that can outgrow the
			// host. Fifteen seconds keeps it small, and two threads stops ffmpeg
			// spawning a stack per core on a small container.
			args = append(args,
				"-t", strconv.Itoa(gifMaxSeconds),
				"-vf", "fps="+strconv.Itoa(gifFps)+
					",scale=480:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse",
				outPath)
		default:
			return nil, "", fmt.Errorf("unknown video operation %q", op)
		}
	}

	// A timeout keeps one pathological input from pinning a worker slot. Without
	// the context a wedged decoder would run until the container was killed.
	ctx, cancel := context.WithTimeout(context.Background(), ffmpegTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, "", fmt.Errorf("ffmpeg %s: timed out after %s", op, ffmpegTimeout)
		}
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, "", fmt.Errorf("ffmpeg %s: %v %s", op, err, msg)
	}
	res, err := os.ReadFile(outPath)
	if err != nil {
		return nil, "", err
	}
	if len(res) == 0 {
		return nil, "", fmt.Errorf("ffmpeg %s produced nothing", op)
	}
	return res, ext, nil
}

// tempFile returns a path in the system temp directory that does not yet exist.
func tempFile(prefix, ext string) (string, error) {
	f, err := os.CreateTemp("", prefix+"-*"+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	os.Remove(name)
	return name, nil
}

// ctOf is a small helper for the filename extension when the caller's content
// type is not otherwise available.
func ctOf(body []byte) string { return "" }

// downloadMediaFile fetches the bytes behind a Telegram file id, capped like every
// other transfer in this bot.
func (h *Handler) downloadMediaFile(fileID string) ([]byte, error) {
	f, err := h.bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return nil, err
	}
	u, err := h.bot.GetFileDirectURL(f.FilePath)
	if err != nil {
		return nil, err
	}

	resp, err := mediaClient.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return readBody(resp, maxDownloadSize)
}

// handleMediaState records the photo or video the user sent (or replied to) and
// shows the operations available for it.
func (h *Handler) handleMediaState(chat *tgbotapi.Chat, msg *tgbotapi.Message, uid int64, lang string) {
	fileID, kind := mediaOf(msg)
	if fileID == "" {
		h.sendMsg(chat.ID, localization.Get("mediaPrompt", lang), keyboards.Back(lang))
		return
	}

	sess := h.store.GetOrCreate(chatIDOf(chat))
	sess.Data["media_file_id"] = fileID
	sess.Data["media_kind"] = kind
	h.store.SetSessionData(chatIDOf(chat), sess.Data)
	h.store.SetState(uid, "idle")

	h.showMediaOps(chatIDOf(chat), lang, kind)
}

// mediaOf picks the file out of whatever the user sent. A reply is preferred,
// because that is how someone asks for a specific photo to be edited.
func mediaOf(msg *tgbotapi.Message) (fileID, kind string) {
	if msg == nil {
		return "", ""
	}
	if msg.ReplyToMessage != nil {
		if id, k := mediaOf(msg.ReplyToMessage); id != "" {
			return id, k
		}
	}
	if msg.Photo != nil && len(msg.Photo) > 0 {
		// The last size is the largest Telegram sent.
		return msg.Photo[len(msg.Photo)-1].FileID, "photo"
	}
	if msg.Document != nil && msg.Document.MimeType != "" &&
		strings.HasPrefix(msg.Document.MimeType, "image/") {
		return msg.Document.FileID, "photo"
	}
	if msg.Video != nil {
		return msg.Video.FileID, "video"
	}
	if msg.Document != nil && strings.HasPrefix(msg.Document.MimeType, "video/") {
		return msg.Document.FileID, "video"
	}
	return "", ""
}

func chatIDOf(chat *tgbotapi.Chat) int64 { return int64(chat.ID) }

// ConfigureMediaTools resolves ffmpeg once at startup so the menus can show only
// the operations that will actually work. An empty path simply means the video
// operations are not offered.
func ConfigureMediaTools(configuredFFmpeg string) {
	ffmpegPath = resolveFFmpeg(configuredFFmpeg)
	if ffmpegPath != "" {
		log.Printf("🎞 Media editing: ffmpeg found at %s, video operations enabled", ffmpegPath)
	} else {
		log.Printf("🖼 Media editing: photo operations only (no ffmpeg found)")
	}
}

// Batch downloads.
//
// Auto-detection handled exactly one link per message, so pasting five URLs did
// nothing useful: only the first was acted on. A message carrying several links
// is now downloaded in sequence with one summary at the end, rather than five
// separate success messages.

// batchLinks extracts every link in a message, in order, dropping duplicates.
func batchLinks(text string) []string {
	matches := linkRe.FindAllString(text, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		m = strings.TrimRight(m, ".,;:!?)]}'\"")
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// handleBatch downloads every recognised link in one message.
//
// The links are resolved one at a time on purpose: a burst of parallel transfers
// on a small host is what the download semaphore exists to avoid.
func (h *Handler) handleBatch(chatID, uid int64, links []string, lang string) {
	h.sendMsg(chatID, localization.Get("batchStarted", lang, len(links)), emptyKB)

	ok, failed, unsupported := 0, 0, 0
	for _, link := range links {
		hit := dlByHost(link)
		switch {
		case hit.offPath:
			unsupported++
			log.Printf("batch: %s is not a supported %s link", link, hit.d.id)
		case !hit.ok:
			failed++
		default:
			// Synchronous, so the summary cannot be written before the work is
			// done. Each one acquires the download semaphore on its own.
			h.resolveDownload(hit.d, chatID, uid, link, "", lang)
			ok++
		}
	}

	h.sendMsg(chatID, localization.Get("batchDone", lang, ok, failed, unsupported), keyboards.Back(lang))
}

// batchHasDownloads reports whether at least one link belongs to a site the bot
// can download. A message full of unrelated links should stay conversation.
func batchHasDownloads(links []string) bool {
	for _, l := range links {
		if dlByHost(l).ok {
			return true
		}
	}
	return false
}

// Stickers.
//
// Sticker search used to send its results as photos, which is not what anybody
// wants from a sticker search: a photo cannot be added to a set and does not
// animate. The bot now owns a sticker pack, uploads what it finds, and hands back
// a real sticker file id.
//
// The pack is created on first use, so there is nothing to configure beyond the
// name and title in config.json.

// stickersEnabled reports whether sticker search should return real stickers
// rather than photos. There is no pack to configure any more, because a bot
// cannot own one.
func (h *Handler) stickersEnabled() bool {
	return h.cfg.Tools.Sticker.Enabled
}

// sendSticker puts the sticker in the chat.
//
// It deliberately does not build a sticker pack. createNewStickerSet and
// addStickerToSet both take a user_id for the *owner* of the set, and Telegram
// answers USER_IS_BOT when that is a bot: a set is owned by a person, and a bot
// may only edit a set that a person already created. There is no way for a bot to
// own a pack, so this sends the sticker straight to the chat instead, which is
// what a user wants from a sticker search anyway — they long-press it and add it
// to their own collection.
func (h *Handler) sendSticker(chatID int64, lang string, png []byte) {
	if !h.stickersEnabled() {
		h.sendMsg(chatID, localization.Get("stickerPackOff", lang), keyboards.Back(lang))
		return
	}
	if len(png) < stickerMinBytes {
		h.sendMsg(chatID, localization.Get("stickerTooSmall", lang), keyboards.Back(lang))
		return
	}

	st := tgbotapi.NewSticker(chatID, tgbotapi.FileBytes{Name: "sticker.png", Bytes: png})
	if _, err := h.bot.Send(st); err != nil {
		log.Printf("sticker: send failed: %v", err)
		h.sendMsg(chatID, localization.Get("stickerUploadError", lang), keyboards.Back(lang))
		return
	}
}

// stickerMinBytes is the floor below which a file is not worth uploading.
const stickerMinBytes = 1024

// toStickerPNG converts image bytes to the 512x512 PNG a sticker must be.
//
// Telegram wants the largest side to be exactly 512 and the format to be PNG, so
// whatever came in is decoded, fitted and re-encoded here rather than uploaded as
// it arrived.
func toStickerPNG(src []byte) ([]byte, error) {
	img, err := decodeImage(src)
	if err != nil {
		return nil, err
	}

	// Telegram measures the largest side, so a wide image is padded rather than
	// squashed.
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	side := w
	if h > side {
		side = h
	}
	if side <= 0 {
		return nil, errors.New("empty image")
	}

	canvas := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, canvas.Bounds(), img, b.Min, draw.Over)

	// Stretched, not fitted: the canvas is already square, and resizeImage
	// deliberately never enlarges, which would leave a small source at its
	// original size and have Telegram reject it.
	scaled := scaleTo(canvas, stickerSide, stickerSide)
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// stickerSide is the size Telegram requires for a sticker.
const stickerSide = 512

// scaleTo resizes to exactly the given size, enlarging when it has to.
//
// resizeImage fits inside a box and refuses to enlarge, which is right for a
// download and wrong for a sticker, where Telegram insists on 512x512 exactly.
func scaleTo(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if b.Dx() == w && b.Dy() == h {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	sx := float64(b.Dx()) / float64(w)
	sy := float64(b.Dy()) / float64(h)
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*sy - 0.5
		y0, wy := sampleWeights(fy, b.Dy())
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*sx - 0.5
			x0, wx := sampleWeights(fx, b.Dx())
			out.Set(x, y, bilinear(img, b.Min, x0, y0, wx, wy, b.Dx(), b.Dy()))
		}
	}
	return out
}
