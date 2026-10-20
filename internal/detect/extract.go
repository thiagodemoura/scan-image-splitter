package detect

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"

	"gocv.io/x/gocv"
)

// Extract cuts a detection out of the full-resolution page, keeping its bit depth
// and channels. Axis detections are copied without resampling; rotated and
// perspective ones are warped to an upright rectangle. padding adds page pixels
// around the photo.
func Extract(page gocv.Mat, d Detection, padding int) (gocv.Mat, error) {
	if d.Method == Axis {
		r := d.Bounds.Inset(-padding).Intersect(image.Rect(0, 0, page.Cols(), page.Rows()))
		if r.Empty() {
			return gocv.Mat{}, errors.New("detection outside the page")
		}
		roi := page.Region(r)
		defer roi.Close()
		return roi.Clone(), nil
	}

	q, p := d.Quad, float64(padding)
	w := math.Max(dist(q[0], q[1]), dist(q[3], q[2]))
	h := math.Max(dist(q[0], q[3]), dist(q[1], q[2]))
	src := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{pf(q[0]), pf(q[1]), pf(q[2]), pf(q[3])})
	defer src.Close()
	dst := gocv.NewPoint2fVectorFromPoints([]gocv.Point2f{
		{X: float32(p), Y: float32(p)}, {X: float32(p + w), Y: float32(p)},
		{X: float32(p + w), Y: float32(p + h)}, {X: float32(p), Y: float32(p + h)},
	})
	defer dst.Close()
	m := gocv.GetPerspectiveTransform2f(src, dst)
	defer m.Close()

	size := image.Pt(int(math.Round(w+2*p)), int(math.Round(h+2*p)))
	if size.X <= 0 || size.Y <= 0 {
		return gocv.Mat{}, fmt.Errorf("invalid crop size %v", size)
	}
	out := gocv.NewMat()
	if err := gocv.WarpPerspectiveWithParams(page, &out, m, size, gocv.InterpolationCubic, gocv.BorderReplicate, color.RGBA{}); err != nil {
		out.Close()
		return gocv.Mat{}, err
	}
	return out, nil
}

func pf(p Point) gocv.Point2f { return gocv.Point2f{X: float32(p.X), Y: float32(p.Y)} }

// workingImage returns an 8-bit BGR copy of page whose long side is at most size,
// and the scale factor from page to working coordinates.
func workingImage(page gocv.Mat, size int) (gocv.Mat, float64, error) {
	out := gocv.NewMat()
	scale := 1.0
	if long := max(page.Cols(), page.Rows()); size > 0 && long > size {
		scale = float64(size) / float64(long)
		sz := image.Pt(max(1, int(math.Round(float64(page.Cols())*scale))), max(1, int(math.Round(float64(page.Rows())*scale))))
		if err := gocv.Resize(page, &out, sz, 0, 0, gocv.InterpolationArea); err != nil {
			out.Close()
			return gocv.Mat{}, 0, err
		}
		scale = float64(out.Cols()) / float64(page.Cols())
	} else {
		page.CopyTo(&out)
	}

	depth := int(out.Type()) & 7
	eightBit := gocv.MatType(int(out.Type()) &^ 7)
	switch depth {
	case int(gocv.MatTypeCV8U):
	case int(gocv.MatTypeCV16U):
		out.ConvertToWithParams(&out, eightBit, 1.0/257, 0)
	case int(gocv.MatTypeCV32F):
		out.ConvertToWithParams(&out, eightBit, 255, 0)
	default:
		out.Close()
		return gocv.Mat{}, 0, fmt.Errorf("unsupported image depth %d", depth)
	}

	switch out.Channels() {
	case 1:
		gocv.CvtColor(out, &out, gocv.ColorGrayToBGR)
	case 4:
		gocv.CvtColor(out, &out, gocv.ColorBGRAToBGR)
	}
	return out, scale, nil
}
