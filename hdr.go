// Package hdr provides support for high dynamic range synthetic PNG image in go.
package hdr

/* Disclaimer: a lot of this code was implemented by AI under my guidance */

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"io"
	"math"
	"sync"

	"fortio.org/safecast"
)

const (
	maxUint16 = 65535
	numUint16 = maxUint16 + 1
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
				left = raw[i-bpp] //nolint:gosec // gosec not seeing the if guard here for some reason
			}
			dst[i] = raw[i] - safecast.MustConv[uint8]((int(left)+int(prior[i]))/2)
		}
	case filterPaeth:
		for i := range raw {
			var left, upperLeft byte
			if i >= bpp {
				left = raw[i-bpp] //nolint:gosec // gosec not seeing the if guard here for some reason
				upperLeft = prior[i-bpp]
			}
			dst[i] = raw[i] - paethPredictor(left, prior[i], upperLeft)
		}
	}
}

func filterRowAndSum(dst, raw, prior []byte, fType byte) int64 {
	var sum int64
	switch fType {
	case filterNone:
		copy(dst, raw)
		for _, value := range raw {
			sum += int64(signedByteAbs[value])
		}
	case filterSub:
		copy(dst[:bpp], raw[:bpp])
		for _, value := range dst[:bpp] {
			sum += int64(signedByteAbs[value])
		}
		for i := bpp; i < len(raw); i++ {
			value := raw[i] - raw[i-bpp]
			dst[i] = value
			sum += int64(signedByteAbs[value])
		}
	case filterUp:
		for i, current := range raw {
			value := current - prior[i]
			dst[i] = value
			sum += int64(signedByteAbs[value])
		}
	case filterAverage:
		for i, current := range raw {
			var left byte
			if i >= bpp {
				left = raw[i-bpp] //nolint:gosec // gosec not seeing the if guard here for some reason
			}
			value := current - byte((int(left)+int(prior[i]))>>1) //nolint:gosec // divide by 2 with right shift
			dst[i] = value
			sum += int64(signedByteAbs[value])
		}
	case filterPaeth:
		for i, current := range raw {
			var left, upperLeft byte
			if i >= bpp {
				left = raw[i-bpp] //nolint:gosec // gosec not seeing the if guard here for some reason
				upperLeft = prior[i-bpp]
			}
			value := current - paethPredictor(left, prior[i], upperLeft)
			dst[i] = value
			sum += int64(signedByteAbs[value])
		}
	}
	return sum
}

// PQ (Perceptual Quantizer, SMPTE ST 2084) constants.
const (
	pqM1         = 2610.0 / 16384.0 // 0.1593017578125
	pqM2         = 2523.0 / 32.0    // 78.84375
	pqC1         = 3424.0 / 4096.0  // 0.8359375
	pqC2         = 2413.0 / 128.0   // 18.8515625
	pqC3         = 2392.0 / 128.0   // 18.6875
	sdrWhiteNits = 203.0            // SDR reference white in nits
	pqMaxNits    = 10000.0          // PQ peak luminance in nits
)

// pqOETF applies the PQ (ST 2084) Opto-Electronic Transfer Function.
// Input: linear light normalised to [0,1] where 1.0 = 10 000 nits.
// Output: PQ code value in [0,1].
func pqOETF(y float64) float64 {
	if y <= 0 {
		return 0
	}
	ym1 := math.Pow(y, pqM1)
	return math.Pow((pqC1+pqC2*ym1)/(1+pqC3*ym1), pqM2)
}

// srgbToLinear inverts the sRGB companding for a single channel in [0,1].
func srgbToLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

var (
	pqLookupTables sync.Map
	signedByteAbs  = func() [256]uint8 {
		var table [256]uint8
		for i := range table {
			v := int8(i)
			if v < 0 {
				table[i] = uint8(-v)
			} else {
				table[i] = uint8(v)
			}
		}
		return table
	}()
)

