package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FolderSize calculates the total size of a folder
func FolderSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

// MoveFile moves a file from source to destination, handling cross-filesystem moves
func MoveFile(src, dst string) error {
	// Capture source timestamps before any operations
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	atime := GetAccessTime(info)
	mtime := info.ModTime()

	// Try Rename first (fast on same filesystem)
	err = os.Rename(src, dst)
	if err == nil {
		return nil
	}

	// If rename fails (e.g. cross-filesystem), try copying
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}

	// Sync to ensure data is written before deleting source
	if err := out.Sync(); err != nil {
		return err
	}

	// Close files before deleting source
	in.Close()
	out.Close()

	// Restore timestamps on destination
	os.Chtimes(dst, atime, mtime)

	return os.Remove(src)
}

// FormatDuration formats seconds into human readable duration
// Prints max two units, skipping zero values (except 0 → "0s")
func FormatDuration(seconds float64) string {
	sInt := int(seconds)
	if sInt == 0 {
		return "0s"
	}
	d := sInt / 86400
	h := (sInt % 86400) / 3600
	m := (sInt % 3600) / 60
	s := sInt % 60

	var parts []string
	if d > 0 {
		parts = append(parts, fmt.Sprintf("%dd", d))
	}
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	if s > 0 {
		parts = append(parts, fmt.Sprintf("%ds", s))
	}

	// Return max two units
	if len(parts) > 2 {
		return parts[0] + " " + parts[1]
	}
	return strings.Join(parts, " ")
}

// FormatSize formats bytes into human readable size using base 1024
func FormatSize(bytes int64) string {
	if bytes == 0 {
		return "-"
	}
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ParseDurationString parses duration strings like "10s", "20m", "1h"
func ParseDurationString(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return time.Duration(n * float64(time.Minute))
}

// ParseBitrate parses bitrate strings like "128kbps", "1Mbps", "1k", "1M"
func ParseBitrate(s string) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	multiplier := int64(1)
	if strings.HasSuffix(s, "kbps") || strings.HasSuffix(s, "k") {
		multiplier = 1000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "kbps"), "k")
	} else if strings.HasSuffix(s, "mbps") || strings.HasSuffix(s, "m") {
		multiplier = 1000000
		s = strings.TrimSuffix(strings.TrimSuffix(s, "mbps"), "m")
	} else if before, ok := strings.CutSuffix(s, "bps"); ok {
		s = before
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n * multiplier
}

// ParseSize parses size strings like "30KiB", "1MB", "1G"
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	multiplier := int64(1)
	if strings.HasSuffix(s, "KiB") || strings.HasSuffix(s, "KB") || strings.HasSuffix(s, "k") || strings.HasSuffix(s, "K") {
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, "KiB"), "KB"), "k"), "K")
	} else if strings.HasSuffix(s, "MiB") || strings.HasSuffix(s, "MB") || strings.HasSuffix(s, "m") || strings.HasSuffix(s, "M") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, "MiB"), "MB"), "m"), "M")
	} else if strings.HasSuffix(s, "GiB") || strings.HasSuffix(s, "GB") || strings.HasSuffix(s, "g") || strings.HasSuffix(s, "G") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, "GiB"), "GB"), "g"), "G")
	} else if before, ok := strings.CutSuffix(s, "B"); ok {
		s = before
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n * multiplier
}

// ParsePercentOrBytes parses percentage or byte values
func ParsePercentOrBytes(s string) float64 {
	s = strings.TrimSpace(s)
	if before, ok := strings.CutSuffix(s, "%"); ok {
		pct, err := strconv.ParseFloat(before, 64)
		if err != nil {
			return 0
		}
		return pct / 100.0
	}
	// Check if it's a size string (has non-digit suffix)
	if len(s) > 0 && (s[len(s)-1] < '0' || s[len(s)-1] > '9') {
		return float64(ParseSize(s))
	}
	// Plain float (could be percentage or bytes depending on value)
	n, _ := strconv.ParseFloat(s, 64)
	return n
}

var SQLiteExtensions = []string{".sqlite", ".sqlite3", ".db", ".db3", ".s3db", ".sl3"}

var AudioExtensions = []string{
	"mka", "opus", "oga", "ogg", "mp3", "mpga", "m2a", "m4a", "m4r", "caf", "m4b", "flac", "wav", "pcm", "aif", "aiff", "wma", "aac", "aa3", "ac3", "ape", "dsf", "dff",
}

