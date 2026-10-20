package detect

import (
	"image"
	"image/color"
	"math"

	"gocv.io/x/gocv"
)

var white = color.RGBA{255, 255, 255, 255}

// splitTouching separates a region that looks like several photos touching each
// other. A distance transform of the region is thresholded at increasing levels
// until it breaks into two or more substantial cores; each core is grown back by
// the same distance and clipped to the region. Returns nil when no split is found.
func splitTouching(pts []image.Point, minPart float64) [][]image.Point {
	pv := gocv.NewPointVectorFromPoints(pts)
	defer pv.Close()
	area := gocv.ContourArea(pv)
	rr := gocv.MinAreaRect2f(pv)
	if area < 2*minPart || area/float64(rr.Width*rr.Height) > 0.9 {
		return nil // too small for two photos, or already rectangular
	}

	b := gocv.BoundingRect(pv).Inset(-2)
	local := make([]image.Point, len(pts))
	for i, p := range pts {
		local[i] = p.Sub(b.Min)
	}
	blob := gocv.NewMatWithSizeFromScalar(gocv.Scalar{}, b.Dy(), b.Dx(), gocv.MatTypeCV8UC1)
	defer blob.Close()
	lv := gocv.NewPointsVectorFromPoints([][]image.Point{local})
	defer lv.Close()
	gocv.FillPoly(&blob, lv, white)

	dt, labels := gocv.NewMat(), gocv.NewMat()
	defer dt.Close()
	defer labels.Close()
	gocv.DistanceTransform(blob, &dt, &labels, gocv.DistL2, gocv.DistanceMask5, gocv.DistanceLabelCComp)
	_, maxD, _, _ := gocv.MinMaxLoc(dt)

	for _, frac := range []float64{0.3, 0.45, 0.6, 0.75} {
		level := frac * float64(maxD)
		cores := gocv.NewMat()
		gocv.Threshold(dt, &cores, float32(level), 255, gocv.ThresholdBinary)
		cores.ConvertTo(&cores, gocv.MatTypeCV8UC1)
		cc := gocv.FindContours(cores, gocv.RetrievalExternal, gocv.ChainApproxSimple)
		cores.Close()

		var big [][]image.Point
		for _, c := range cc.ToPoints() {
			cv := gocv.NewPointVectorFromPoints(c)
			if gocv.ContourArea(cv) >= 0.05*area {
				big = append(big, c)
			}
			cv.Close()
		}
		cc.Close()
		if len(big) < 2 {
			continue
		}

		r := int(math.Ceil(level)) + 1
		kernel := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(2*r+1, 2*r+1))
		var parts [][]image.Point
		for _, c := range big {
			if part := growBack(blob, c, kernel); part != nil {
				for i := range part {
					part[i] = part[i].Add(b.Min)
				}
				parts = append(parts, part)
			}
		}
		kernel.Close()
		if len(parts) >= 2 {
			return parts
		}
	}
	return nil
}

// growBack dilates one core and clips it to the original region, returning the
// largest resulting contour.
func growBack(blob gocv.Mat, core []image.Point, kernel gocv.Mat) []image.Point {
	m := gocv.NewMatWithSizeFromScalar(gocv.Scalar{}, blob.Rows(), blob.Cols(), gocv.MatTypeCV8UC1)
	defer m.Close()
	cv := gocv.NewPointsVectorFromPoints([][]image.Point{core})
	defer cv.Close()
	gocv.FillPoly(&m, cv, white)
	gocv.Dilate(m, &m, kernel)
	gocv.BitwiseAnd(m, blob, &m)

	contours := gocv.FindContours(m, gocv.RetrievalExternal, gocv.ChainApproxNone)
	defer contours.Close()
	var best []image.Point
	bestArea := 0.0
	for i := range contours.Size() {
		if a := gocv.ContourArea(contours.At(i)); a > bestArea {
			best, bestArea = contours.At(i).ToPoints(), a
		}
	}
	return best
}
