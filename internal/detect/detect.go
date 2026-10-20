// Package detect finds individual photographs on a scanned page with OpenCV
// and extracts them with deskew and, when reliable, perspective correction.
package detect

import (
	"encoding/binary"
	"errors"
	"image"
	"math"
	"sort"

	"gocv.io/x/gocv"
)

// Config controls detection. Sizes are in page pixels (the full-resolution scan).
type Config struct {
	Threshold       float64 // Lab distance from the scanner bed counted as foreground (0-255)
	MinArea         float64 // minimum photo area
	MinWidth        float64 // minimum photo width (orientation-independent)
	MinHeight       float64 // minimum photo height (orientation-independent)
	MaxPageFraction float64 // regions covering more of the page are rejected
	MinFill         float64 // minimum region area / its rotated bounding box area
	WorkSize        int     // long side of the downscaled image used for detection
	Edges           bool    // add an edge mask (finds white-bordered photos)
	SplitTouching   bool    // try to separate photos that touch each other
	Deskew          bool    // straighten rotated photos
	Perspective     bool    // correct perspective when four reliable corners are found
	MinAngle        float64 // rotations below this (degrees) are cropped without resampling
}

func DefaultConfig() Config {
	return Config{
		Threshold:       28,
		MinArea:         40000,
		MinWidth:        150,
		MinHeight:       150,
		MaxPageFraction: 0.95,
		MinFill:         0.6,
		WorkSize:        2000,
		Edges:           true,
		SplitTouching:   true,
		Deskew:          true,
		Perspective:     true,
		MinAngle:        0.3,
	}
}

// Morphology kernels as a fraction of the working image's long side, so they
// behave the same whatever the scan resolution.
const (
	medianFrac  = 0.0025
	closeFrac   = 0.002 // small enough to keep ~1 mm gaps between photos open
	openFrac    = 0.006 // removes noise bumps on outlines; photos are far wider
	bandFrac    = 0.01  // border band sampled to estimate the scanner bed
	minEdgeFrac = 0.02  // shorter edge fragments are treated as noise
)

type Method string

const (
	Axis        Method = "axis"        // straight crop, no resampling
	Rotated     Method = "rotated"     // deskewed by rotation
	Perspective Method = "perspective" // four-corner perspective warp
)

type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Detection is one photo, in page pixel coordinates.
type Detection struct {
	Index  int             `json:"index"`
	Method Method          `json:"method"`
	Angle  float64         `json:"angle"` // skew of the top edge in degrees, clockwise positive
	Quad   [4]Point        `json:"quad"`  // corners: top-left, top-right, bottom-right, bottom-left
	Bounds image.Rectangle `json:"bounds"`
	Area   float64         `json:"area"`
	Fill   float64         `json:"fill"`
}

type Stats struct {
	Scale         float64        `json:"scale"`
	Background    [3]float64     `json:"backgroundLab"`
	OtsuThreshold float64        `json:"otsuThreshold"`
	OtsuUsed      bool           `json:"otsuUsed"`
	CloseKernel   int            `json:"closeKernel"`
	OpenKernel    int            `json:"openKernel"`
	Regions       int            `json:"regions"`
	SplitRegions  int            `json:"splitRegions"`
	Duplicates    int            `json:"duplicates"`
	Rejected      map[string]int `json:"rejected,omitempty"`
}

type Result struct {
	Detections []Detection
	Stats      Stats
	// Masks holds the intermediate segmentation masks (working resolution) when
	// requested; Close releases them.
	Masks map[string]gocv.Mat
}

func (r *Result) Close() {
	for _, m := range r.Masks {
		m.Close()
	}
}