var VideoExtensions = []string{
	"str", "aa", "aax", "acm", "adf", "adp", "asf", "dtk", "ads", "ss2", "adx", "aea", "afc", "aix", "al", "apl", "avifs", "gif", "gifv",
	"mac", "aptx", "aptxhd", "aqt", "ast", "obu", "avi", "avr", "avs", "avs2", "avs3", "bfstm", "bcstm", "binka",
	"bit", "bmv", "brstm", "cdg", "cdxl", "xl", "c2", "302", "daud", "str", "adp", "dav", "dss", "dts", "dtshd", "dv",
	"dif", "divx", "cdata", "eac3", "paf", "fap", "flm", "flv", "fsb", "fwse", "g722", "722", "tco", "rco", "heics",
	"g723_1", "g729", "genh", "gsm", "h261", "h26l", "h264", "264", "avc", "mts", "m2ts", "hca", "hevc", "h265", "265", "idf",
	"ifv", "cgi", "ipu", "sf", "ircam", "ivr", "kux", "669", "abc", "amf", "ams", "dbm", "dmf", "dsm", "far", "it", "mdl",
	"med", "mod", "mt2", "mtm", "okt", "psm", "ptm", "s3m", "stm", "ult", "umx", "xm", "itgz", "itr", "itz",
	"mdgz", "mdr", "mdz", "s3gz", "s3r", "s3z", "xmgz", "xmr", "xmz", "669", "amf", "ams", "dbm", "digi", "dmf",
	"dsm", "dtm", "far", "gdm", "ice", "imf", "it", "j2b", "m15", "mdl", "med", "mmcmp", "mms", "mo3", "mod", "mptm",
	"mt2", "mtm", "nst", "okt", "ogm", "ogv", "plm", "ppm", "psm", "pt36", "ptm", "s3m", "sfx", "sfx2", "st26", "stk", "stm",
	"stp", "ult", "umx", "wow", "xm", "xpk", "flv", "dat", "lvf", "m4v", "mkv", "ts", "tp", "mk3d", "webm", "mca", "mcc",
	"mjpg", "mjpeg", "mpg", "mpeg", "mpo", "j2k", "mlp", "mods", "moflex", "mov", "mp4", "3g2", "3gp2", "3gp", "3gpp", "3g2", "mj2", "psp",
	"ism", "ismv", "isma", "f4v", "mp2", "mpa", "mpc", "mjpg", "mpl2", "msf", "mtaf", "ul", "musx", "mvi", "mxg",
	"v", "nist", "sph", "nut", "obu", "oma", "omg", "pjs", "pvf", "yuv", "cif", "qcif", "rgb", "rt", "rsd", "rmvb", "rm",
	"rsd", "rso", "sw", "sb", "sami", "sbc", "msbc", "sbg", "scc", "sdr2", "sds", "sdx", "ser", "sga", "shn", "vb", "son", "imx",
	"sln", "mjpg", "stl", "sup", "svag", "svs", "tak", "thd", "tta", "ans", "art", "asc", "diz", "ice", "vt", "ty", "ty+", "uw", "ub",
	"v210", "yuv10", "vag", "vc1", "rcv", "vob", "viv", "vpk", "vqf", "vql", "vqe", "wmv", "wsd", "xmv", "xvag", "yop", "y4m",
}

var ImageExtensions = []string{
	"aai", "ai", "ait", "avs", "bpg", "png", "arq", "arw", "cr2", "cs1", "dcp", "dng", "eps", "epsf", "ps", "erf", "exv", "fff",
	"gpr", "hdp", "wdp", "jxr", "iiq", "insp", "jpeg", "jpg", "jpe", "mef", "mie", "mos", "mrw", "nef", "nrw", "orf",
	"ori", "pef", "psd", "psb", "psdt", "raf", "raw", "rw2", "rwl", "sr2", "srw", "thm", "tiff", "tif", "x3f", "flif",
	"icc", "icm", "avif", "heic", "heif", "hif", "jp2", "jpf", "jpm", "jpx", "j2c", "jpc", "3fr", "btf", "dcr", "k25",
	"kdc", "miff", "mif", "rwz", "srf", "xcf", "bpg", "doc", "dot", "fla", "fpx", "max", "ppt", "pps", "pot", "vsd", "xls",
	"xlt", "pict", "pct", "360", "dvb", "f4a", "f4b", "f4p", "lrv", "bmp", "bmp2", "bmp3", "jng", "mng", "emf", "wmf",
	"m4p", "qt", "mqv", "qtif", "qti", "qif", "cr3", "crm", "jxl", "crw", "ciff", "ind", "indd", "indt",
	"nksc", "vrd", "xmp", "la", "ofr", "pac", "riff", "rif", "webp", "wv", "djvu", "djv", "dvr-ms",
	"insv", "inx", "swf", "exif", "eip", "pspimage", "fax", "farbfeld", "fits", "fl32", "jbig",
	"pbm", "pfm", "pgm", "phm", "pnm", "ppm", "ptif", "qoi", "tga",
}

