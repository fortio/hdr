package hdr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestEncodeRoundTrip(t *testing.T) {
	w, h := 4, 3
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	img.SetNRGBA64(0, 0, color.NRGBA64{R: 65535, A: 65535})
	img.SetNRGBA64(1, 0, color.NRGBA64{G: 65535, A: 65535})
	img.SetNRGBA64(2, 0, color.NRGBA64{B: 65535, A: 65535})
	img.SetNRGBA64(3, 0, color.NRGBA64{R: 32768, G: 16384, B: 8192, A: 65535})
	for x := range w {
		img.SetNRGBA64(x, 1, color.NRGBA64{R: 65535, G: 65535, B: 65535, A: 65535})
	}

	var buf bytes.Buffer
	if err := Encode(&buf, img, 0); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	decoded, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("png.Decode failed: %v", err)
	}

	bounds := decoded.Bounds()
	if bounds.Dx() != w || bounds.Dy() != h {
		t.Fatalf("decoded size %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), w, h)
	}

	for y := range h {
		for x := range w {
			want := img.NRGBA64At(x, y)
			got := decoded.(*image.NRGBA64).NRGBA64At(x, y)
			if got != want {
				t.Errorf("pixel(%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
}