// Detect finds photos on page. With keepMasks the intermediate masks are kept in
// the result for diagnostics.
func Detect(page gocv.Mat, cfg Config, keepMasks bool) (*Result, error) {
	if page.Empty() {
		return nil, errors.New("empty page")
	}
	work, scale, err := workingImage(page, cfg.WorkSize)
	if err != nil {
		return nil, err
	}
	defer work.Close()

	res := &Result{Stats: Stats{Scale: scale, Rejected: map[string]int{}}}
	keep := func(string, gocv.Mat) {}
	if keepMasks {
		res.Masks = map[string]gocv.Mat{}
		keep = func(name string, m gocv.Mat) { res.Masks[name] = m.Clone() }
	}

	mask, err := foreground(work, cfg, &res.Stats, keep)
	if err != nil {
		res.Close()
		return nil, err
	}
	defer mask.Close()

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxNone)
	defer contours.Close()
	minPart := cfg.MinArea * scale * scale
	var regions [][]image.Point
	for _, pts := range contours.ToPoints() {
		if cfg.SplitTouching {
			if parts := splitTouching(pts, minPart); len(parts) > 1 {
				res.Stats.SplitRegions++
				regions = append(regions, parts...)
				continue
			}
		}
		regions = append(regions, pts)
	}
	res.Stats.Regions = len(regions)

	pageArea := float64(work.Cols() * work.Rows())
	var dets []Detection
	for _, pts := range regions {
		d, reason := candidate(pts, scale, pageArea, cfg)
		if reason != "" {
			res.Stats.Rejected[reason]++
			continue
		}
		dets = append(dets, d)
	}
	n := len(dets)
	dets = suppressDuplicates(dets)
	res.Stats.Duplicates = n - len(dets)
	orderRows(dets)
	for i := range dets {
		dets[i].Index = i + 1
	}
	res.Detections = dets
	return res, nil
}

// foreground builds the photo mask on the working image by combining a fixed
// colour-distance threshold (after illumination flattening), Otsu's threshold and
// an edge map, then cleaning it with scale-aware morphology.
func foreground(work gocv.Mat, cfg Config, st *Stats, keep func(string, gocv.Mat)) (gocv.Mat, error) {
	w, h := work.Cols(), work.Rows()
	n := w * h
	long := float64(max(w, h))
	ksize := func(frac float64) int { return max(3, int(math.Round(frac*long))) | 1 }

	smooth := gocv.NewMat()
	defer smooth.Close()
	if err := gocv.MedianBlur(work, &smooth, ksize(medianFrac)); err != nil {
		return gocv.Mat{}, err
	}
	lab := gocv.NewMat()
	defer lab.Close()
	if err := gocv.CvtColor(smooth, &lab, gocv.ColorBGRToLab); err != nil {
		return gocv.Mat{}, err
	}
	px, err := lab.DataPtrUint8()
	if err != nil {
		return gocv.Mat{}, err
	}

	bed := borderMedian(px, w, h, max(2, int(bandFrac*long)))
	st.Background = bed
	T := cfg.Threshold

	// Pass 1: rough mask, used only to keep photos out of the illumination model.
	chroma := make([]float32, n)
	rough := make([]byte, n)
	for i := range n {
		c := math.Hypot(float64(px[3*i+1])-bed[1], float64(px[3*i+2])-bed[2])
		chroma[i] = float32(c)
		if math.Abs(float64(px[3*i])-bed[0]) > T || c > T {
			rough[i] = 255
		}
	}
	field, err := illumination(px, rough, w, h, bed[0], long)
	if err != nil {
		return gocv.Mat{}, err
	}

	// Pass 2: fixed threshold on the flattened lightness plus chroma.
	flat := make([]byte, n)
	fixed := make([]byte, n)
	for i := range n {
		v := float64(px[3*i]) * bed[0] / math.Max(float64(field[i]), 1)
		flat[i] = uint8(math.Min(255, math.Max(0, math.Round(v))))
		if math.Abs(v-bed[0]) > T || float64(chroma[i]) > T {
			fixed[i] = 255
		}
	}
	fixedMat := matFromBytes(h, w, fixed)
	defer fixedMat.Close()
	keep("fixed", fixedMat)

	flatMat := matFromBytes(h, w, flat)
	defer flatMat.Close()
	flatBlur := gocv.NewMat()
	defer flatBlur.Close()
	if err := gocv.GaussianBlur(flatMat, &flatBlur, image.Pt(5, 5), 0, 0, gocv.BorderReplicate); err != nil {
		return gocv.Mat{}, err
	}

	combined := fixedMat.Clone()
	defer combined.Close()

	// Otsu: only trusted when its split is clearly away from the bed, otherwise it
	// is just separating bed noise.
	otsu := gocv.NewMat()
	defer otsu.Close()
	t := float64(gocv.Threshold(flatBlur, &otsu, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu))
	st.OtsuThreshold = t
	if bed[0] > t {
		gocv.BitwiseNot(otsu, &otsu)
	}
	if math.Abs(t-bed[0]) >= T && float64(gocv.CountNonZero(otsu)) < cfg.MaxPageFraction*float64(n) {
		st.OtsuUsed = true
		keep("otsu", otsu)
		gocv.BitwiseOr(combined, otsu, &combined)
	}

	if cfg.Edges {
		edges, err := edgeMask(flatBlur, px, bed, w, h, T)
		if err != nil {
			return gocv.Mat{}, err
		}
		defer edges.Close()
		k := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
		gocv.Dilate(edges, &edges, k)
		k.Close()
		if err := dropShortEdges(&edges, int(minEdgeFrac*long)); err != nil {
			return gocv.Mat{}, err
		}
		keep("edges", edges)
		gocv.BitwiseOr(combined, edges, &combined)
	}
	keep("combined", combined)

	st.CloseKernel, st.OpenKernel = ksize(closeFrac), ksize(openFrac)
	closeK := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(st.CloseKernel, st.CloseKernel))
	defer closeK.Close()
	openK := gocv.GetStructuringElement(gocv.MorphEllipse, image.Pt(st.OpenKernel, st.OpenKernel))
	defer openK.Close()

	gocv.MorphologyEx(combined, &combined, gocv.MorphClose, closeK)
	filled := fillHoles(combined)
	gocv.MorphologyEx(filled, &filled, gocv.MorphOpen, openK)
	keep("final", filled)
	return filled, nil
}

