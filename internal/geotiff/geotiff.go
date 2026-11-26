// Package geotiff inspects TIFF metadata without decoding pixel data.
//
// It is used to keep georeferenced rasters away from image transcoders that
// would drop their coordinate systems and allocate a full decoded pixel cache.
package geotiff

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotTIFF is returned when a file does not start with a TIFF header.
var ErrNotTIFF = errors.New("not a TIFF image")

const (
	tagCompression         = 259
	tagTileWidth           = 322
	tagTileLength          = 323
	tagModelPixelScale     = 33550
	tagModelTiepoint       = 33922
	tagModelTransformation = 34264
	tagGeoKeyDirectory     = 34735
	tagGeoDoubleParams     = 34736
	tagGeoAsciiParams      = 34737
	tagGDALMetadata        = 42112
	tagGDALNoData          = 42113

	maxIFDs         = 16
	maxEntries      = 4096
	maxIFDBytes     = 1 << 20
	maxSidecarBytes = 64 << 10
)

// Info describes the layout and georeferencing of a TIFF image.
type Info struct {
	// Geospatial is true when the TIFF contains GeoTIFF tags or has a
	// georeferencing sidecar.
	Geospatial bool
	// Tiled is true when the first strip is stored as tiles.
	Tiled bool
	// Compression is the TIFF compression tag value. Zero means unset.
	Compression uint16
}

// Compressed reports whether the TIFF uses any compression other than none.
func (i Info) Compressed() bool {
	return i.Compression != 0 && i.Compression != 1
}

// IsGeospatial reports whether path is a TIFF that carries georeferencing
// metadata either in its tags or in a sidecar file.
func IsGeospatial(path string) bool {
	if !isTIFFPath(path) {
		return false
	}
	info, err := Inspect(path)
	if err == nil && info.Geospatial {
		return true
	}
	return hasGeospatialSidecar(path)
}

// IsTiledCompressed reports whether path is a TIFF already stored as compressed
// tiles. Such files are close to the layout produced by the COG driver, so
// recompressing them is usually not worthwhile.
func IsTiledCompressed(path string) bool {
	info, err := Inspect(path)
	if err != nil {
		return false
	}
	return info.Tiled && info.Compressed()
}

// Inspect reads TIFF metadata from path. Pixel data is never read.
func Inspect(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer func() { _ = f.Close() }()

	var header [16]byte
	if _, err := io.ReadFull(f, header[:8]); err != nil {
		return Info{}, ErrNotTIFF
	}

	var order binary.ByteOrder
	switch string(header[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return Info{}, ErrNotTIFF
	}

	var big bool
	var offset uint64
	switch order.Uint16(header[2:4]) {
	case 42:
		offset = uint64(order.Uint32(header[4:8]))
	case 43:
		if _, err := io.ReadFull(f, header[8:16]); err != nil {
			return Info{}, ErrNotTIFF
		}
		// BigTIFF requires 8-byte offsets and a reserved zero field.
		if order.Uint16(header[4:6]) != 8 || order.Uint16(header[6:8]) != 0 {
			return Info{}, ErrNotTIFF
		}
		big = true
		offset = order.Uint64(header[8:16])
	default:
		return Info{}, ErrNotTIFF
	}

	info := Info{}
	visited := make(map[uint64]bool, maxIFDs)
	for page := 0; offset != 0 && page < maxIFDs; page++ {
		if visited[offset] {
			break
		}
		visited[offset] = true
		next, err := readIFD(f, order, offset, big, &info)
		if err != nil {
			// Metadata found before a malformed IFD is still useful.
			break
		}
		offset = next
	}

	info.Geospatial = info.Geospatial || hasGeospatialSidecar(path)
	return info, nil
}

func readIFD(f *os.File, order binary.ByteOrder, offset uint64, big bool, info *Info) (uint64, error) {
	countSize, entrySize, nextSize := 2, 12, 4
	if big {
		countSize, entrySize, nextSize = 8, 20, 8
	}

	var countBuf [8]byte
	if _, err := f.ReadAt(countBuf[:countSize], int64(offset)); err != nil {
		return 0, err
	}

	var count uint64
	if big {
		count = order.Uint64(countBuf[:8])
	} else {
		count = uint64(order.Uint16(countBuf[:2]))
	}
	if count > maxEntries {
		return 0, errors.New("TIFF IFD has too many entries")
	}

	size := int64(count)*int64(entrySize) + int64(nextSize)
	if size > maxIFDBytes {
		return 0, errors.New("TIFF IFD is too large")
	}

	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, int64(offset)+int64(countSize)); err != nil {
		return 0, err
	}

	for i := uint64(0); i < count; i++ {
		entry := buf[i*uint64(entrySize):]
		tag := order.Uint16(entry[0:2])
		kind := order.Uint16(entry[2:4])

		if isGeospatialTag(tag) {
			info.Geospatial = true
		}
		switch tag {
		case tagTileWidth, tagTileLength:
			info.Tiled = true
		case tagCompression:
			if kind == 3 && readCount(order, entry, big) == 1 {
				info.Compression = readInlineShort(order, entry, big)
			}
		}
	}

	next := buf[count*uint64(entrySize):]
	if big {
		return order.Uint64(next[:8]), nil
	}
	return uint64(order.Uint32(next[:4])), nil
}

func readCount(order binary.ByteOrder, entry []byte, big bool) uint64 {
	if big {
		return order.Uint64(entry[4:12])
	}
	return uint64(order.Uint32(entry[4:8]))
}

func readInlineShort(order binary.ByteOrder, entry []byte, big bool) uint16 {
	if big {
		return order.Uint16(entry[12:14])
	}
	return order.Uint16(entry[8:10])
}

func isGeospatialTag(tag uint16) bool {
	switch tag {
	case tagModelPixelScale, tagModelTiepoint, tagModelTransformation,
		tagGeoKeyDirectory, tagGeoDoubleParams, tagGeoAsciiParams,
		tagGDALMetadata, tagGDALNoData:
		return true
	default:
		return false
	}
}

func isTIFFPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff", ".btf", ".ptif":
		return true
	default:
		return false
	}
}

func hasGeospatialSidecar(path string) bool {
	if sidecarHasSRS(path + ".aux.xml") {
		return true
	}
	if fileExists(path + ".prj") {
		return true
	}

	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for _, candidate := range []string{base + ".tfw", base + ".tifw", base + ".wld"} {
		if fileExists(candidate) {
			return true
		}
	}
	return false
}

func sidecarHasSRS(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, maxSidecarBytes)
	n, _ := io.ReadFull(f, buf)
	content := bytes.ToLower(buf[:n])
	return bytes.Contains(content, []byte("<srs")) ||
		bytes.Contains(content, []byte("<geotransform")) ||
		bytes.Contains(content, []byte("<gcp"))
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
