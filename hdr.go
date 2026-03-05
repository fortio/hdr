// Package hdr provides support for high dynamic range synthetic PNG image in go.
package hdr

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"io"

	"fortio.org/safecast"
)

// pngSignature is the 8-byte magic number at the start of every PNG file.
var pngSignature = [8]byte{137, 80, 78, 71, 13, 10, 26, 10}

// writeChunk writes a PNG chunk (length, type, data, CRC32) to w.
func writeChunk(w io.Writer, chunkType string, data []byte) error {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], safecast.MustConv[uint32](len(data)))
	if _, err := w.Write(tmp[:]); err != nil {
		return err
	}
	ct := []byte(chunkType)
	if _, err := w.Write(ct); err != nil {
		return err
	}
	crc := crc32.NewIEEE()
	crc.Write(ct)
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			return err
		}
		crc.Write(data)
	}
	binary.BigEndian.PutUint32(tmp[:], crc.Sum32())
	_, err := w.Write(tmp[:])
	return err
}

// PNG filter types.
const (
	filterNone    = 0
	filterSub     = 1
	filterUp      = 2
	filterAverage = 3
	filterPaeth   = 4
	nFilter       = 5
	bpp           = 8 // bytes per pixel: 4 channels × 2 bytes (16-bit RGBA)
)

func paethPredictor(a, b, c byte) byte {
	// Per PNG spec: a = left, b = above, c = upper-left.
	ia, ib, ic := int(a), int(b), int(c)
	p := ia + ib - ic
	pa := p - ia
	if pa < 0 {
		pa = -pa
	}
	pb := p - ib
	if pb < 0 {
		pb = -pb
	}
	pc := p - ic
	if pc < 0 {
		pc = -pc
	}
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

// filterRow applies PNG filter fType to raw (current row) with prior (previous row)
// and writes the result into dst. All slices must have length rowBytes.
func filterRow(dst, raw, prior []byte, fType byte) {
	switch fType {
	case filterNone:
		copy(dst, raw)
	case filterSub:
		copy(dst[:bpp], raw[:bpp])
		for i := bpp; i < len(raw); i++ {
			dst[i] = raw[i] - raw[i-bpp]
		}
	case filterUp:
		for i, v := range raw {
			dst[i] = v - prior[i]
		}
	case filterAverage:
		for i := range raw {
			var left byte
			if i >= bpp {
				left = raw[i-bpp]
			}
			dst[i] = raw[i] - safecast.MustConv[uint8]((int(left)+int(prior[i]))/2)
		}
	case filterPaeth:
		for i := range raw {
			var left, upperLeft byte
			if i >= bpp {
				left = raw[i-bpp]
				upperLeft = prior[i-bpp]
			}
			dst[i] = raw[i] - paethPredictor(left, prior[i], upperLeft)
		}
	}
}

// sumAbs returns the sum of absolute values of signed interpretation of bytes,
// used as a heuristic to pick the best filter per row.
func sumAbs(data []byte) int64 {
	var s int64
	for _, b := range data {
		v := int8(b)
		if v < 0 {
			s -= int64(v)
		} else {
			s += int64(v)
		}
	}
	return s
}

// Encode writes img as a PNG (truecolor 16-bit per channel with alpha) to w.
// Adaptive row filtering (None/Sub/Up/Average/Paeth) is used to minimize file size.
// The third parameter is reserved for future use and currently ignored.
func Encode(w io.Writer, img *image.NRGBA64, _ float64) error {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// PNG signature
	if _, err := w.Write(pngSignature[:]); err != nil {
		return err
	}

	// IHDR
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], safecast.MustConv[uint32](width))
	binary.BigEndian.PutUint32(ihdr[4:8], safecast.MustConv[uint32](height))
	ihdr[8] = 16 // bit depth: 16 bits per channel
	ihdr[9] = 6  // color type: truecolor with alpha (RGBA)
	if err := writeChunk(w, "IHDR", ihdr[:]); err != nil {
		return err
	}

	// IDAT: adaptively filtered image data wrapped in a zlib stream.
	// image.NRGBA64.Pix is laid out as [R_hi R_lo G_hi G_lo B_hi B_lo A_hi A_lo ...] per pixel,
	// which matches the PNG byte order.
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
	if err != nil {
		return err
	}
	rowBytes := width * bpp
	// Allocate candidate filtered rows for each filter type.
	var candidates [nFilter][]byte
	for i := range nFilter {
		candidates[i] = make([]byte, rowBytes)
	}
	priorRow := make([]byte, rowBytes) // zeros for first row (no row above)
	filterByte := [1]byte{}
	for y := range height {
		srcOff := y * img.Stride
		raw := img.Pix[srcOff : srcOff+rowBytes]
		// Apply all five filters and pick the one with the smallest absolute sum.
		bestFilter := byte(0)
		bestSum := int64(1<<63 - 1)
		for f := range byte(nFilter) {
			filterRow(candidates[f], raw, priorRow, f)
			if s := sumAbs(candidates[f]); s < bestSum {
				bestSum = s
				bestFilter = f
			}
		}
		filterByte[0] = bestFilter
		if _, err := zw.Write(filterByte[:]); err != nil {
			return err
		}
		if _, err := zw.Write(candidates[bestFilter]); err != nil {
			return err
		}
		copy(priorRow, raw)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := writeChunk(w, "IDAT", buf.Bytes()); err != nil {
		return err
	}

	// IEND
	return writeChunk(w, "IEND", nil)
}
