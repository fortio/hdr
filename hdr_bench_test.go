package hdr

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"
)

var benchmarkSink byte

func benchmarkImage(width, height int) *image.NRGBA64 {
	img := image.NewNRGBA64(image.Rect(0, 0, width, height))
	for y := range height {
		fy := float64(y) / float64(height-1)
		for x := range width {
			fx := float64(x) / float64(width-1)
			wave := 0.5 + 0.5*math.Sin(float64(x)*0.071+float64(y)*0.043)
			img.SetNRGBA64(x, y, color.NRGBA64{
				R: uint16(fx * maxUint16),
				G: uint16(fy * maxUint16),
				B: uint16(wave * maxUint16),
				A: maxUint16,
			})
		}
	}
	return img
}

func benchmarkEncode(b *testing.B, white float64) {
	b.Helper()
	img := benchmarkImage(512, 512)
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		buf.Reset()
		if err := Encode(&buf, img, white); err != nil {
			b.Fatal(err)
		}
	}
	if buf.Len() == 0 {
		b.Fatal("empty output")
	}
}

func benchmarkRemapOnly(b *testing.B, white float64) {
	b.Helper()
	img := benchmarkImage(512, 512)
	rowBytes := img.Rect.Dx() * bpp
	remappedRow := make([]byte, rowBytes)
	table := getPQLookupTable(white)
	b.SetBytes(int64(len(img.Pix)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for y := range img.Rect.Dy() {
			srcOff := y * img.Stride
			remapRowToPQ(remappedRow, img.Pix[srcOff:srcOff+rowBytes], table)
		}
	}
	benchmarkSink = remappedRow[0] ^ remappedRow[len(remappedRow)-1]
}

func BenchmarkEncodeHDRWhite1(b *testing.B) {
	benchmarkEncode(b, 1)
}

func BenchmarkEncodeHDRWhite04(b *testing.B) {
	benchmarkEncode(b, 0.4)
}

func BenchmarkRemapOnlyWhite1(b *testing.B) {
	benchmarkRemapOnly(b, 1)
}

func BenchmarkRemapOnlyWhite04(b *testing.B) {
	benchmarkRemapOnly(b, 0.4)
}
