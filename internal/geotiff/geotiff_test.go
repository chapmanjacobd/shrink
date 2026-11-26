package geotiff

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

type testTag struct {
	id    uint16
	kind  uint16
	count uint32
	value uint32
}

func writeTIFF(t *testing.T, order binary.ByteOrder, big bool, tags []testTag) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "image.tif")
	var buf []byte

	if order == binary.LittleEndian {
		buf = append(buf, 'I', 'I')
	} else {
		buf = append(buf, 'M', 'M')
	}

	header := make([]byte, 8)
	if big {
		order.PutUint16(header[0:2], 43)
		order.PutUint16(header[2:4], 8)
		// Magic, offset size, reserved zero field.
		buf = append(buf, header[:4]...)
		buf = append(buf, 0, 0)
		offset := make([]byte, 8)
		order.PutUint64(offset, 16)
		buf = append(buf, offset...)
	} else {
		order.PutUint16(header[0:2], 42)
		order.PutUint32(header[2:6], 8)
		buf = append(buf, header[:6]...)
	}

	countSize, entrySize, nextSize := 2, 12, 4
	if big {
		countSize, entrySize, nextSize = 8, 20, 8
	}

	count := make([]byte, countSize)
	if big {
		order.PutUint64(count, uint64(len(tags)))
	} else {
		order.PutUint16(count, uint16(len(tags)))
	}
	buf = append(buf, count...)

	for _, tag := range tags {
		entry := make([]byte, entrySize)
		order.PutUint16(entry[0:2], tag.id)
		order.PutUint16(entry[2:4], tag.kind)
		if big {
			order.PutUint64(entry[4:12], uint64(tag.count))
			order.PutUint64(entry[12:20], uint64(tag.value))
		} else {
			order.PutUint32(entry[4:8], tag.count)
			order.PutUint32(entry[8:12], tag.value)
		}
		buf = append(buf, entry...)
	}
	buf = append(buf, make([]byte, nextSize)...)

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("write TIFF: %v", err)
	}
	return path
}

func TestInspect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		order       binary.ByteOrder
		big         bool
		tags        []testTag
		geospatial  bool
		tiled       bool
		compression uint16
	}{
		{
			name:  "plain classic TIFF",
			order: binary.LittleEndian,
			tags:  []testTag{{id: 256, kind: 3, count: 1, value: 100}},
		},
		{
			name:       "GeoKeyDirectory makes it geospatial",
			order:      binary.LittleEndian,
			tags:       []testTag{{id: tagGeoKeyDirectory, kind: 3, count: 4, value: 100}},
			geospatial: true,
		},
		{
			name:       "ModelPixelScale makes it geospatial",
			order:      binary.LittleEndian,
			tags:       []testTag{{id: tagModelPixelScale, kind: 12, count: 3, value: 100}},
			geospatial: true,
		},
		{
			name:        "tiled deflate TIFF is compressed",
			order:       binary.LittleEndian,
			tags:        []testTag{{id: tagTileWidth, kind: 3, count: 1, value: 512}, {id: tagCompression, kind: 3, count: 1, value: 8}},
			tiled:       true,
			compression: 8,
		},
		{
			name:        "striped uncompressed TIFF",
			order:       binary.LittleEndian,
			tags:        []testTag{{id: tagCompression, kind: 3, count: 1, value: 1}},
			compression: 1,
		},
		{
			name:       "big endian geospatial TIFF",
			order:      binary.BigEndian,
			tags:       []testTag{{id: tagModelTiepoint, kind: 12, count: 6, value: 100}},
			geospatial: true,
		},
		{
			name:       "BigTIFF geospatial",
			order:      binary.LittleEndian,
			big:        true,
			tags:       []testTag{{id: tagGeoKeyDirectory, kind: 3, count: 4, value: 100}},
			geospatial: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := writeTIFF(t, tt.order, tt.big, tt.tags)
			info, err := Inspect(path)
			if err != nil {
				t.Fatalf("Inspect returned error: %v", err)
			}
			if info.Geospatial != tt.geospatial {
				t.Errorf("Geospatial = %v, want %v", info.Geospatial, tt.geospatial)
			}
			if info.Tiled != tt.tiled {
				t.Errorf("Tiled = %v, want %v", info.Tiled, tt.tiled)
			}
			if info.Compression != tt.compression {
				t.Errorf("Compression = %d, want %d", info.Compression, tt.compression)
			}
			if got := info.Compressed(); got != (tt.compression != 0 && tt.compression != 1) {
				t.Errorf("Compressed() = %v", got)
			}
			if got := IsGeospatial(path); got != tt.geospatial {
				t.Errorf("IsGeospatial = %v, want %v", got, tt.geospatial)
			}
			if got := IsTiledCompressed(path); got != (tt.tiled && tt.compression != 0 && tt.compression != 1) {
				t.Errorf("IsTiledCompressed = %v", got)
			}
		})
	}
}

func TestInspectRejectsNonTIFF(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "image.tif")
	if err := os.WriteFile(path, []byte("not a tiff"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(path); err != ErrNotTIFF {
		t.Errorf("Inspect error = %v, want ErrNotTIFF", err)
	}
	if IsGeospatial(path) {
		t.Error("IsGeospatial = true for a non-TIFF file")
	}
}

func TestSidecarGeoreferencing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sidecar  string
		content  string
		expected bool
	}{
		{name: "PRJ file", sidecar: "image.tif.prj", content: "GEOGCS[\"WGS 84\"]", expected: true},
		{name: "world file", sidecar: "image.tfw", content: "1.0", expected: true},
		{name: "AUX XML with SRS", sidecar: "image.tif.aux.xml", content: "<PAMDataset><SRS>EPSG:4326</SRS></PAMDataset>", expected: true},
		{name: "AUX XML with GeoTransform", sidecar: "image.tif.aux.xml", content: "<PAMDataset><GeoTransform>1,2,3,4,5,6</GeoTransform></PAMDataset>", expected: true},
		{name: "AUX XML without georeferencing", sidecar: "image.tif.aux.xml", content: "<PAMDataset><PAMRasterBand><Metadata><MDI>STATISTICS</MDI></Metadata></PAMRasterBand></PAMDataset>"},
		{name: "unrelated sidecar", sidecar: "image.txt", content: "hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "image.tif")
			if err := os.WriteFile(path, []byte("II*\x00\x08\x00\x00\x00\x00\x00\x00\x00\x00\x00"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tt.sidecar), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := IsGeospatial(path); got != tt.expected {
				t.Errorf("IsGeospatial = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNonTIFFWithSidecarIsNotGeospatial(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "image.jpg")
	if err := os.WriteFile(path, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image.jpg.aux.xml"), []byte("<SRS>EPSG:4326</SRS>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsGeospatial(path) {
		t.Error("IsGeospatial should only inspect TIFF files")
	}
}
