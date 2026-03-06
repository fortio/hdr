// Package main demonstrates the support for high dynamic range synthetic PNG image in Go
// using a nice mandelbrot set with exponential cyclic coloring in CIE LCH color space
// with derivative-based shading.
// See https://en.wikipedia.org/wiki/Plotting_algorithms_for_the_Mandelbrot_set
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"runtime/pprof"
	"time"

	"fortio.org/cli"
	"fortio.org/hdr"
	"fortio.org/log"
	"fortio.org/progressbar"
)

func main() {
	os.Exit(Main())
}

const maxAlpha = 65535

func Main() int {
	fCpuprofile := flag.String("profile-cpu", "", "write cpu profile to `file`")
	fMemprofile := flag.String("profile-mem", "", "write memory profile to `file`")
	fHueFreq := flag.Float64("hue-freq", 0.35, "hue frequency multiplier for coloring (higher means more color cycles)")
	fChroma := flag.Float64("chroma", 80, "chroma (saturation) for coloring")
	fShading := flag.Float64("shading", 70, "range for shading lightness (0 = no shading, 100 = full range from black to white)")
	fLightAngle := flag.Float64("light-angle", 45, "light angle in degrees for shading (azimuth)")
	fLightHeight := flag.Float64("light-height", 1.5, "light height for shading (elevation, higher means more light from above)")
	cli.Main()
	if *fCpuprofile != "" {
		f, err := os.Create(*fCpuprofile)
		if err != nil {
			return log.FErrf("can't open file for cpu profile: %v", err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			return log.FErrf("can't start cpu profile: %v", err)
		}
		log.Infof("Writing cpu profile to %s", *fCpuprofile)
		defer pprof.StopCPUProfile()
	}
	res := GenerateDemoImage(*fHueFreq, *fChroma, *fShading, *fLightAngle, *fLightHeight)
	if *fMemprofile != "" {
		f, errMP := os.Create(*fMemprofile)
		if errMP != nil {
			return log.FErrf("can't open file for mem profile: %v", errMP)
		}
		errMP = pprof.WriteHeapProfile(f)
		if errMP != nil {
			return log.FErrf("can't write mem profile: %v", errMP)
		}
		log.Infof("Wrote memory profile to %s", *fMemprofile)
		_ = f.Close()
	}
	return res
}

// srgbGamma applies sRGB gamma correction to a linear channel value.
func srgbGamma(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1.0/2.4) - 0.055
}

// lchToRGBA64 converts CIE LCH(ab) color to sRGB RGBA64.
// L: lightness [0,100], C: chroma [0,~130], H: hue angle in degrees [0,360).
func lchToRGBA64(l, c, h float64) color.NRGBA64 {
	// LCH to Lab
	Hrad := h * math.Pi / 180
	a := c * math.Cos(Hrad)
	b := c * math.Sin(Hrad)

	// Lab to XYZ (D65 illuminant)
	fy := (l + 16) / 116
	fx := a/500 + fy
	fz := fy - b/200

	const epsilon = 216.0 / 24389.0
	const kappa = 24389.0 / 27.0

	var xr, yr, zr float64
	if fx3 := fx * fx * fx; fx3 > epsilon {
		xr = fx3
	} else {
		xr = (116*fx - 16) / kappa
	}
	if l > kappa*epsilon {
		yr = fy * fy * fy
	} else {
		yr = l / kappa
	}
	if fz3 := fz * fz * fz; fz3 > epsilon {
		zr = fz3
	} else {
		zr = (116*fz - 16) / kappa
	}

	// Scale by D65 white point
	x := xr * 0.95047
	y := yr // * 1.0
	z := zr * 1.08883

	// XYZ to linear sRGB (Rec. 709 primaries, D65)
	rLin := srgbGamma(3.2404542*x - 1.5371385*y - 0.4985314*z)
	gLin := srgbGamma(-0.9692660*x + 1.8760108*y + 0.0415560*z)
	bLin := srgbGamma(0.0556434*x - 0.2040259*y + 1.0572252*z)

	// Clamp to [0,1] and scale to uint16
	clamp16 := func(v float64) uint16 {
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		return uint16(v * maxAlpha)
	}
	return color.NRGBA64{clamp16(rLin), clamp16(gLin), clamp16(bLin), maxAlpha}
}

