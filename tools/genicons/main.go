// Command genicons draws the placeholder icons in assets/.
// Replace them with your own .ico files; this tool is only for the defaults.
//
//	go run ./tools/genicons
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/tc-hib/winres"
)

type rgb struct{ r, g, b float64 }

var (
	blue  = rgb{0.16, 0.42, 0.90}
	grey  = rgb{0.55, 0.57, 0.60}
	amber = rgb{0.93, 0.62, 0.10}
	green = rgb{0.13, 0.70, 0.35}
	white = rgb{1, 1, 1}
)

type style struct {
	bg    rgb
	badge bool
}

var pngDir = flag.String("png", "", "also write 48px PNG previews to this directory")

func main() {
	flag.Parse()
	icons := map[string]style{
		"app.ico":               {bg: blue},
		"tray-connected.ico":    {bg: blue},
		"tray-disconnected.ico": {bg: grey},
		"tray-connecting.ico":   {bg: amber},
		"tray-exitnode.ico":     {bg: blue, badge: true},
	}
	sizes := []int{256, 64, 48, 40, 32, 24, 20, 16}
	for name, st := range icons {
		var imgs []image.Image
		for _, s := range sizes {
			imgs = append(imgs, render(s, st))
		}
		if *pngDir != "" {
			f, _ := os.Create(filepath.Join(*pngDir, name+".png"))
			png.Encode(f, imgs[2])
			f.Close()
		}
		ico, err := winres.NewIconFromImages(imgs)
		if err != nil {
			log.Fatal(err)
		}
		path := filepath.Join("assets", name)
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		if err := ico.SaveICO(f); err != nil {
			log.Fatal(err)
		}
		f.Close()
		log.Printf("wrote %s", path)
	}
}

// render draws the glyph with 4x4 supersampling.
func render(size int, st style) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4
	nodes := [][2]float64{{0.50, 0.28}, {0.28, 0.68}, {0.72, 0.68}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/ss) / float64(size)
					py := (float64(y) + (float64(sy)+0.5)/ss) / float64(size)
					c, alpha := shade(px, py, st, nodes)
					r += c.r * alpha
					g += c.g * alpha
					b += c.b * alpha
					a += alpha
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(255 * r / a), G: uint8(255 * g / a), B: uint8(255 * b / a),
				A: uint8(255 * a / (ss * ss)),
			})
		}
	}
	return img
}

func shade(x, y float64, st style, nodes [][2]float64) (rgb, float64) {
	if st.badge && circle(x, y, 0.78, 0.78, 0.22) <= 0 {
		if circle(x, y, 0.78, 0.78, 0.15) <= 0 {
			if segment(x, y, 0.70, 0.86, 0.86, 0.70) <= 0.035 || segment(x, y, 0.76, 0.70, 0.86, 0.70) <= 0.035 || segment(x, y, 0.86, 0.70, 0.86, 0.80) <= 0.035 {
				return white, 1
			}
			return green, 1
		}
		return rgb{}, 0 // transparent ring around the badge
	}
	if roundRect(x, y, 0.5, 0.5, 0.47, 0.20) > 0 {
		return rgb{}, 0
	}
	for i, n := range nodes {
		if circle(x, y, n[0], n[1], 0.105) <= 0 {
			return white, 1
		}
		m := nodes[(i+1)%len(nodes)]
		if segment(x, y, n[0], n[1], m[0], m[1]) <= 0.035 {
			return white, 1
		}
	}
	return st.bg, 1
}

func circle(x, y, cx, cy, r float64) float64 { return math.Hypot(x-cx, y-cy) - r }

func roundRect(x, y, cx, cy, half, r float64) float64 {
	dx := math.Max(math.Abs(x-cx)-(half-r), 0)
	dy := math.Max(math.Abs(y-cy)-(half-r), 0)
	return math.Hypot(dx, dy) - r
}

func segment(x, y, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	t := ((x-ax)*vx + (y-ay)*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(x-(ax+t*vx), y-(ay+t*vy))
}
