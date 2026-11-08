package board

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	imgpng "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/llm"
)

// noise is a w×h image of pseudo-random pixels: it compresses badly, so even
// a small one is a big file.
func noise(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	x := uint32(1)
	for i := range img.Pix {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		img.Pix[i] = byte(x)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

func encodeImage(t *testing.T, format string, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = imgpng.Encode(&b, img)
	case "jpeg":
		err = jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	case "gif":
		err = gif.Encode(&b, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// checkSmall fails unless data is a decodable picture of type mime, within
// previewMax bytes and previewSide pixels a side.
func checkSmall(t *testing.T, what, mime string, data []byte) {
	t.Helper()
	if len(data) > previewMax {
		t.Errorf("%s: %d bytes, want ≤ %d", what, len(data), previewMax)
	}
	c, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if "image/"+format != mime {
		t.Errorf("%s: says %s, is %s", what, mime, format)
	}
	if c.Width > previewSide || c.Height > previewSide || c.Width < 1 || c.Height < 1 {
		t.Errorf("%s: %d×%d, want within %d", what, c.Width, c.Height, previewSide)
	}
}

func TestPreviewOfASmallFileIsTheFile(t *testing.T) {
	small := map[string][]byte{"image/png": encode(t, "png"), "image/jpeg": encode(t, "jpeg"), "image/gif": encode(t, "gif"), "image/webp": webp(300, 200)}
	for mime, data := range small {
		got, out, err := preview(mime, data)
		if err != nil || got != mime || !bytes.Equal(out, data) {
			t.Errorf("%s: %s %d bytes, %v; want the file unchanged", mime, got, len(out), err)
		}
	}
}

func TestPreviewOfALargeImageIsSmall(t *testing.T) {
	// A large noisy PNG: its 160 px PNG is still over 32 KB, so JPEG.
	big := encodeImage(t, "png", noise(1200, 900))
	if len(big) <= previewMax {
		t.Fatalf("the test image is only %d bytes", len(big))
	}
	mime, out, err := preview("image/png", big)
	if err != nil {
		t.Fatal(err)
	}
	checkSmall(t, "noisy PNG", mime, out)
	if c, _, _ := image.DecodeConfig(bytes.NewReader(out)); c.Width != 160 || c.Height != 120 {
		t.Errorf("noisy PNG: %d×%d, want 160×120 (the aspect kept)", c.Width, c.Height)
	}
	// A large plain PNG shrinks to a PNG.
	plain := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	for y := 0; y < 2000; y++ {
		for x := 0; x < 3000; x++ {
			plain.Set(x, y, color.RGBA{uint8(x / 12), uint8(y / 8), uint8((x ^ y) & 7), 255})
		}
	}
	data := encodeImage(t, "png", plain)
	if len(data) <= previewMax {
		t.Fatalf("the plain image is only %d bytes", len(data))
	}
	mime, out, err = preview("image/png", data)
	if err != nil || mime != "image/png" {
		t.Fatalf("plain PNG: %s, %v; want a PNG", mime, err)
	}
	checkSmall(t, "plain PNG", mime, out)
	// JPEG and GIF too; a tall one keeps its aspect.
	for _, format := range []string{"jpeg", "gif"} {
		data := encodeImage(t, format, noise(300, 900))
		if len(data) <= previewMax {
			t.Fatalf("%s is only %d bytes", format, len(data))
		}
		mime, out, err := preview("image/"+format, data)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		checkSmall(t, format, mime, out)
		if c, _, _ := image.DecodeConfig(bytes.NewReader(out)); c.Height != 160 || c.Width != 53 {
			t.Errorf("%s: %d×%d, want 53×160", format, c.Width, c.Height)
		}
	}
}

func TestNoPreview(t *testing.T) {
	cases := map[string]struct {
		mime string
		data []byte
	}{
		"a large WebP":         {"image/webp", append(webp(3000, 2000), make([]byte, 40<<10)...)},
		"over 8000 px wide":    {"image/png", encodeImage(t, "png", noise(8001, 4))},
		"over 8000 px tall":    {"image/png", encodeImage(t, "png", noise(4, 8001))},
		"a broken PNG":         {"image/png", append(encodeImage(t, "png", noise(400, 300))[:200], make([]byte, 40<<10)...)},
		"a large unknown type": {"image/bmp", make([]byte, 40<<10)},
		"a broken JPEG":        {"image/jpeg", append([]byte("\xff\xd8\xff"), make([]byte, 40<<10)...)},
	}
	for name, c := range cases {
		if len(c.data) <= previewMax {
			t.Fatalf("%s: only %d bytes", name, len(c.data))
		}
		if mime, out, err := preview(c.mime, c.data); !errors.Is(err, errNoPreview) {
			t.Errorf("%s: %s %d bytes, %v; want no preview", name, mime, len(out), err)
		}
	}
}

// The approval card's call: a large staged PNG comes back small, a large
// WebP as "no preview", and the call still adds the whole file.
func TestStagedImageIsAPreview(t *testing.T) {
	tf := newToolFixture(t)
	big := encodeImage(t, "png", noise(1200, 900))
	files := map[string][]byte{"big.png": big, "big.webp": append(webp(3000, 2000), make([]byte, 40<<10)...)}
	for name, data := range files {
		os.WriteFile(filepath.Join(tf.root, name), data, 0o600)
	}
	stage := func(id, path string) {
		raw, _ := json.Marshal(map[string]any{"board": tf.board.ID, "path": path})
		call, err := tf.tools.Prepare(ws, tf.agent, llm.ToolCall{ID: id, Name: "board_add_image", Input: raw})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !strings.Contains(call.Preview, path+" (") {
			t.Errorf("%s: the preview text lost its name, type and size: %s", path, call.Preview)
		}
	}
	stage("p", "big.png")
	stage("w", "big.webp")
	p, err := tf.svc.StagedImage(ws, 7, "p")
	if err != nil || p.NoPreview {
		t.Fatalf("big PNG: %+v %v", p.NoPreview, err)
	}
	checkSmall(t, "staged PNG", p.MimeType, p.Data)
	w, err := tf.svc.StagedImage(ws, 7, "w")
	if err != nil || !w.NoPreview || len(w.Data) != 0 || w.CallID != "w" {
		t.Errorf("big WebP: %+v %v, want no preview", w, err)
	}
	// The call adds the whole file, not the preview.
	st, err := tf.svc.stagedImage(ws, 7, "p")
	if err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(tf.svc.path(ws, st.Hash)); !bytes.Equal(kept, big) {
		t.Errorf("the staged file is %d bytes, want the original %d", len(kept), len(big))
	}
}