// GenerateDemoImage generates a Mandelbrot set using exponential cyclic
// coloring in CIE LCH color space with derivative-based shading.
// See https://en.wikipedia.org/wiki/Plotting_algorithms_for_the_Mandelbrot_set
func GenerateDemoImage(hueFreq, chr, shading, lightAngle, lightHeight float64) int {
	// --- Configuration ---
	width := 1024
	height := 1024
	maxIterations := 2048
	scale := 0.0025 // Controls the zoom level (lower is zoomed in)
	// Center of the Mandelbrot set (approximate)
	centerX := -0.5
	centerY := 0.0
	// bailout radius² (radius = 1e6, needed for smooth coloring)
	bailoutSq := 1e12
	// Shading: light direction for normal-mapped shading via dz/dc
	lightAngle *= math.Pi / 180 // azimuth
	vx := math.Cos(lightAngle)
	vy := math.Sin(lightAngle)

	img := image.NewNRGBA64(image.Rect(0, 0, width, height))
	bar := progressbar.NewBar()
	for y := range height {
		cIm := (float64(y)-float64(height)/2)*scale + centerY
		for x := range width {
			c := complex((float64(x)-float64(width)/2)*scale+centerX, cIm)
			z := complex(0, 0)
			dz := complex(0, 0) // derivative dz/dc for shading
			iter := 0
			for iter < maxIterations {
				dz = 2*z*dz + 1 // chain rule: d(z²+c)/dc = 2z·dz/dc + 1
				z = z*z + c
				iter++
				if real(z)*real(z)+imag(z)*imag(z) > bailoutSq {
					break
				}
			}
			if iter == maxIterations {
				// Interior points stay as default (transparent black)
				continue
			}
			// Smooth (continuous) iteration count
			absZ := math.Hypot(real(z), imag(z))
			mu := float64(iter) + 1 - math.Log2(math.Log2(absZ))
			if mu < 1 {
				mu = 1
			}
			// Exponential cyclic hue: log₂ compresses iteration bands
			// so color cycles are evenly spaced on a log scale
			h := math.Mod(360*hueFreq*math.Log2(mu+1), 360)
			// Shading from orbit derivative (normal mapping)
			shade := 1.0
			if dzAbs := math.Hypot(real(dz), imag(dz)); dzAbs > 0 {
				u := z / dz
				if uAbs := math.Hypot(real(u), imag(u)); uAbs > 0 {
					u = complex(real(u)/uAbs, imag(u)/uAbs)
					shade = (real(u)*vx + imag(u)*vy + lightHeight) / (1 + lightHeight)
					if shade < 0 {
						shade = 0
					}
				}
			}
			l := 20 + shading*shade // lightness: dark → bright based on shading range
			img.SetNRGBA64(x, y, lchToRGBA64(l, chr, h))
		}
		bar.Progress(100. * float64(y) / float64(height))
	}
	bar.Progress(100)
	bar.End()
	// Save to normal png
	fname := "mandelbrot.png"
	file, err := os.Create(fname)
	if err != nil {
		return log.FErrf("can't create output file: %v", err)
	}
	now := time.Now()
	err = png.Encode(file, img)
	elapsed := time.Since(now)
	log.Infof("Encoding regular PNG took %v", elapsed)
	_ = file.Close()
	if err != nil {
		return log.FErrf("can't encode regular png: %v", err)
	}
	log.Infof("Mandelbrot set successfully saved to normal %s", fname)

	// Save to HDR png
	fname = "mandelbrot_hdr.png"
	file, err = os.Create(fname)
	if err != nil {
		return log.FErrf("can't create output file: %v", err)
	}
	// white=0.4 means input pixels above 40% brightness will appear brighter than SDR white on HDR displays
	now = time.Now()
	err = hdr.Encode(file, img, 0.4)
	elapsed = time.Since(now)
	log.Infof("Encoding HDR PNG took %v", elapsed)
	_ = file.Close()
	if err != nil {
		return log.FErrf("can't encode png: %v", err)
	}
	log.Infof("Mandelbrot set successfully saved to HDR %s", fname)
	return 0
}
