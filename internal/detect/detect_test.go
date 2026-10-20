package detect

import (
	"image"
	"math"
	"testing"

	"gocv.io/x/gocv"

	"image-splitter/internal/scantest"
)

type photo = scantest.Photo

func center(d Detection) Point {
	var c Point
	for _, p := range d.Quad {
		c.X, c.Y = c.X+p.X/4, c.Y+p.Y/4
	}
	return c
}

func detectPage(t *testing.T, photos []photo, cfg Config) (gocv.Mat, *Result) {
	t.Helper()
	page := scantest.Page(photos)
	t.Cleanup(func() { page.Close() })
	res, err := Detect(page, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stats: %+v", res.Stats)
	for _, d := range res.Detections {
		t.Logf("#%d %s angle=%.2f center=%.0f,%.0f bounds=%v fill=%.2f", d.Index, d.Method, d.Angle, center(d).X, center(d).Y, d.Bounds, d.Fill)
	}
	return page, res
}

// checkPhotos verifies count, row order, centre, skew angle and extracted size.
func checkPhotos(t *testing.T, page gocv.Mat, ds []Detection, want []photo) {
	t.Helper()
	if len(ds) != len(want) {
		t.Fatalf("got %d detections, want %d", len(ds), len(want))
	}
	for i, w := range want {
		d := ds[i]
		if d.Index != i+1 {
			t.Errorf("photo %d: index %d", i+1, d.Index)
		}
		c := center(d)
		if math.Hypot(c.X-w.CX, c.Y-w.CY) > 25 {
			t.Errorf("photo %d: centre %.0f,%.0f, want %.0f,%.0f", i+1, c.X, c.Y, w.CX, w.CY)
		}
		if math.Abs(d.Angle-w.Angle) > 1 {
			t.Errorf("photo %d: angle %.2f, want %.2f", i+1, d.Angle, w.Angle)
		}
		crop, err := Extract(page, d, 0)
		if err != nil {
			t.Fatal(err)
		}
		gotW, gotH := float64(crop.Cols()), float64(crop.Rows())
		crop.Close()
		if math.Abs(gotW-w.W)/w.W > 0.03 || math.Abs(gotH-w.H)/w.H > 0.03 {
			t.Errorf("photo %d: extracted %vx%v, want about %vx%v", i+1, gotW, gotH, w.W, w.H)
		}
	}
}

var freelyPlaced = []photo{
	{CX: 650, CY: 700, W: 900, H: 620, Angle: 0},
	{CX: 1800, CY: 760, W: 650, H: 900, Angle: 8},
	{CX: 750, CY: 2000, W: 1000, H: 700, Angle: -15},
	{CX: 1800, CY: 2050, W: 700, H: 950, Angle: 4, WhiteBorder: true},
	{CX: 1250, CY: 3050, W: 1200, H: 500, Angle: 2},
}

func TestDetectsFreelyPlacedPhotos(t *testing.T) {
	page, res := detectPage(t, freelyPlaced, DefaultConfig())
	ds := res.Detections
	checkPhotos(t, page, ds, freelyPlaced)
	if ds[0].Method != Axis {
		t.Errorf("unrotated photo: method %s, want %s (no resampling)", ds[0].Method, Axis)
	}
	for _, d := range ds[1:] {
		if d.Method != Rotated {
			t.Errorf("photo %d: method %s, want %s", d.Index, d.Method, Rotated)
		}
	}
	if !res.Stats.OtsuUsed || res.Stats.Scale >= 1 || res.Stats.Background[0] < 200 {
		t.Errorf("unexpected stats %+v", res.Stats)
	}
}

func TestSeparatesNearbyPhotos(t *testing.T) {
	photos := []photo{
		{CX: 700, CY: 800, W: 1000, H: 700},
		{CX: 1715, CY: 800, W: 1000, H: 700}, // 15 px (~1.3 mm at 300 DPI) gap
	}
	page, res := detectPage(t, photos, DefaultConfig())
	checkPhotos(t, page, res.Detections, photos)
}

func TestSplitsTouchingPhotos(t *testing.T) {
	photos := []photo{
		{CX: 700, CY: 1200, W: 1000, H: 700},
		{CX: 1690, CY: 1250, W: 900, H: 650, Angle: 6}, // overlaps the first one slightly
	}
	_, res := detectPage(t, photos, DefaultConfig())
	if len(res.Detections) != 2 || res.Stats.SplitRegions != 1 {
		t.Fatalf("got %d detections (%d split regions), want 2 (1)", len(res.Detections), res.Stats.SplitRegions)
	}

	cfg := DefaultConfig()
	cfg.SplitTouching = false
	_, res = detectPage(t, photos, cfg)
	if len(res.Detections) != 1 {
		t.Fatalf("with splitting disabled got %d detections, want 1", len(res.Detections))
	}
}

func TestCorrectsPerspective(t *testing.T) {
	trapezoid := []image.Point{{600, 800}, {1700, 800}, {1800, 1700}, {500, 1700}}
	photos := []photo{{CX: 1150, CY: 1250, W: 1300, H: 900, Corners: trapezoid}}
	page, res := detectPage(t, photos, DefaultConfig())
	if len(res.Detections) != 1 || res.Detections[0].Method != Perspective {
		t.Fatalf("want one perspective detection, got %+v", res.Detections)
	}
	crop, err := Extract(page, res.Detections[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	defer crop.Close()
	if math.Abs(float64(crop.Cols())-1300) > 30 || math.Abs(float64(crop.Rows())-900) > 30 {
		t.Errorf("extracted %dx%d, want about 1300x900", crop.Cols(), crop.Rows())
	}

	cfg := DefaultConfig()
	cfg.Perspective = false
	_, res = detectPage(t, photos, cfg)
	if len(res.Detections) != 1 || res.Detections[0].Method == Perspective {
		t.Fatalf("perspective disabled, got %+v", res.Detections)
	}
}

func TestDeskewDisabledCropsAxisAligned(t *testing.T) {
	photos := []photo{{CX: 1200, CY: 1500, W: 900, H: 700, Angle: 10}}
	cfg := DefaultConfig()
	cfg.Deskew = false
	page, res := detectPage(t, photos, cfg)
	if len(res.Detections) != 1 || res.Detections[0].Method != Axis {
		t.Fatalf("want one axis detection, got %+v", res.Detections)
	}
	crop, err := Extract(page, res.Detections[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	defer crop.Close()
	// The axis-aligned box of a 900x700 photo rotated 10 degrees.
	wantW := 900*math.Cos(math.Pi/18) + 700*math.Sin(math.Pi/18)
	if math.Abs(float64(crop.Cols())-wantW) > 0.03*wantW {
		t.Errorf("width %d, want about %.0f", crop.Cols(), wantW)
	}
}

func TestSmallRotationIsNotResampled(t *testing.T) {
	_, res := detectPage(t, []photo{{CX: 1200, CY: 1500, W: 900, H: 700, Angle: 0.15}}, DefaultConfig())
	if len(res.Detections) != 1 || res.Detections[0].Method != Axis {
		t.Fatalf("want one axis detection below MinAngle, got %+v", res.Detections)
	}
}

func TestEmptyPage(t *testing.T) {
	_, res := detectPage(t, nil, DefaultConfig())
	if len(res.Detections) != 0 || res.Stats.OtsuUsed {
		t.Fatalf("empty page: %d detections, otsu used %v", len(res.Detections), res.Stats.OtsuUsed)
	}
}

func TestRejectionReasons(t *testing.T) {
	photos := []photo{
		{CX: 400, CY: 400, W: 120, H: 100},  // below min-area
		{CX: 1200, CY: 400, W: 900, H: 120}, // big enough, but too thin
		{CX: 1200, CY: 1800, W: 1400, H: 1000},
	}
	_, res := detectPage(t, photos, DefaultConfig())
	if len(res.Detections) != 1 {
		t.Fatalf("got %d detections, want 1", len(res.Detections))
	}
	if res.Stats.Rejected["min-area"] == 0 || res.Stats.Rejected["min-size"] == 0 {
		t.Errorf("rejections %v, want min-area and min-size", res.Stats.Rejected)
	}

	cfg := DefaultConfig()
	cfg.MaxPageFraction = 0.1
	_, res = detectPage(t, photos[2:], cfg)
	if len(res.Detections) != 0 || res.Stats.Rejected["page-fraction"] != 1 {
		t.Errorf("max page fraction: %d detections, rejections %v", len(res.Detections), res.Stats.Rejected)
	}

	cfg = DefaultConfig()
	cfg.MinFill = 1.01
	_, res = detectPage(t, photos[2:], cfg)
	if len(res.Detections) != 0 || res.Stats.Rejected["not-rectangular"] != 1 {
		t.Errorf("min fill: %d detections, rejections %v", len(res.Detections), res.Stats.Rejected)
	}
}

func TestEdgesFindPaleBorders(t *testing.T) {
	// Without edges the pale white border blends into the bed and the photo
	// shrinks to its printed area.
	photos := []photo{{CX: 1200, CY: 1500, W: 800, H: 1000, Angle: 3, WhiteBorder: true}}
	page, res := detectPage(t, photos, DefaultConfig())
	checkPhotos(t, page, res.Detections, photos)

	cfg := DefaultConfig()
	cfg.Edges = false
	_, res = detectPage(t, photos, cfg)
	if len(res.Detections) == 1 && res.Detections[0].Area > 0.9*800*1000 {
		t.Errorf("without edges the white border should not be found (area %.0f)", res.Detections[0].Area)
	}
}

func TestMasksAreKept(t *testing.T) {
	page := scantest.Page([]photo{{CX: 1200, CY: 1500, W: 900, H: 700, Angle: 10}})
	defer page.Close()
	res, err := Detect(page, DefaultConfig(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for _, name := range []string{"fixed", "otsu", "edges", "combined", "final"} {
		m, ok := res.Masks[name]
		if !ok || m.Empty() || m.Channels() != 1 {
			t.Errorf("mask %q missing or invalid", name)
		}
	}
}

func TestSourceFormats(t *testing.T) {
	page8 := scantest.Page([]photo{{CX: 1200, CY: 1500, W: 900, H: 700, Angle: 10}})
	defer page8.Close()
	convert := func(f func(dst *gocv.Mat)) gocv.Mat { m := gocv.NewMat(); f(&m); return m }
	cases := map[string]gocv.Mat{
		"16-bit BGR": convert(func(d *gocv.Mat) { page8.ConvertToWithParams(d, gocv.MatTypeCV16UC3, 257, 0) }),
		"float BGR":  convert(func(d *gocv.Mat) { page8.ConvertToWithParams(d, gocv.MatTypeCV32FC3, 1.0/255, 0) }),
		"gray":       convert(func(d *gocv.Mat) { gocv.CvtColor(page8, d, gocv.ColorBGRToGray) }),
		"BGRA":       convert(func(d *gocv.Mat) { gocv.CvtColor(page8, d, gocv.ColorBGRToBGRA) }),
	}
	for name, page := range cases {
		t.Run(name, func(t *testing.T) {
			defer page.Close()
			res, err := Detect(page, DefaultConfig(), false)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Detections) != 1 {
				t.Fatalf("got %d detections, want 1", len(res.Detections))
			}
			crop, err := Extract(page, res.Detections[0], 4)
			if err != nil {
				t.Fatal(err)
			}
			defer crop.Close()
			if crop.Type() != page.Type() {
				t.Errorf("crop type %v, want the source type %v", crop.Type(), page.Type())
			}
		})
	}
}

func TestDetectErrors(t *testing.T) {
	empty := gocv.NewMat()
	defer empty.Close()
	if _, err := Detect(empty, DefaultConfig(), false); err == nil {
		t.Error("empty page: want an error")
	}
	signed := gocv.NewMatWithSize(100, 100, gocv.MatTypeCV8SC3)
	defer signed.Close()
	if _, err := Detect(signed, DefaultConfig(), false); err == nil {
		t.Error("8-bit signed page: want an unsupported depth error")
	}
}

func TestExtractPaddingAndBounds(t *testing.T) {
	page := gocv.NewMatWithSize(200, 300, gocv.MatTypeCV8UC3)
	defer page.Close()
	axis := Detection{Method: Axis, Bounds: image.Rect(10, 20, 110, 70)}
	crop, err := Extract(page, axis, 5)
	if err != nil {
		t.Fatal(err)
	}
	if crop.Cols() != 110 || crop.Rows() != 60 {
		t.Errorf("padded axis crop %dx%d, want 110x60", crop.Cols(), crop.Rows())
	}
	crop.Close()

	// Padding is clipped at the page border.
	crop, err = Extract(page, Detection{Method: Axis, Bounds: image.Rect(0, 0, 50, 50)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if crop.Cols() != 60 || crop.Rows() != 60 {
		t.Errorf("clipped crop %dx%d, want 60x60", crop.Cols(), crop.Rows())
	}
	crop.Close()

	rotated := Detection{Method: Rotated, Quad: [4]Point{{50, 50}, {150, 50}, {150, 100}, {50, 100}}}
	crop, err = Extract(page, rotated, 3)
	if err != nil {
		t.Fatal(err)
	}
	if crop.Cols() != 106 || crop.Rows() != 56 {
		t.Errorf("padded warp %dx%d, want 106x56", crop.Cols(), crop.Rows())
	}
	crop.Close()

	if _, err := Extract(page, Detection{Method: Axis, Bounds: image.Rect(500, 500, 600, 600)}, 0); err == nil {
		t.Error("detection outside the page: want an error")
	}
	if _, err := Extract(page, Detection{Method: Rotated}, 0); err == nil {
		t.Error("degenerate quad: want an error")
	}
}

func TestReviewImage(t *testing.T) {
	page, res := detectPage(t, []photo{{CX: 1200, CY: 1500, W: 900, H: 700, Angle: 10}}, DefaultConfig())
	img, err := Review(page, res.Detections, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	if max(img.Cols(), img.Rows()) != 1000 || img.Channels() != 3 {
		t.Errorf("review %dx%d with %d channels", img.Cols(), img.Rows(), img.Channels())
	}
}

func TestOrderRows(t *testing.T) {
	r := func(x, y, w, h int) Detection { return Detection{Bounds: image.Rect(x, y, x+w, y+h)} }
	ds := []Detection{
		r(1200, 120, 500, 400), // row 1, right; starts lower than its neighbour
		r(100, 100, 500, 400),  // row 1, left
		r(900, 700, 500, 400),  // row 2, right
		r(100, 760, 500, 400),  // row 2, left
	}
	orderRows(ds)
	want := []int{100, 1200, 100, 900}
	for i, d := range ds {
		if d.Bounds.Min.X != want[i] {
			t.Fatalf("order %v", ds)
		}
	}
}

func TestSuppressDuplicates(t *testing.T) {
	d := func(r image.Rectangle) Detection { return Detection{Bounds: r, Area: rectArea(r)} }
	ds := suppressDuplicates([]Detection{
		d(image.Rect(10, 10, 60, 60)),   // inside the big one: dropped
		d(image.Rect(0, 0, 100, 100)),   // kept
		d(image.Rect(95, 0, 200, 100)),  // touches the big one: kept, never merged
		d(image.Rect(300, 0, 400, 100)), // separate: kept
	})
	if len(ds) != 3 {
		t.Fatalf("kept %d detections, want 3: %v", len(ds), ds)
	}
}

func TestRobustBoxIgnoresBumps(t *testing.T) {
	var pts []image.Point
	for x := 0; x < 400; x++ { // 400x300 rectangle outline
		pts = append(pts, image.Pt(x, 0), image.Pt(x, 299))
	}
	for y := 0; y < 300; y++ {
		pts = append(pts, image.Pt(0, y), image.Pt(399, y))
	}
	for x := 200; x < 205; x++ { // a 5 px wide, 20 px tall bump on the top edge
		pts = append(pts, image.Pt(x, -20))
	}
	q := robustBox(pts, 0)
	if q[0] != (Point{0, 0}) || q[2] != (Point{400, 300}) {
		t.Errorf("box %v, want (0,0)-(400,300)", q)
	}
}

func TestOrderCornersAndAngles(t *testing.T) {
	q := orderCorners([4]Point{{10, 110}, {110, 0}, {0, 10}, {120, 100}})
	want := [4]Point{{0, 10}, {110, 0}, {120, 100}, {10, 110}}
	if q != want {
		t.Errorf("ordered %v, want %v", q, want)
	}
	if a := skewAngle(q); math.Abs(a-(-5.19)) > 0.01 {
		t.Errorf("skew %.2f, want -5.19", a)
	}
	if a := cornerAngle(Point{1, 0}, Point{0, 0}, Point{0, 1}); math.Abs(a-90) > 1e-9 {
		t.Errorf("corner angle %v, want 90", a)
	}
}