// chromaGain amplifies a*/b* differences from the bed so faint colour boundaries
// (e.g. a pale sky against a grey bed) produce edges like lightness steps do.
const chromaGain = 3

// edgeMask runs Canny on the flattened lightness and on the amplified a*/b*
// channels; photo borders often differ from the bed only in one of them.
func edgeMask(flatBlur gocv.Mat, lab []byte, bed [3]float64, w, h int, T float64) (gocv.Mat, error) {
	edges := gocv.NewMat()
	if err := gocv.Canny(flatBlur, &edges, float32(T/4), float32(T/2)); err != nil {
		edges.Close()
		return gocv.Mat{}, err
	}
	for c := 1; c <= 2; c++ {
		ch := make([]byte, w*h)
		for i := range ch {
			ch[i] = uint8(math.Min(255, math.Max(0, 128+chromaGain*(float64(lab[3*i+c])-bed[c]))))
		}
		m := matFromBytes(h, w, ch)
		gocv.GaussianBlur(m, &m, image.Pt(5, 5), 0, 0, gocv.BorderReplicate)
		e := gocv.NewMat()
		gocv.Canny(m, &e, float32(T/4), float32(T/2))
		gocv.BitwiseOr(edges, e, &edges)
		e.Close()
		m.Close()
	}
	return edges, nil
}

// dropShortEdges removes edge fragments whose bounding box is shorter than
// minLen on both sides: bed texture and noise, which would otherwise roughen
// photo outlines.
func dropShortEdges(edges *gocv.Mat, minLen int) error {
	labels, stats, centroids := gocv.NewMat(), gocv.NewMat(), gocv.NewMat()
	defer labels.Close()
	defer stats.Close()
	defer centroids.Close()
	n := gocv.ConnectedComponentsWithStats(*edges, &labels, &stats, &centroids)
	keepLabel := make([]bool, n)
	for i := 1; i < n; i++ {
		keepLabel[i] = int(stats.GetIntAt(i, 2)) >= minLen || int(stats.GetIntAt(i, 3)) >= minLen
	}
	lbl, err := labels.DataPtrUint8() // int32 labels, native (little) endian
	if err != nil {
		return err
	}
	px, err := edges.DataPtrUint8()
	if err != nil {
		return err
	}
	for i := range px {
		if !keepLabel[binary.LittleEndian.Uint32(lbl[4*i:])] {
			px[i] = 0
		}
	}
	return nil
}