func remapSampleToPQ(v uint16, scaleFactor float64) uint16 {
	lin := srgbToLinear(float64(v) / maxUint16)
	scaled := lin * scaleFactor
	if scaled > 1.0 {
		scaled = 1.0 // clamp to PQ peak (10 000 nits)
	}
	return uint16(math.Round(pqOETF(scaled) * maxUint16))
}

func getPQLookupTable(white float64) *[numUint16]uint16 {
	key := math.Float64bits(white)
	if cached, ok := pqLookupTables.Load(key); ok {
		return cached.(*[numUint16]uint16)
	}
	scaleFactor := (sdrWhiteNits / pqMaxNits) / srgbToLinear(white)
	table := new([numUint16]uint16)
	for i := range numUint16 {
		table[i] = remapSampleToPQ(uint16(i), scaleFactor)
	}
	actual, _ := pqLookupTables.LoadOrStore(key, table)
	return actual.(*[numUint16]uint16)
}

// remapRowToPQ converts a row of 16-bit RGBA pixels from sRGB to PQ encoding.
func remapRowToPQ(dst, src []byte, table *[numUint16]uint16) {
	for i := 0; i < len(src); i += 2 {
		// Alpha channel (every 4th uint16): pass through unchanged.
		if (i/2)%4 == 3 {
			dst[i] = src[i]
			dst[i+1] = src[i+1]
			continue
		}
		v := uint16(src[i])<<8 | uint16(src[i+1])
		out := table[v]
		dst[i] = byte(out >> 8)
		dst[i+1] = byte(out & 0xff)
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

// Encode writes img as an HDR PNG 3.0 (truecolor 16-bit per channel with alpha)
// to the provided writer.
//
// The white parameter must be in (0,1]. It is the input sRGB level that maps to
// SDR reference white (203 nits). Input values above white extend into the HDR
// range; smaller white values therefore push more of the source range above SDR
// white. For example white=0.5 means anything above 50 % input brightness will
// appear brighter than SDR white on HDR displays.
//
// white=0 is invalid for HDR output because it would require infinite scaling.
//
// For SDR output, use the standard library's image/png encoder directly.
func Encode(writer io.Writer, img *image.NRGBA64, white float64) error {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if white <= 0 || white > 1 {
		return fmt.Errorf("white parameter must be in (0,1], got %f", white)
	}
	// PNG signature
	if _, err := writer.Write(pngSignature[:]); err != nil {
		return err
	}
	// IHDR
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], safecast.MustConv[uint32](width))
	binary.BigEndian.PutUint32(ihdr[4:8], safecast.MustConv[uint32](height))
	ihdr[8] = 16 // bit depth: 16 bits per channel
	ihdr[9] = 6  // color type: truecolor with alpha (RGBA)
	if err := writeChunk(writer, "IHDR", ihdr[:]); err != nil {
		return err
	}
	// Add cICP (PNG 3.0) signaling sRGB primaries with PQ transfer.
	cicp := [4]byte{
		1,  // Color primaries: BT.709/sRGB (pixel data is not gamut-converted)
		16, // Transfer function: PQ (SMPTE ST 2084)
		0,  // Matrix coefficients: Identity
		1,  // Video full range flag
	}
	if err := writeChunk(writer, "cICP", cicp[:]); err != nil {
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
	remappedRow := make([]byte, rowBytes)
	table := getPQLookupTable(white)
	filterByte := [1]byte{}
	for y := range height {
		srcOff := y * img.Stride
		raw := img.Pix[srcOff : srcOff+rowBytes]
		remapRowToPQ(remappedRow, raw, table)
		raw = remappedRow
		// Apply all five filters and pick the one with the smallest absolute sum.
		bestFilter := byte(0)
		bestSum := int64(1<<63 - 1)
		for f := range byte(nFilter) {
			if s := filterRowAndSum(candidates[f], raw, priorRow, f); s < bestSum {
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
	if err := writeChunk(writer, "IDAT", buf.Bytes()); err != nil {
		return err
	}
	// IEND
	return writeChunk(writer, "IEND", nil)
}
