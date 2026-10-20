package imageio

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// readDPI returns the horizontal resolution recorded in a TIFF (first IFD) or
// JPEG (JFIF header), or 0 when it is missing or unreadable.
func readDPI(path string) float64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff":
		tags, err := ReadTIFFTags(f)
		if err != nil {
			return 0
		}
		return resolutionToDPI(tags[282], tags[296])
	case ".jpg", ".jpeg":
		return jfifDPI(f)
	}
	return 0
}

// ReadTIFFTags reads the numeric SHORT, LONG and RATIONAL tags of the first IFD.
// RATIONAL values are returned as numerator/denominator.
func ReadTIFFTags(r io.ReaderAt) (map[uint16]float64, error) {
	head := make([]byte, 8)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, err
	}
	var bo binary.ByteOrder
	switch string(head[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, io.ErrUnexpectedEOF
	}
	if bo.Uint16(head[2:]) != 42 {
		return nil, io.ErrUnexpectedEOF // BigTIFF is not handled
	}
	ifd := int64(bo.Uint32(head[4:]))
	cnt := make([]byte, 2)
	if _, err := r.ReadAt(cnt, ifd); err != nil {
		return nil, err
	}
	entries := make([]byte, 12*int(bo.Uint16(cnt)))
	if _, err := r.ReadAt(entries, ifd+2); err != nil {
		return nil, err
	}
	tags := map[uint16]float64{}
	for e := entries; len(e) >= 12; e = e[12:] {
		tag, typ, count := bo.Uint16(e), bo.Uint16(e[2:]), bo.Uint32(e[4:])
		if count != 1 {
			continue
		}
		switch typ {
		case 3: // SHORT
			tags[tag] = float64(bo.Uint16(e[8:]))
		case 4: // LONG
			tags[tag] = float64(bo.Uint32(e[8:]))
		case 5: // RATIONAL, stored at an offset
			v := make([]byte, 8)
			if _, err := r.ReadAt(v, int64(bo.Uint32(e[8:]))); err == nil && bo.Uint32(v[4:]) != 0 {
				tags[tag] = float64(bo.Uint32(v)) / float64(bo.Uint32(v[4:]))
			}
		}
	}
	return tags, nil
}

// resolutionToDPI converts a TIFF/JFIF resolution and unit (2 = inch, 3 = cm).
func resolutionToDPI(res, unit float64) float64 {
	switch unit {
	case 3:
		return res * 2.54
	case 1: // no absolute unit
		return 0
	}
	return res // TIFF defaults to inches
}

func jfifDPI(r io.Reader) float64 {
	b := make([]byte, 18)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0
	}
	// SOI, APP0 marker, length, "JFIF\0", version, units, Xdensity
	if b[0] != 0xFF || b[1] != 0xD8 || b[2] != 0xFF || b[3] != 0xE0 || !bytes.Equal(b[6:11], []byte("JFIF\x00")) {
		return 0
	}
	units, x := b[13], float64(binary.BigEndian.Uint16(b[14:]))
	switch units {
	case 1:
		return x
	case 2:
		return x * 2.54
	}
	return 0
}
