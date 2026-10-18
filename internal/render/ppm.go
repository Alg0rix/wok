package render

import (
	"bufio"
	"bytes"
	"fmt"
	"image/png"
	"io"
	"math"
	"net/url"
	"strconv"
)

func ppmSize(q url.Values) (int, int, error) {
	w, h := 0, 0
	for name, dst := range map[string]*int{"w": &w, "h": &h} {
		if raw := q.Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 4096 {
				return 0, 0, fmt.Errorf("%s must be 1..4096", name)
			}
			*dst = n
		}
	}
	if w == 0 && h == 0 {
		w = 800
	}
	return w, h, nil
}
func writePPM(out io.Writer, data []byte, maxW, maxH int) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	scale := 1.0
	if maxW > 0 {
		scale = float64(maxW) / float64(width)
	}
	if maxH > 0 {
		hs := float64(maxH) / float64(height)
		if maxW == 0 || hs < scale {
			scale = hs
		}
	}
	w, h := max(1, int(math.Round(float64(width)*scale))), max(1, int(math.Round(float64(height)*scale)))
	if w*h > 16*1024*1024 {
		return fmt.Errorf("image too large")
	}
	b := bufio.NewWriter(out)
	if _, err := fmt.Fprintf(b, "P6\n%d %d\n255\n", w, h); err != nil {
		return err
	}
	row := make([]byte, w*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bb, _ := img.At(bounds.Min.X+x*width/w, bounds.Min.Y+y*height/h).RGBA()
			row[x*3] = byte(r >> 8)
			row[x*3+1] = byte(g >> 8)
			row[x*3+2] = byte(bb >> 8)
		}
		if _, err := b.Write(row); err != nil {
			return err
		}
	}
	return b.Flush()
}