// illumination models the scanner bed brightness across the page: foreground is
// replaced by the bed value, then the lightness is median-filtered (so faint photo
// areas missed by the rough mask do not count as lighting) and smoothed, both at
// low resolution.
func illumination(lab []byte, rough []byte, w, h int, bedL, long float64) ([]float32, error) {
	roughMat := matFromBytes(h, w, rough)
	defer roughMat.Close()
	k := max(3, int(0.01*long)) | 1
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(k, k))
	defer kernel.Close()
	gocv.Dilate(roughMat, &roughMat, kernel)
	grown, err := roughMat.DataPtrUint8()
	if err != nil {
		return nil, err
	}

	l := make([]byte, w*h)
	for i := range l {
		if grown[i] != 0 {
			l[i] = uint8(bedL)
		} else {
			l[i] = lab[3*i]
		}
	}
	lMat := matFromBytes(h, w, l)
	defer lMat.Close()

	small := gocv.NewMat()
	defer small.Close()
	sw, sh := max(8, w/16), max(8, h/16)
	gocv.Resize(lMat, &small, image.Pt(sw, sh), 0, 0, gocv.InterpolationArea)
	gocv.MedianBlur(small, &small, max(3, max(sw, sh)/6)|1)
	gocv.GaussianBlur(small, &small, image.Pt(0, 0), float64(max(sw, sh))/20, 0, gocv.BorderReplicate)
	small.ConvertTo(&small, gocv.MatTypeCV32F)
	full := gocv.NewMat()
	defer full.Close()
	gocv.Resize(small, &full, image.Pt(w, h), 0, 0, gocv.InterpolationLinear)
	f, err := full.DataPtrFloat32()
	if err != nil {
		return nil, err
	}
	return append([]float32(nil), f...), nil
}

// borderMedian returns the per-channel median of a band along the image border.
func borderMedian(px []byte, w, h, band int) [3]float64 {
	var hist [3][256]int
	total := 0
	for y := range h {
		for x := range w {
			if x >= band && x < w-band && y >= band && y < h-band {
				continue
			}
			i := 3 * (y*w + x)
			hist[0][px[i]]++
			hist[1][px[i+1]]++
			hist[2][px[i+2]]++
			total++
		}
	}
	var med [3]float64
	for c := range 3 {
		acc := 0
		for v, cnt := range hist[c] {
			acc += cnt
			if 2*acc >= total {
				med[c] = float64(v)
				break
			}
		}
	}
	return med
}

func fillHoles(mask gocv.Mat) gocv.Mat {
	out := gocv.NewMatWithSizeFromScalar(gocv.Scalar{}, mask.Rows(), mask.Cols(), gocv.MatTypeCV8UC1)
	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()
	if contours.Size() > 0 {
		gocv.FillPoly(&out, contours, white)
	}
	return out
}

// matFromBytes returns a single-channel Mat owning a copy of data.
func matFromBytes(rows, cols int, data []byte) gocv.Mat {
	m, err := gocv.NewMatFromBytes(rows, cols, gocv.MatTypeCV8UC1, data)
	if err != nil {
		panic(err) // only fails on a size mismatch, a programming error
	}
	defer m.Close()
	return m.Clone()
}

