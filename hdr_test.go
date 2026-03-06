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

func remapRowToPQReference(dst, src []byte, white float64) {
	scaleFactor := (sdrWhiteNits / pqMaxNits) / srgbToLinear(white)
	for i := 0; i < len(src); i += 2 {
		if (i/2)%4 == 3 {
			dst[i] = src[i]
			dst[i+1] = src[i+1]
			continue
		}
		v := uint16(src[i])<<8 | uint16(src[i+1])
		out := remapSampleToPQ(v, scaleFactor)
		dst[i] = byte(out >> 8)
		dst[i+1] = byte(out)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	w, h := 4, 3
	img := image.NewNRGBA64(image.Rect(0, 0, w, h))
	img.SetNRGBA64(0, 0, color.NRGBA64{R: maxUint16, A: maxUint16})
	img.SetNRGBA64(1, 0, color.NRGBA64{G: maxUint16, A: maxUint16})
	img.SetNRGBA64(2, 0, color.NRGBA64{B: maxUint16, A: maxUint16})
	img.SetNRGBA64(3, 0, color.NRGBA64{R: 32768, G: 16384, B: 8192, A: maxUint16})
	for x := range w {
		img.SetNRGBA64(x, 1, color.NRGBA64{R: maxUint16, G: maxUint16, B: maxUint16, A: maxUint16})
	}
	var buf bytes.Buffer
	const white = 0.5
	if err := Encode(&buf, img, white); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	decoded, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("png.Decode failed: %v", err)
	}
	decodedNRGBA64 := decoded.(*image.NRGBA64)
	decodedBounds := decodedNRGBA64.Bounds()
	if decodedBounds.Dx() != w || decodedBounds.Dy() != h {
		t.Fatalf("decoded size %dx%d, want %dx%d", decodedBounds.Dx(), decodedBounds.Dy(), w, h)
	}
	expected := image.NewNRGBA64(image.Rect(0, 0, w, h))
	for y := range h {
		srcOff := y * img.Stride
		dstOff := y * expected.Stride
		remapRowToPQReference(expected.Pix[dstOff:dstOff+w*bpp], img.Pix[srcOff:srcOff+w*bpp], white)
		for x := range w {
			want := expected.NRGBA64At(x, y)
			got := decodedNRGBA64.NRGBA64At(x, y)
			if got != want {
				t.Errorf("pixel(%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
}

func TestRemapRowToPQLookupMatchesReference(t *testing.T) {
	for _, white := range []float64{0.4, 0.5, 1.0} {
		src := benchmarkImage(64, 8)
		got := make([]byte, len(src.Pix))
		want := make([]byte, len(src.Pix))
		remapRowToPQ(got, src.Pix, getPQLookupTable(white))
		remapRowToPQReference(want, src.Pix, white)
		if !bytes.Equal(got, want) {
			t.Fatalf("lookup remap mismatch for white=%g", white)
		}
	}
}

func TestFilterRowAndSumMatchesSeparatePass(t *testing.T) {
	raw := make([]byte, bpp*17)
	prior := make([]byte, len(raw))
	for i := range raw {
		raw[i] = byte((i*29 + 17) & 0xff)
		prior[i] = byte((i*47 + 3) & 0xff)
	}
	for _, filter := range []byte{filterNone, filterSub, filterUp, filterAverage, filterPaeth} {
		got := make([]byte, len(raw))
		want := make([]byte, len(raw))
		gotSum := filterRowAndSum(got, raw, prior, filter)
		filterRow(want, raw, prior, filter)
		wantSum := sumAbs(want)
		if !bytes.Equal(got, want) {
			t.Fatalf("filtered row mismatch for filter %d", filter)
		}
		if gotSum != wantSum {
			t.Fatalf("sum mismatch for filter %d: got %d want %d", filter, gotSum, wantSum)
		}
	}
}

func TestEncodeRoundTripNonZeroBounds(t *testing.T) {
	bounds := image.Rect(10, 20, 14, 23)
	img := image.NewNRGBA64(bounds)
	img.SetNRGBA64(10, 20, color.NRGBA64{R: maxUint16, A: maxUint16})
	img.SetNRGBA64(11, 20, color.NRGBA64{G: maxUint16, A: maxUint16})
	img.SetNRGBA64(12, 20, color.NRGBA64{B: maxUint16, A: maxUint16})
	img.SetNRGBA64(13, 20, color.NRGBA64{R: 32768, G: 16384, B: 8192, A: maxUint16})
	for x := bounds.Min.X; x < bounds.Max.X; x++ {
		img.SetNRGBA64(x, 21, color.NRGBA64{R: maxUint16, G: maxUint16, B: maxUint16, A: maxUint16})
	}
	zeroBounds := image.NewNRGBA64(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := range bounds.Dy() {
		for x := range bounds.Dx() {
			zeroBounds.SetNRGBA64(x, y, img.NRGBA64At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	var buf bytes.Buffer
	if err := Encode(&buf, img, 0.5); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	var zeroBuf bytes.Buffer
	if err := Encode(&zeroBuf, zeroBounds, 0.5); err != nil {
		t.Fatalf("Encode zero-bounds failed: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), zeroBuf.Bytes()) {
		t.Fatal("encoding differs for equivalent non-zero-bounds image")
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
	img.SetNRGBA64(0, 0, color.NRGBA64{R: maxUint16, G: maxUint16, B: maxUint16, A: maxUint16})
	img.SetNRGBA64(1, 0, color.NRGBA64{R: maxUint16, A: maxUint16})
	img.SetNRGBA64(2, 0, color.NRGBA64{G: 32768, A: maxUint16})
	img.SetNRGBA64(3, 0, color.NRGBA64{B: 8192, A: maxUint16})
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
	if cicp[0] != 1 || cicp[1] != 16 || cicp[2] != 0 || cicp[3] != 1 {
		t.Errorf("cICP = %v, want [1 16 0 1] (BT.709, PQ, Identity, FullRange)", cicp)
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

func TestEncodeRejectsNonHDRWhiteValues(t *testing.T) {
	img := image.NewNRGBA64(image.Rect(0, 0, 2, 2))
	for _, white := range []float64{-1, 0, 1.1} {
		var buf bytes.Buffer
		if err := Encode(&buf, img, white); err == nil {
			t.Fatalf("Encode(%f) succeeded, want error", white)
		}
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