var TextExtensions = []string{
	"epub", "mobi", "pdf", "azw", "azw3", "fb2", "djvu",
	"azw4", "cbc", "chm", "docx", "fbz", "htmlz", "lit", "lrf", "odt",
}

var (
	VideoExtensionMap   = make(map[string]bool)
	AudioExtensionMap   = make(map[string]bool)
	ImageExtensionMap   = make(map[string]bool)
	TextExtensionMap    = make(map[string]bool)
	ArchiveExtensionMap = make(map[string]bool)
	MediaExtensionMap   = make(map[string]bool)
)

func init() {
	for _, ext := range VideoExtensions {
		VideoExtensionMap["."+ext] = true
		MediaExtensionMap["."+ext] = true
	}
	for _, ext := range AudioExtensions {
		AudioExtensionMap["."+ext] = true
		MediaExtensionMap["."+ext] = true
	}
	for _, ext := range ImageExtensions {
		ImageExtensionMap["."+ext] = true
		MediaExtensionMap["."+ext] = true
	}
	for _, ext := range TextExtensions {
		TextExtensionMap["."+ext] = true
		MediaExtensionMap["."+ext] = true
	}
	for _, ext := range ArchiveExtensions {
		ArchiveExtensionMap["."+ext] = true
		MediaExtensionMap["."+ext] = true
	}
}

var SubtitleExtensions = []string{
	"srt", "vtt", "mks", "ass", "ssa", "lrc", "idx", "sub",
}

var ArchiveExtensions = []string{
	"7z", "arj", "arc", "adf", "br", "bz2", "gz", "iso", "lha", "lzh", "lzx", "pak", "rar", "sit", "tar", "tar.bz2", "tar.gz", "tar.xz", "tar.zst", "tbz2", "tgz", "txz", "tzst", "xz", "zoo", "zip", "zst", "zstd", "cbz", "cbr",
}

// UnreliableDurationFormats are formats known to have unreliable duration metadata
// (DVD, Blu-ray, camcorder formats, and older codecs)
// The int value is the estimated bitrate in bits per second for each format
var UnreliableDurationFormats = map[string]int{
	// DVD formats (lower bitrate, ~5-10 Mbps typical)
	".vob": 5000000, // DVD Video Object
	".ifo": 5000000, // DVD Information
	".vro": 5000000, // DVD Recording format

	// AVCHD / Camcorder formats (medium bitrate, ~10-20 Mbps typical)
	".m2t":  15000000, // MPEG-2 Transport Stream
	".m2ts": 15000000, // Blu-ray MPEG-2 Transport Stream
	".mts":  15000000, // AVCHD Video
	".mod":  10000000, // Canon/ JVC camcorder format
	".tod":  12000000, // JVC camcorder format

	// Older/lossy codecs (variable bitrate, ~2-8 Mbps typical)
	".divx": 4000000, // DivX codec
	".xvid": 4000000, // Xvid codec
	".rm":   2000000, // RealMedia
	".rmvb": 3000000, // RealMedia Variable Bitrate
	".wmv":  3000000, // Windows Media Video
	".asf":  3000000, // Advanced Systems Format

	// Blu-ray formats (high bitrate, ~20-40 Mbps typical)
	".avchd": 20000000, // AVCHD container
	".bdmv":  30000000, // Blu-ray Disc Movie
	".mpls":  30000000, // Blu-ray Playlist

	// Disc images (use average of contained formats)
	".iso": 8000000, // Disc image (average estimate)
}

// HasUnreliableDuration checks if a file extension is known to have unreliable duration metadata
func HasUnreliableDuration(ext string) bool {
	_, ok := UnreliableDurationFormats[strings.ToLower(ext)]
	return ok
}

// GetEstimatedBitrate returns the estimated bitrate for a format
// Returns 0 if the format is not in the unreliable formats map
func GetEstimatedBitrate(ext string) int {
	return UnreliableDurationFormats[strings.ToLower(ext)]
}

// Default bitrates for duration estimation (bits per second)
const (
	DefaultAudioBitrate = 256000  // 256 kbps
	DefaultVideoBitrate = 1500000 // 1500 kbps
)

// EstimateDurationFromSize estimates duration from file size and bitrate
// Returns duration in seconds
func EstimateDurationFromSize(size int64, isVideo bool) float64 {
	bitrate := DefaultAudioBitrate
	if isVideo {
		bitrate = DefaultVideoBitrate
	}
	return float64(size) / float64(bitrate) * 8
}