// candidate validates a region (working-image points) and converts it into a
// detection in page coordinates; the reason is non-empty when it is rejected.
func candidate(pts []image.Point, scale, pageArea float64, cfg Config) (Detection, string) {
	pv := gocv.NewPointVectorFromPoints(pts)
	defer pv.Close()
	area := gocv.ContourArea(pv)
	if area > cfg.MaxPageFraction*pageArea {
		return Detection{}, "page-fraction"
	}
	if area/(scale*scale) < cfg.MinArea {
		return Detection{}, "min-area"
	}
	rr := gocv.MinAreaRect2f(pv)
	rw, rh := float64(rr.Width)/scale, float64(rr.Height)/scale
	if min(rw, rh) < min(cfg.MinWidth, cfg.MinHeight) || max(rw, rh) < max(cfg.MinWidth, cfg.MinHeight) {
		return Detection{}, "min-size"
	}
	fill := area / float64(rr.Width*rr.Height)
	if fill < cfg.MinFill {
		return Detection{}, "not-rectangular"
	}

	quad, method := orderCorners(robustBox(pts, rr.Angle)), Rotated
	if cfg.Perspective {
		if q, ok := perspectiveQuad(pv, quad); ok {
			quad, method = q, Perspective
		}
	}
	angle := skewAngle(quad)
	if !cfg.Deskew || (method == Rotated && math.Abs(angle) < cfg.MinAngle) {
		quad, method = robustBox(pts, 0), Axis
	}

	d := Detection{Method: method, Angle: angle, Area: area / (scale * scale), Fill: fill}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i, p := range quad {
		p = Point{p.X / scale, p.Y / scale}
		d.Quad[i] = p
		minX, minY = math.Min(minX, p.X), math.Min(minY, p.Y)
		maxX, maxY = math.Max(maxX, p.X), math.Max(maxY, p.Y)
	}
	d.Bounds = image.Rect(int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY)))
	return d, ""
}

// perspectiveQuad accepts the region's 4-corner polygon approximation when it
// matches the region's hull and its corners are measurably, but not wildly, off a
// right angle. Otherwise plain rotation (box) is the cleaner transform.
func perspectiveQuad(pv gocv.PointVector, box [4]Point) ([4]Point, bool) {
	hullMat := gocv.NewMat()
	defer hullMat.Close()
	if err := gocv.ConvexHull(pv, &hullMat, true, true); err != nil {
		return box, false
	}
	hull := gocv.NewPointVectorFromMat(hullMat)
	defer hull.Close()
	approx := gocv.ApproxPolyDP(hull, 0.02*gocv.ArcLength(hull, true), true)
	defer approx.Close()
	if approx.Size() != 4 {
		return box, false
	}
	hullArea, quadArea := gocv.ContourArea(hull), gocv.ContourArea(approx)
	if hullArea == 0 || math.Abs(quadArea-hullArea)/hullArea > 0.04 {
		return box, false
	}
	var q [4]Point
	for i, p := range approx.ToPoints() {
		q[i] = Point{float64(p.X), float64(p.Y)}
	}
	q = orderCorners(q)
	skew := 0.0
	for i := range 4 {
		skew = math.Max(skew, math.Abs(cornerAngle(q[(i+3)%4], q[i], q[(i+1)%4])-90))
	}
	// Up to minPerspectiveSkew is outline noise on a plain rectangle; beyond
	// maxPerspectiveSkew the shape is not a photo seen in perspective.
	return q, skew > minPerspectiveSkew && skew < maxPerspectiveSkew
}

const (
	minPerspectiveSkew = 2.5 // degrees away from a right angle
	maxPerspectiveSkew = 20
)

// boxTrim is the fraction of outline points ignored at each end of each axis
// when fitting a rectangle, so small bumps do not enlarge the crop.
const boxTrim = 0.02

