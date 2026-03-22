# shrink

A high-performance media optimization tool written in Go. `shrink` automatically transcodes video, audio, images, and ebooks into modern, space-efficient formats like AV1, Opus, and AVIF. It can also recursively extract and process files within archives.

## Features

- Video: Transcodes to AV1 (using SVT-AV1) in an MKV container.
- Audio: Transcodes to Opus.
- Images: Converts to AVIF (using ImageMagick).
- Ebooks: Optimizes EPUB/PDF files by compressing internal images and cleaning CSS (using Calibre).
- Archives: Recursively extracts and processes contents of ZIP, RAR, 7z, and more (using unar).
- Smart Filtering: Only processes files if the estimated savings meet your configured thresholds.
- Parallelism: Concurrent processing of different media types.
- Database Support: Integrates with SQLite databases to track and manage media state.

## Installation

Ensure you have Go installed, then run:

```bash
go install github.com/chapmanjacobd/shrink/cmd/shrink@latest
```

## External Dependencies

`shrink` relies on several industry-standard tools for media processing. Ensure these are installed and available in your `PATH`:

| Tool | Required For |
| :--- | :--- |
| FFmpeg | Video and Audio transcoding |
| ImageMagick | Image conversion (specifically `magick`) |
| Calibre | Ebook conversion (`ebook-convert`) |
| unar | Archive extraction (`unar` and `lsar`) |
| ocrmypdf | (Optional) OCR for PDF files |

## Quick Start

### Basic Usage

Shrink all media in a directory:

```bash
shrink .
```

### Advanced Examples

Process only video files and move successful transcodes to a specific directory:

```bash
shrink --video-only --move ./optimized_videos /path/to/media
```

Shrink files while requiring at least 20% space savings for video:

```bash
shrink --min-savings-video=20% /path/to/media
```

Dry run to see what would be processed:

```bash
shrink --simulate /path/to/media
```

## CLI Reference

### Global Flags

| Flag | Description | Default |
| :--- | :--- | :--- |
| -v, --verbose | Enable verbose logging | `false` |
| --simulate | Dry run; don't actually modify files | `false` |
| -y, --no-confirm | Don't ask for confirmation before starting | `false` |
| --video-threads | Maximum concurrent video transcodes | `2` |
| --audio-threads | Maximum concurrent audio transcodes | `4` |
| --image-threads | Maximum concurrent image conversions | `8` |
| --text-threads | Maximum concurrent text conversions | `2` |

### Optimization Flags

| Flag | Description | Default |
| :--- | :--- | :--- |
| --min-savings-video | Minimum savings for video (e.g., "10%" or "50MB") | `5%` |
| --min-savings-audio | Minimum savings for audio | `10%` |
| --min-savings-image | Minimum savings for images | `15%` |
| --preset | SVT-AV1 preset (0-13, lower is slower/better) | `7` |
| --crf | CRF value for SVT-AV1 (0-63, lower is higher quality) | `40` |
| --max-video-height | Maximum video height | `960` |
| --max-video-width | Maximum video width | `1440` |

### Filter Flags

| Flag | Description |
| :--- | :--- |
| --video-only | Only process video files |
| --audio-only | Only process audio files |
| --image-only | Only process image files |
| --text-only | Only process text/ebook files |
| -s, --include | Include paths matching pattern |
| -E, --exclude | Exclude paths matching pattern |

## License

This project is licensed under the BSD 3-Clause License. See the [LICENSE](LICENSE) file for details.