// EstimateDurationFromSizeWithFormat estimates duration from file size using format-specific bitrate
// Returns duration in seconds
func EstimateDurationFromSizeWithFormat(size int64, ext string) float64 {
	bitrate := GetEstimatedBitrate(ext)
	if bitrate <= 0 {
		// Fallback to default estimation
		return EstimateDurationFromSize(size, true)
	}
	return float64(size) / float64(bitrate) * 8
}

// GetDurationForTimeout returns a duration value suitable for timeout calculations.
// If the provided duration is valid (> 0), it returns it as-is.
// If duration is <= 0, it estimates from file size:
//   - For unreliable formats (DVD, Blu-ray, etc.), uses format-specific bitrate
//   - For other formats, uses default video bitrate
//
// Returns 0 if size is invalid (<= 0)
func GetDurationForTimeout(duration float64, size int64, ext string) float64 {
	if est, ok := ShouldOverrideDuration(duration, size, ext); ok {
		return est
	}
	if duration > 0 {
		return duration
	}
	if size <= 0 {
		return 0
	}
	return EstimateDurationFromSizeWithFormat(size, ext)
}

// ShouldOverrideDuration determines if reported duration should be overridden
// with an estimate based on file size. Returns true only when:
//   - File extension matches an unreliable format
//   - Reported duration is suspiciously low (< 2 minutes)
//   - Estimated duration is much higher (> 2 minutes)
func ShouldOverrideDuration(reportedDuration float64, size int64, ext string) (float64, bool) {
	if reportedDuration >= 120 {
		// Duration is >= 2 minutes, trust it
		return 0, false
	}
	if !HasUnreliableDuration(ext) {
		// Not an unreliable format, trust reported duration
		return 0, false
	}

	estimatedDuration := EstimateDurationFromSizeWithFormat(size, ext)
	if estimatedDuration <= 120 {
		// Estimated duration is also low, trust reported duration
		return 0, false
	}

	// Override with estimated duration
	return estimatedDuration, true
}

// PrintTable prints a formatted table with dynamic column widths
func PrintTable(headers []string, rows [][]string) {
	fmt.Print(PrintTableToString(headers, rows))
}

// PrintTableToString returns a formatted table string with dynamic column widths
func PrintTableToString(headers []string, rows [][]string) string {
	if len(headers) == 0 {
		return ""
	}

	numCols := len(headers)
	colWidths := make([]int, numCols)

	// Calculate max width for each column (headers)
	for i, h := range headers {
		if len(h) > colWidths[i] {
			colWidths[i] = len(h)
		}
	}

	// Calculate max width for each column (rows)
	for _, row := range rows {
		for i, cell := range row {
			if i < numCols && len(cell) > colWidths[i] {
				colWidths[i] = len(cell)
			}
		}
	}

	// Determine column alignment based on header names
	// Default is right-align for numbers; special-case text columns for left-align
	leftAlignHeaders := map[string]bool{
		"Media Type": true, "Type": true, "Name": true, "File": true,
		"Path": true, "Status": true, "Format": true, "Codec": true,
	}
	isNumericCol := make([]bool, numCols)
	for i, h := range headers {
		if !leftAlignHeaders[h] {
			isNumericCol[i] = true
		}
	}

	// Build format strings (add 1 extra space padding per column)
	// Use right-align for numeric columns, left-align for text
	var headerFormatParts []string
	for _, w := range colWidths {
		headerFormatParts = append(headerFormatParts, fmt.Sprintf("%%-%ds", w+1))
	}
	headerFormat := strings.Join(headerFormatParts, " ") + "\n"

	var rowFormatParts []string
	for i, w := range colWidths {
		if isNumericCol[i] {
			rowFormatParts = append(rowFormatParts, fmt.Sprintf("%%%ds", w+1))
		} else {
			rowFormatParts = append(rowFormatParts, fmt.Sprintf("%%-%ds", w+1))
		}
	}
	rowFormat := strings.Join(rowFormatParts, " ") + "\n"

	var sb strings.Builder

	// Print headers
	headerArgs := make([]any, len(headers))
	for i, h := range headers {
		headerArgs[i] = h
	}
	sb.WriteString(fmt.Sprintf(headerFormat, headerArgs...))

	// Print rows
	for _, row := range rows {
		rowArgs := make([]any, numCols)
		for i := range numCols {
			if i < len(row) {
				rowArgs[i] = row[i]
			} else {
				rowArgs[i] = ""
			}
		}
		sb.WriteString(fmt.Sprintf(rowFormat, rowArgs...))
	}

	return sb.String()
}
