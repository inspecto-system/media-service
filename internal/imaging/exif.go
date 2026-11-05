package imaging

import (
	"bytes"
	"encoding/binary"
)

// Orientation values of the EXIF Orientation tag (CIPA DC-008, tag 0x0112).
const (
	orientationNormal     = 1
	orientationTransverse = 8
	orientationTag        = 0x0112
	typeShort             = 3
)

var exifHeader = []byte("Exif\x00\x00")

// jpegOrientation reads the EXIF Orientation of a JPEG file. Anything unexpected — no EXIF,
// truncated or malformed data, an unknown value — yields the normal orientation: a wrong rotation
// is better than a rejected photo, and no other EXIF field is ever read.
func jpegOrientation(data []byte) int {
	for pos := 2; pos+4 <= len(data); {
		if data[pos] != 0xFF {
			return orientationNormal
		}
		marker := data[pos+1]
		if marker == 0xDA || marker == 0xD9 { // start of scan or end of image: no more metadata
			return orientationNormal
		}
		length := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if length < 2 || pos+2+length > len(data) {
			return orientationNormal
		}
		segment := data[pos+4 : pos+2+length]
		if marker == 0xE1 && bytes.HasPrefix(segment, exifHeader) {
			return tiffOrientation(segment[len(exifHeader):])
		}
		pos += 2 + length
	}
	return orientationNormal
}

// tiffOrientation reads the Orientation tag from IFD0 of a TIFF structure.
func tiffOrientation(tiff []byte) int {
	order, ifd, ok := tiffHeader(tiff)
	if !ok {
		return orientationNormal
	}
	return ifdOrientation(tiff, order, ifd)
}

// tiffHeader returns the byte order and the offset of IFD0 of a well-formed TIFF header.
func tiffHeader(tiff []byte) (binary.ByteOrder, int, bool) {
	if len(tiff) < 8 {
		return nil, 0, false
	}
	var order binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return nil, 0, false
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return nil, 0, false
	}
	ifd := int(order.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return nil, 0, false
	}
	return order, ifd, true
}

// ifdOrientation looks for a valid Orientation entry in the IFD at the given offset.
func ifdOrientation(tiff []byte, order binary.ByteOrder, ifd int) int {
	entries := int(order.Uint16(tiff[ifd : ifd+2]))
	for i := range entries {
		entry := ifd + 2 + i*12
		if entry+12 > len(tiff) {
			return orientationNormal
		}
		if order.Uint16(tiff[entry:entry+2]) != orientationTag {
			continue
		}
		if order.Uint16(tiff[entry+2:entry+4]) != typeShort {
			return orientationNormal
		}
		value := int(order.Uint16(tiff[entry+8 : entry+10]))
		if value < orientationNormal || value > orientationTransverse {
			return orientationNormal
		}
		return value
	}
	return orientationNormal
}
