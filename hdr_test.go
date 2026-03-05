package hdr

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
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

// findChunk searches for a PNG chunk by type in raw PNG bytes and returns its data.
func findChunk(data []byte, chunkType string) ([]byte, bool) {
	// Skip 8-byte PNG signature.
	pos := 8
	for pos+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		ct := string(data[pos+4 : pos+8])
		if ct == chunkType {
			return data[pos+8 : pos+8+length], true
		}
		pos += 12 + length // length(4) + type(4) + data(length) + crc(4)
	}
	return nil, false
}

func TestEncodeHDR(t *testing.T) {
	w, h := 4, 3
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	// Set some bright pixels that should extend into HDR.
	img.SetNRGBA64(0, 0, color.NRGBA64{R: 65535, G: 65535, B: 65535, A: 65535})
	img.SetNRGBA64(1, 0, color.NRGBA64{R: 65535, A: 65535})
	img.SetNRGBA64(2, 0, color.NRGBA64{G: 32768, A: 65535})
	img.SetNRGBA64(3, 0, color.NRGBA64{B: 8192, A: 65535})
	var buf bytes.Buffer
	if err := Encode(&buf, img, 0.5); err != nil {
		t.Fatalf("Encode HDR failed: %v", err)
	}
	raw := buf.Bytes()
	// Verify cICP chunk is present with correct values.
	cicp, ok := findChunk(raw, "cICP")
	if !ok {
		t.Fatal("cICP chunk not found in HDR PNG")
	}
	if len(cicp) != 4 {
		t.Fatalf("cICP chunk length = %d, want 4", len(cicp))
	}
	if cicp[0] != 9 || cicp[1] != 16 || cicp[2] != 0 || cicp[3] != 1 {
		t.Errorf("cICP = %v, want [9 16 0 1] (BT.2020, PQ, Identity, FullRange)", cicp)
	}
	// Verify cICP appears before IDAT.
	_, hasCICP := findChunk(raw, "cICP")
	idatPos := bytes.Index(raw, []byte("IDAT"))
	cicpPos := bytes.Index(raw, []byte("cICP"))
	if !hasCICP || cicpPos > idatPos {
		t.Error("cICP chunk must appear before IDAT")
	}
	// Verify the output is still a valid PNG (stdlib can parse the structure).
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("stdlib png.Decode failed on HDR output: %v", err)
	}
}

func TestEncodeNoHDR(t *testing.T) {
	// With white=0, no cICP chunk should be present.
	img := image.NewNRGBA64(image.Rect(0, 0, 2, 2))
	img.SetNRGBA64(0, 0, color.NRGBA64{R: 65535, A: 65535})
	var buf bytes.Buffer
	if err := Encode(&buf, img, 0); err != nil {
		t.Fatalf("Encode SDR failed: %v", err)
	}
	if _, ok := findChunk(buf.Bytes(), "cICP"); ok {
		t.Error("cICP chunk should not be present when white=0")
	}
}

func TestPQOETF(t *testing.T) {
	// PQ(0) = 0
	if v := pqOETF(0); v != 0 {
		t.Errorf("pqOETF(0) = %f, want 0", v)
	}
	// PQ(1.0) should be very close to 1.0 (10000 nits)
	if v := pqOETF(1.0); math.Abs(v-1.0) > 1e-6 {
		t.Errorf("pqOETF(1.0) = %f, want ~1.0", v)
	}
	// SDR reference white (203/10000) should map to ~0.58
	sdrWhite := sdrWhiteNits / pqMaxNits
	v := pqOETF(sdrWhite)
	if math.Abs(v-0.58) > 0.01 {
		t.Errorf("pqOETF(SDR white) = %f, want ~0.58", v)
	}
}

func TestSrgbToLinear(t *testing.T) {
	// srgbToLinear(0) = 0
	if v := srgbToLinear(0); v != 0 {
		t.Errorf("srgbToLinear(0) = %f, want 0", v)
	}
	// srgbToLinear(1) = 1
	if v := srgbToLinear(1.0); math.Abs(v-1.0) > 1e-10 {
		t.Errorf("srgbToLinear(1.0) = %f, want 1.0", v)
	}
	// srgbToLinear(0.5) ≈ 0.214
	if v := srgbToLinear(0.5); math.Abs(v-0.214) > 0.001 {
		t.Errorf("srgbToLinear(0.5) = %f, want ~0.214", v)
	}
}
