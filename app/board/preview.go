package board

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	imgpng "image/png"
)

// The approval card shows a staged image small: never the whole file, which
// may be up to maxImage. A file of previewMax or less goes as it is; a larger
// PNG, JPEG or GIF is decoded (if its header says previewPixels or less on
// each side), shrunk to fit previewSide and sent as PNG, or as JPEG when the
// PNG is still too big. WebP has no decoder in the standard library, so it,
// and anything that fails to decode, gets no preview: the card shows the
// name, type and size as text.
const (
	previewMax    = 32 << 10
	previewSide   = 160
	previewPixels = 8000
)

var errNoPreview = errors.New("no preview")

// StagedPreview is what an approval card shows for a staged image: the
// file's name, type and size, and a small picture (MimeType, Data), or
// NoPreview when one cannot be made.
type StagedPreview struct {
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	FileType  string `json:"fileType"`
	Size      int64  `json:"size"`
	MimeType  string `json:"mimeType,omitempty"`
	Data      []byte `json:"data,omitempty"`
	NoPreview bool   `json:"noPreview,omitempty"`
}

// preview makes the card's picture of an image of type mime, or errNoPreview.
func preview(mime string, data []byte) (string, []byte, error) {
	if len(data) <= previewMax {
		return mime, data, nil
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif":
	default:
		return "", nil, errNoPreview
	}
	c, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || c.Width <= 0 || c.Height <= 0 || c.Width > previewPixels || c.Height > previewPixels {
		return "", nil, errNoPreview
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", nil, errNoPreview
	}
	small := shrink(img, previewSide)
	var b bytes.Buffer
	if imgpng.Encode(&b, small) == nil && b.Len() <= previewMax {
		return "image/png", b.Bytes(), nil
	}
	// JPEG has no alpha: flatten onto white first.
	flat := image.NewRGBA(small.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), small, small.Bounds().Min, draw.Over)
	b.Reset()
	if jpeg.Encode(&b, flat, &jpeg.Options{Quality: 80}) == nil && b.Len() <= previewMax {
		return "image/jpeg", b.Bytes(), nil
	}
	return "", nil, errNoPreview
}

// shrink scales img down to fit side×side (never up), averaging up to 4×4
// samples per pixel, so a big image costs at most side²×16 reads.
func shrink(img image.Image, side int) *image.RGBA {
	src := img.Bounds()
	sw, sh := src.Dx(), src.Dy()
	w, h := sw, sh
	if w > side || h > side {
		if sw >= sh {
			w, h = side, max(1, sh*side/sw)
		} else {
			w, h = max(1, sw*side/sh), side
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	n := 4 // samples per axis within each output pixel's box
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a uint64
			for j := 0; j < n; j++ {
				sy := src.Min.Y + ((2*y+1)*n+2*j-n+1)*sh/(2*h*n) // the centre of sample j in row y's box
				for i := 0; i < n; i++ {
					sx := src.Min.X + ((2*x+1)*n+2*i-n+1)*sw/(2*w*n)
					cr, cg, cb, ca := img.At(min(max(sx, src.Min.X), src.Max.X-1), min(max(sy, src.Min.Y), src.Max.Y-1)).RGBA()
					r, g, b, a = r+uint64(cr), g+uint64(cg), b+uint64(cb), a+uint64(ca)
				}
			}
			k := uint64(n * n * 257)
			out.SetRGBA(x, y, color.RGBA{uint8(r / k), uint8(g / k), uint8(b / k), uint8(a / k)})
		}
	}
	return out
}
