// Package scantest generates synthetic scanned pages and scan files for tests.
package scantest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"os"

	"gocv.io/x/gocv"
)

// A4 at 300 DPI.
const PageW, PageH = 2480, 3508

// Photo describes a photo lying on the scanner bed, in page pixels.
type Photo struct {
	CX, CY, W, H, Angle float64
	WhiteBorder         bool
	Corners             []image.Point // explicit corners, e.g. for perspective
}

// Quad returns the photo's corners (clockwise from top-left), inset by inset px.
func (p Photo) Quad(inset float64) []image.Point {
	if p.Corners != nil {
		return p.Corners
	}
	s, c := math.Sincos(p.Angle * math.Pi / 180)
	hw, hh := p.W/2-inset, p.H/2-inset
	var pts []image.Point
	for _, d := range [][2]float64{{-hw, -hh}, {hw, -hh}, {hw, hh}, {-hw, hh}} {
		pts = append(pts, image.Pt(int(math.Round(p.CX+d[0]*c-d[1]*s)), int(math.Round(p.CY+d[0]*s+d[1]*c))))
	}
	return pts
}

// Page renders an 8-bit BGR scan: a light bed with uneven illumination and noise,
// and photos with some internal structure, including a pale band along the top
// edge that is hard to tell from the bed.
func Page(photos []Photo) gocv.Mat {
	rng := rand.New(rand.NewPCG(1, 2))
	data := make([]byte, PageW*PageH*3)
	for y := range PageH {
		for x := range PageW {
			shade := 238 - 22*float64(y)/PageH - 8*math.Abs(float64(x)-PageW/2)/(PageW/2)
			for c := range 3 {
				data[3*(y*PageW+x)+c] = uint8(math.Max(0, math.Min(255, shade+rng.NormFloat64()*2)))
			}
		}
	}
	m, err := gocv.NewMatFromBytes(PageH, PageW, gocv.MatTypeCV8UC3, data)
	if err != nil {
		panic(err)
	}
	page := m.Clone()
	m.Close()

	for i, p := range photos {
		inset := 0.0
		if p.WhiteBorder {
			outer := p.Quad(0)
			fill(&page, outer, color.RGBA{250, 250, 248, 255})
			pv := gocv.NewPointsVectorFromPoints([][]image.Point{outer})
			gocv.Polylines(&page, pv, true, color.RGBA{185, 185, 185, 255}, 2) // edge shadow
			pv.Close()
			inset = 45
		}
		q := p.Quad(inset)
		fill(&page, q, color.RGBA{uint8(60 + 30*i), 80, uint8(120 - 15*i), 255})
		top := []image.Point{q[0], q[1], lerp(q[1], q[2], 0.25), lerp(q[0], q[3], 0.25)}
		fill(&page, top, color.RGBA{205, 215, 228, 255})
		gocv.Circle(&page, lerp(q[0], q[2], 0.5), int(min(p.W, p.H)/6), color.RGBA{30, 30, 30, 255}, -1)
	}
	gocv.GaussianBlur(page, &page, image.Pt(3, 3), 0, 0, gocv.BorderReplicate)
	return page
}

func fill(m *gocv.Mat, pts []image.Point, c color.RGBA) {
	pv := gocv.NewPointsVectorFromPoints([][]image.Point{pts})
	defer pv.Close()
	gocv.FillPoly(m, pv, c)
}

func lerp(a, b image.Point, f float64) image.Point {
	return image.Pt(a.X+int(f*float64(b.X-a.X)), a.Y+int(f*float64(b.Y-a.Y)))
}

// WriteTIFF writes 8-bit pages (gray or BGR) as an uncompressed multi-page TIFF
// recording dpi.
func WriteTIFF(path string, pages []gocv.Mat, dpi int) error {
	var buf bytes.Buffer
	le := binary.LittleEndian
	buf.WriteString("II*\x00\x00\x00\x00\x00")
	next := 4 // offset of the pointer to patch with the next IFD
	for _, p := range pages {
		w, h, ch := p.Cols(), p.Rows(), p.Channels()
		pixels := p.ToBytes()
		if ch == 3 { // BGR -> RGB
			pixels = bytes.Clone(pixels)
			for i := 0; i+2 < len(pixels); i += 3 {
				pixels[i], pixels[i+2] = pixels[i+2], pixels[i]
			}
		}
		dataOff := buf.Len()
		buf.Write(pixels)
		resOff := buf.Len()
		binary.Write(&buf, le, []uint32{uint32(dpi), 1})
		bpsOff := buf.Len()
		binary.Write(&buf, le, []uint16{8, 8, 8})
		ifd := buf.Len()
		le.PutUint32(buf.Bytes()[next:], uint32(ifd))

		type entry struct {
			Tag, Type uint16
			Count     uint32
			Value     uint32
		}
		bps, photometric := entry{258, 3, 1, 8}, uint32(1)
		if ch == 3 {
			bps, photometric = entry{258, 3, 3, uint32(bpsOff)}, 2
		}
		entries := []entry{
			{256, 4, 1, uint32(w)}, {257, 4, 1, uint32(h)}, bps, {259, 3, 1, 1},
			{262, 3, 1, photometric}, {273, 4, 1, uint32(dataOff)}, {277, 3, 1, uint32(ch)},
			{278, 4, 1, uint32(h)}, {279, 4, 1, uint32(len(pixels))},
			{282, 5, 1, uint32(resOff)}, {283, 5, 1, uint32(resOff)}, {296, 3, 1, 2},
		}
		binary.Write(&buf, le, uint16(len(entries)))
		binary.Write(&buf, le, entries)
		next = buf.Len()
		binary.Write(&buf, le, uint32(0))
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// WriteJPEG writes m as a JPEG file.
func WriteJPEG(path string, m gocv.Mat) error {
	b, err := JPEGBytes(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// JPEGBytes encodes m as JPEG.
func JPEGBytes(m gocv.Mat) ([]byte, error) {
	buf, err := gocv.IMEncodeWithParams(gocv.JPEGFileExt, m, []int{gocv.IMWriteJpegQuality, 95})
	if err != nil {
		return nil, err
	}
	defer buf.Close()
	return bytes.Clone(buf.GetBytes()), nil
}

// WritePDF writes a PDF with one page per JPEG image, each sized so that
// rendering at dpi reproduces the image's pixel size.
func WritePDF(path string, jpegs [][]byte, sizes []image.Point, dpi int) error {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string, stream []byte) int {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s", len(offsets), body)
		if stream != nil {
			b.WriteString("\nstream\n")
			b.Write(stream)
			b.WriteString("\nendstream")
		}
		b.WriteString("\nendobj\n")
		return len(offsets)
	}
	n := len(jpegs)
	b.WriteString("%PDF-1.4\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>", nil)
	kids := ""
	for i := range n {
		kids += fmt.Sprintf("%d 0 R ", 3+3*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n), nil)
	for i, jpg := range jpegs {
		w := float64(sizes[i].X) * 72 / float64(dpi)
		h := float64(sizes[i].Y) * 72 / float64(dpi)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.4f %.4f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			w, h, 4+3*i, 5+3*i), nil)
		obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>",
			sizes[i].X, sizes[i].Y, len(jpg)), jpg)
		content := []byte(fmt.Sprintf("q %.4f 0 0 %.4f 0 0 cm /Im0 Do Q", w, h))
		obj(fmt.Sprintf("<< /Length %d >>", len(content)), content)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return os.WriteFile(path, b.Bytes(), 0o644)
}
