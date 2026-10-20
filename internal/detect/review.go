package detect

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gocv.io/x/gocv"
)

var methodColors = map[Method]color.RGBA{
	Axis:        {0, 170, 0, 255},   // green
	Rotated:     {0, 120, 255, 255}, // blue
	Perspective: {255, 140, 0, 255}, // orange
}

// Review draws the numbered detections on a downscaled 8-bit copy of the page.
func Review(page gocv.Mat, ds []Detection, maxSize int) (gocv.Mat, error) {
	img, scale, err := workingImage(page, maxSize)
	if err != nil {
		return gocv.Mat{}, err
	}
	long := float64(max(img.Cols(), img.Rows()))
	thick := max(2, int(long/600))
	fontScale := math.Max(0.6, long/1400)

	for _, d := range ds {
		pts := make([]image.Point, 4)
		for i, p := range d.Quad {
			pts[i] = image.Pt(int(math.Round(p.X*scale)), int(math.Round(p.Y*scale)))
		}
		pv := gocv.NewPointsVectorFromPoints([][]image.Point{pts})
		c := methodColors[d.Method]
		gocv.Polylines(&img, pv, true, c, thick)
		pv.Close()

		label := fmt.Sprint(d.Index)
		sz := gocv.GetTextSize(label, gocv.FontHersheySimplex, fontScale, thick)
		org := pts[0].Add(image.Pt(thick*3, thick*3+sz.Y))
		bg := image.Rect(org.X-thick*2, org.Y-sz.Y-thick*2, org.X+sz.X+thick*2, org.Y+thick*3)
		gocv.Rectangle(&img, bg, c, -1)
		gocv.PutTextWithParams(&img, label, org, gocv.FontHersheySimplex, fontScale, white, thick, gocv.LineAA, false)
	}
	return img, nil
}