// robustBox fits a rectangle rotated by angle degrees to a dense outline. Its
// sides sit at the boxTrim/1-boxTrim quantiles of the points projected on the
// rectangle's axes: a real edge contributes many points at the same offset, a
// bump only a few. (GoCV's RotatedRect2f.Points is not reliable, so the corners
// are computed here.)
func robustBox(pts []image.Point, angle float64) [4]Point {
	s, c := math.Sincos(angle * math.Pi / 180)
	us, vs := make([]float64, len(pts)), make([]float64, len(pts))
	for i, p := range pts {
		x, y := float64(p.X), float64(p.Y)
		us[i], vs[i] = x*c+y*s, -x*s+y*c
	}
	sort.Float64s(us)
	sort.Float64s(vs)
	q := func(a []float64, f float64) float64 { return a[int(f*float64(len(a)-1))] }
	// +1: outline points are pixel centres on the region's edge pixels.
	u0, u1, v0, v1 := q(us, boxTrim), q(us, 1-boxTrim)+1, q(vs, boxTrim), q(vs, 1-boxTrim)+1
	corner := func(u, v float64) Point { return Point{u*c - v*s, u*s + v*c} }
	return [4]Point{corner(u0, v0), corner(u1, v0), corner(u1, v1), corner(u0, v1)}
}

// orderCorners returns corners clockwise starting at the top-left one.
func orderCorners(p [4]Point) [4]Point {
	var cx, cy float64
	for _, v := range p {
		cx, cy = cx+v.X/4, cy+v.Y/4
	}
	s := p[:]
	sort.Slice(s, func(i, j int) bool {
		return math.Atan2(s[i].Y-cy, s[i].X-cx) < math.Atan2(s[j].Y-cy, s[j].X-cx)
	})
	start := 0
	for i := range 4 {
		if s[i].X+s[i].Y < s[start].X+s[start].Y {
			start = i
		}
	}
	var out [4]Point
	for i := range 4 {
		out[i] = s[(start+i)%4]
	}
	return out
}

func skewAngle(q [4]Point) float64 {
	return math.Atan2(q[1].Y-q[0].Y, q[1].X-q[0].X) * 180 / math.Pi
}

func cornerAngle(a, b, c Point) float64 {
	v1x, v1y, v2x, v2y := a.X-b.X, a.Y-b.Y, c.X-b.X, c.Y-b.Y
	cos := (v1x*v2x + v1y*v2y) / (math.Hypot(v1x, v1y) * math.Hypot(v2x, v2y))
	return math.Acos(math.Max(-1, math.Min(1, cos))) * 180 / math.Pi
}

func dist(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

// suppressDuplicates drops a detection whose bounds lie mostly inside a larger
// one. Nearby photos are never merged.
func suppressDuplicates(ds []Detection) []Detection {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Area > ds[j].Area })
	var kept []Detection
next:
	for _, d := range ds {
		for _, k := range kept {
			inter := d.Bounds.Intersect(k.Bounds)
			if !inter.Empty() && rectArea(inter) > 0.8*rectArea(d.Bounds) {
				continue next
			}
		}
		kept = append(kept, d)
	}
	return kept
}

func rectArea(r image.Rectangle) float64 { return float64(r.Dx()) * float64(r.Dy()) }

// orderRows sorts detections top to bottom, then left to right: a photo joins the
// current row when it overlaps the row vertically by at least half its height.
func orderRows(ds []Detection) {
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Bounds.Min.Y < ds[j].Bounds.Min.Y })
	var rows [][]Detection
	var top, bottom int
	for _, d := range ds {
		if len(rows) > 0 {
			overlap := min(bottom, d.Bounds.Max.Y) - max(top, d.Bounds.Min.Y)
			if float64(overlap) >= 0.5*float64(min(d.Bounds.Dy(), bottom-top)) {
				rows[len(rows)-1] = append(rows[len(rows)-1], d)
				top, bottom = min(top, d.Bounds.Min.Y), max(bottom, d.Bounds.Max.Y)
				continue
			}
		}
		rows = append(rows, []Detection{d})
		top, bottom = d.Bounds.Min.Y, d.Bounds.Max.Y
	}
	i := 0
	for _, row := range rows {
		sort.SliceStable(row, func(a, b int) bool { return row[a].Bounds.Min.X < row[b].Bounds.Min.X })
		i += copy(ds[i:], row)
	}
}
