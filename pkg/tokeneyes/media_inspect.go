package tokeneyes

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxOfficeExpanded = int64(64 << 20)

func inspectMedia(label, path, sha string, b []byte) (Asset, []byte, bool, error) {
	mime := detectMIME(b)
	if mime == "" {
		return Asset{}, nil, false, nil
	}
	a := Asset{ID: "asset-" + sha[:12], Label: label, Path: path, SHA256: sha, Bytes: int64(len(b)), DetectedMIME: mime}
	switch {
	case strings.HasPrefix(mime, "image/"):
		a.SourceKind = "image"
		m, err := inspectImage(mime, b)
		if err != nil {
			return a, nil, true, err
		}
		a.Image = &m
		if m.Frames > 1 {
			a.Warnings = append(a.Warnings, "animated image: only the first frame is planned")
		}
		return a, nil, true, nil
	case strings.HasPrefix(mime, "audio/"):
		a.SourceKind = "audio"
		m, err := inspectAudio(mime, b)
		if err != nil {
			return a, nil, true, err
		}
		a.Audio = &m
		return a, nil, true, nil
	case mime == "application/pdf":
		a.SourceKind = "document"
		m, text, err := inspectPDF(b)
		a.Document = &m
		return a, text, true, err
	case strings.Contains(mime, "openxmlformats"):
		a.SourceKind = "document"
		m, text, err := inspectOffice(mime, b)
		a.Document = &m
		if m.Format == "docx" {
			a.Warnings = append(a.Warnings, "embedded images, tracked layout, and macros are ignored")
		}
		if m.Format == "pptx" {
			a.Warnings = append(a.Warnings, "embedded media and speaker notes are ignored")
		}
		if m.Format == "xlsx" {
			a.Warnings = append(a.Warnings, "formulas are serialized but never evaluated; charts and macros are ignored")
		}
		return a, text, true, err
	}
	return Asset{}, nil, false, nil
}

func detectMIME(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 3 && bytes.Equal(b[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		return "audio/wav"
	case len(b) >= 4 && string(b[:4]) == "fLaC":
		return "audio/flac"
	case len(b) >= 4 && string(b[:4]) == "OggS":
		return "audio/ogg"
	case len(b) >= 3 && string(b[:3]) == "ID3", len(b) >= 2 && b[0] == 0xff && b[1]&0xe0 == 0xe0:
		return "audio/mpeg"
	case len(b) >= 8 && string(b[4:8]) == "ftyp":
		return "audio/mp4"
	case len(b) >= 2 && b[0] == 0xff && b[1]&0xf6 == 0xf0:
		return "audio/aac"
	case len(b) >= 5 && string(b[:5]) == "%PDF-":
		return "application/pdf"
	case len(b) >= 4 && bytes.Equal(b[:4], []byte{'P', 'K', 3, 4}):
		return detectOfficeMIME(b)
	default:
		return ""
	}
}

func detectOfficeMIME(b []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return ""
	}
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml":
			return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case f.Name == "ppt/presentation.xml":
			return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		case f.Name == "xl/workbook.xml":
			return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		}
	}
	return ""
}

func inspectImage(mime string, b []byte) (ImageMetadata, error) {
	if mime == "image/webp" {
		return inspectWebP(b)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return ImageMetadata{}, fmt.Errorf("invalid image: %w", err)
	}
	m := ImageMetadata{Width: cfg.Width, Height: cfg.Height, Frames: 1}
	if mime == "image/gif" {
		m.Frames, err = countGIFFrames(b)
		if err != nil {
			return m, err
		}
	}
	if mime == "image/jpeg" {
		m.Orientation = jpegOrientation(b)
		if m.Orientation >= 5 && m.Orientation <= 8 {
			m.Width, m.Height = m.Height, m.Width
		}
	}
	return m, nil
}

func countGIFFrames(b []byte) (int, error) {
	if len(b) < 13 {
		return 0, fmt.Errorf("truncated GIF")
	}
	p := 13
	if b[10]&0x80 != 0 {
		p += 3 * (1 << ((b[10] & 7) + 1))
	}
	frames := 0
	skipBlocks := func() bool {
		for {
			if p >= len(b) {
				return false
			}
			n := int(b[p])
			p++
			if n == 0 {
				return true
			}
			if p+n > len(b) {
				return false
			}
			p += n
		}
	}
	for p < len(b) {
		switch b[p] {
		case 0x2c:
			p++
			if p+9 > len(b) {
				return 0, fmt.Errorf("truncated GIF image descriptor")
			}
			packed := b[p+8]
			p += 9
			if packed&0x80 != 0 {
				p += 3 * (1 << ((packed & 7) + 1))
			}
			if p >= len(b) {
				return 0, fmt.Errorf("truncated GIF image data")
			}
			p++
			if !skipBlocks() {
				return 0, fmt.Errorf("truncated GIF image data")
			}
			frames++
		case 0x21:
			p += 2
			if !skipBlocks() {
				return 0, fmt.Errorf("truncated GIF extension")
			}
		case 0x3b:
			if frames == 0 {
				return 0, fmt.Errorf("GIF contains no frames")
			}
			return frames, nil
		default:
			return 0, fmt.Errorf("invalid GIF block")
		}
	}
	if frames == 0 {
		return 0, fmt.Errorf("GIF contains no frames")
	}
	return frames, nil
}

func inspectWebP(b []byte) (ImageMetadata, error) {
	if len(b) < 30 {
		return ImageMetadata{}, fmt.Errorf("truncated WebP")
	}
	for p := 12; p+8 <= len(b); {
		kind, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:p+8]))
		p += 8
		if p+n > len(b) {
			break
		}
		switch kind {
		case "VP8X":
			if n >= 10 {
				return ImageMetadata{Width: 1 + int(b[p+4]) + (int(b[p+5]) << 8) + (int(b[p+6]) << 16), Height: 1 + int(b[p+7]) + (int(b[p+8]) << 8) + (int(b[p+9]) << 16), Frames: 1}, nil
			}
		case "VP8 ":
			if n >= 10 && b[p+3] == 0x9d && b[p+4] == 0x01 && b[p+5] == 0x2a {
				return ImageMetadata{Width: int(binary.LittleEndian.Uint16(b[p+6:p+8]) & 0x3fff), Height: int(binary.LittleEndian.Uint16(b[p+8:p+10]) & 0x3fff), Frames: 1}, nil
			}
		case "VP8L":
			if n >= 5 {
				bits := binary.LittleEndian.Uint32(b[p+1 : p+5])
				return ImageMetadata{Width: int(bits&0x3fff) + 1, Height: int((bits>>14)&0x3fff) + 1, Frames: 1}, nil
			}
		}
		p += n + n%2
	}
	return ImageMetadata{}, fmt.Errorf("unsupported WebP header")
}

func jpegOrientation(b []byte) int {
	for i := 2; i+4 < len(b) && b[i] == 0xff; {
		marker, n := b[i+1], int(binary.BigEndian.Uint16(b[i+2:i+4]))
		if n < 2 || i+2+n > len(b) {
			break
		}
		if marker == 0xe1 && n >= 16 && string(b[i+4:i+10]) == "Exif\x00\x00" {
			t := b[i+10 : i+2+n]
			little := len(t) > 8 && string(t[:2]) == "II"
			u16 := func(x []byte) uint16 {
				if little {
					return binary.LittleEndian.Uint16(x)
				}
				return binary.BigEndian.Uint16(x)
			}
			if len(t) < 8 {
				return 0
			}
			off := int(func() uint32 {
				if little {
					return binary.LittleEndian.Uint32(t[4:8])
				}
				return binary.BigEndian.Uint32(t[4:8])
			}())
			if off+2 > len(t) {
				return 0
			}
			count := int(u16(t[off : off+2]))
			for j := 0; j < count; j++ {
				p := off + 2 + j*12
				if p+12 > len(t) {
					break
				}
				if u16(t[p:p+2]) == 0x112 {
					return int(u16(t[p+8 : p+10]))
				}
			}
		}
		i += 2 + n
	}
	return 0
}

func inspectAudio(mime string, b []byte) (AudioMetadata, error) {
	switch mime {
	case "audio/wav":
		return inspectWAV(b)
	case "audio/flac":
		return inspectFLAC(b)
	case "audio/ogg":
		return inspectOgg(b)
	case "audio/mpeg":
		return inspectMP3(b)
	case "audio/mp4":
		return inspectMP4(b)
	case "audio/aac":
		return inspectAAC(b)
	default:
		return AudioMetadata{}, fmt.Errorf("unsupported audio")
	}
}

func inspectWAV(b []byte) (AudioMetadata, error) {
	m := AudioMetadata{Codec: "wav"}
	var byteRate uint32
	var dataSize uint32
	for p := 12; p+8 <= len(b); {
		kind := string(b[p : p+4])
		n := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		p += 8
		if p+n > len(b) {
			return m, fmt.Errorf("truncated WAV")
		}
		if kind == "fmt " && n >= 16 {
			m.Channels = int(binary.LittleEndian.Uint16(b[p+2 : p+4]))
			m.SampleRateHz = int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
			byteRate = binary.LittleEndian.Uint32(b[p+8 : p+12])
		}
		if kind == "data" {
			dataSize = uint32(n)
		}
		p += n + n%2
	}
	if byteRate == 0 {
		return m, fmt.Errorf("WAV duration unavailable")
	}
	m.DurationMillis = int64(dataSize) * 1000 / int64(byteRate)
	return m, nil
}

func inspectFLAC(b []byte) (AudioMetadata, error) {
	if len(b) < 42 {
		return AudioMetadata{}, fmt.Errorf("truncated FLAC")
	}
	x := binary.BigEndian.Uint64(b[18:26])
	rate := int(x >> 44)
	channels := int((x>>41)&7) + 1
	samples := int64(x & 0xfffffffff)
	m := AudioMetadata{Codec: "flac", SampleRateHz: rate, Channels: channels}
	if rate > 0 {
		m.DurationMillis = samples * 1000 / int64(rate)
	}
	return m, nil
}
func inspectOgg(b []byte) (AudioMetadata, error) {
	m := AudioMetadata{Codec: "ogg"}
	if p := bytes.Index(b, []byte("OpusHead")); p >= 0 && p+12 <= len(b) {
		m.Codec = "opus"
		m.Channels = int(b[p+9])
		m.SampleRateHz = 48000
	}
	if p := bytes.Index(b, []byte("\x01vorbis")); p >= 0 && p+12 <= len(b) {
		m.Codec = "vorbis"
		m.Channels = int(b[p+7])
		m.SampleRateHz = int(binary.LittleEndian.Uint32(b[p+8 : p+12]))
	}
	var granule uint64
	for p := 0; p+27 <= len(b); {
		q := bytes.Index(b[p:], []byte("OggS"))
		if q < 0 {
			break
		}
		p += q
		if p+14 <= len(b) {
			g := binary.LittleEndian.Uint64(b[p+6 : p+14])
			if g > granule {
				granule = g
			}
		}
		p += 4
	}
	if m.SampleRateHz > 0 {
		m.DurationMillis = int64(granule) * 1000 / int64(m.SampleRateHz)
	}
	if m.DurationMillis == 0 {
		return m, fmt.Errorf("Ogg duration unavailable")
	}
	return m, nil
}
func inspectMP3(b []byte) (AudioMetadata, error) {
	p := 0
	if len(b) >= 10 && string(b[:3]) == "ID3" {
		p = 10 + int(b[6]&0x7f)<<21 + int(b[7]&0x7f)<<14 + int(b[8]&0x7f)<<7 + int(b[9]&0x7f)
	}
	for p+4 < len(b) && !(b[p] == 0xff && b[p+1]&0xe0 == 0xe0) {
		p++
	}
	if p+4 >= len(b) {
		return AudioMetadata{}, fmt.Errorf("MP3 frame not found")
	}
	m := AudioMetadata{Codec: "mp3", Channels: 2}
	frames, totalSamples := 0, int64(0)
	for p+4 <= len(b) {
		h := binary.BigEndian.Uint32(b[p : p+4])
		if h&0xffe00000 != 0xffe00000 {
			break
		}
		versionBits := (h >> 19) & 3
		layerBits := (h >> 17) & 3
		if versionBits == 1 || layerBits != 1 {
			break
		}
		bitrateIndex := (h >> 12) & 15
		rateIndex := (h >> 10) & 3
		if bitrateIndex == 0 || bitrateIndex == 15 || rateIndex == 3 {
			break
		}
		rates := []int{44100, 48000, 32000}
		rate := rates[rateIndex]
		mpeg1 := versionBits == 3
		if versionBits == 2 {
			rate /= 2
		} else if versionBits == 0 {
			rate /= 4
		}
		br1 := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
		br2 := []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
		br := br2[bitrateIndex]
		samples := int64(576)
		coefficient := 72
		if mpeg1 {
			br = br1[bitrateIndex]
			samples = 1152
			coefficient = 144
		}
		frameLen := coefficient*br*1000/rate + int((h>>9)&1)
		if frameLen < 4 || p+frameLen > len(b) {
			break
		}
		if frames == 0 {
			m.SampleRateHz = rate
			if (h>>6)&3 == 3 {
				m.Channels = 1
			}
		}
		frames++
		totalSamples += samples
		p += frameLen
	}
	if frames == 0 || m.SampleRateHz == 0 {
		return m, fmt.Errorf("MP3 duration unavailable")
	}
	m.DurationMillis = totalSamples * 1000 / int64(m.SampleRateHz)
	return m, nil
}
func inspectAAC(b []byte) (AudioMetadata, error) {
	if len(b) < 7 {
		return AudioMetadata{}, fmt.Errorf("truncated AAC")
	}
	rates := []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	idx := (b[2] >> 2) & 15
	if int(idx) >= len(rates) {
		return AudioMetadata{}, fmt.Errorf("invalid AAC rate")
	}
	rate := rates[idx]
	frames := 0
	for p := 0; p+7 <= len(b); {
		if b[p] != 0xff {
			break
		}
		n := (int(b[p+3]&3) << 11) | (int(b[p+4]) << 3) | int(b[p+5]>>5)
		if n < 7 || p+n > len(b) {
			break
		}
		frames++
		p += n
	}
	return AudioMetadata{Codec: "aac", SampleRateHz: rate, Channels: int((b[2]&1)<<2 | b[3]>>6), DurationMillis: int64(frames) * 1024 * 1000 / int64(rate)}, nil
}
func inspectMP4(b []byte) (AudioMetadata, error) {
	m := AudioMetadata{Codec: "m4a"}
	p := bytes.Index(b, []byte("mvhd"))
	if p < 0 || p+24 > len(b) {
		return m, fmt.Errorf("M4A duration unavailable")
	}
	version := b[p+4]
	var scale, dur uint32
	if version == 0 && p+24 <= len(b) {
		scale = binary.BigEndian.Uint32(b[p+16 : p+20])
		dur = binary.BigEndian.Uint32(b[p+20 : p+24])
	}
	if scale == 0 {
		return m, fmt.Errorf("M4A duration unavailable")
	}
	m.DurationMillis = int64(dur) * 1000 / int64(scale)
	if audio := bytes.Index(b, []byte("mp4a")); audio >= 0 && audio+32 <= len(b) {
		m.Codec = "aac/m4a"
		m.Channels = int(binary.BigEndian.Uint16(b[audio+20 : audio+22]))
		m.SampleRateHz = int(binary.BigEndian.Uint32(b[audio+28:audio+32]) >> 16)
	}
	return m, nil
}

var pdfPageRE = regexp.MustCompile(`/Type\s*/Page(?:\s|/|>>)`)
var pdfBoxRE = regexp.MustCompile(`/MediaBox\s*\[\s*[-\d.]+\s+[-\d.]+\s+([-\d.]+)\s+([-\d.]+)`)
var pdfTextRE = regexp.MustCompile(`\(([^()]*)\)\s*Tj`)

func inspectPDF(b []byte) (DocumentMetadata, []byte, error) {
	m := DocumentMetadata{Format: "pdf", Encrypted: bytes.Contains(b, []byte("/Encrypt")), ExtractionStatus: "none"}
	if m.Encrypted {
		return m, nil, fmt.Errorf("encrypted PDF is not supported")
	}
	pageStarts := pdfPageRE.FindAllIndex(b, -1)
	m.Pages = len(pageStarts)
	if m.Pages == 0 {
		m.Pages = 1
		pageStarts = [][]int{{0, 0}}
	}
	var out strings.Builder
	for i, start := range pageStarts {
		end := len(b)
		if i+1 < len(pageStarts) {
			end = pageStarts[i+1][0]
		}
		segment := b[start[0]:end]
		p := PageMetadata{Number: i + 1, Classification: "unknown"}
		if box := pdfBoxRE.FindSubmatch(segment); len(box) > 0 {
			p.WidthPoints, _ = strconv.ParseFloat(string(box[1]), 64)
			p.HeightPoints, _ = strconv.ParseFloat(string(box[2]), 64)
		}
		texts := pdfTextRE.FindAllSubmatch(segment, -1)
		hasImage := bytes.Contains(segment, []byte("/Subtype /Image"))
		if len(texts) > 0 {
			if hasImage {
				p.Classification = "mixed"
			} else {
				p.Classification = "text-bearing"
			}
			fmt.Fprintf(&out, "[page %d]\n", i+1)
			for _, text := range texts {
				out.WriteString(pdfUnescape(string(text[1])))
				out.WriteByte('\n')
			}
		} else if hasImage {
			p.Classification = "image-only"
		}
		m.PageMetadata = append(m.PageMetadata, p)
	}
	// Inspect common Flate streams without following object references. Text
	// found here is retained under an explicit unallocated boundary rather than
	// being falsely assigned to a page.
	for _, loc := range regexp.MustCompile(`(?s)/FlateDecode.*?stream\r?\n(.*?)\r?\nendstream`).FindAllSubmatch(b, -1) {
		zr, e := zlib.NewReader(bytes.NewReader(loc[1]))
		if e != nil {
			continue
		}
		plain, _ := io.ReadAll(io.LimitReader(zr, 8<<20))
		zr.Close()
		found := pdfTextRE.FindAllSubmatch(plain, -1)
		if len(found) > 0 {
			out.WriteString("[unallocated compressed PDF text]\n")
		}
		for _, t := range found {
			fmt.Fprintf(&out, "%s\n", pdfUnescape(string(t[1])))
		}
	}
	text := []byte(out.String())
	m.ExtractedBytes = int64(len(text))
	if len(text) > 0 {
		m.ExtractionStatus = "partial"
	}
	return m, text, nil
}
func pdfUnescape(s string) string {
	return strings.NewReplacer(`\n`, `\n`, `\r`, `\n`, `\t`, `\t`, `\(`, `(`, `\)`, `)`, `\\`, `\`).Replace(s)
}

func inspectOffice(mime string, b []byte) (DocumentMetadata, []byte, error) {
	format := "docx"
	prefix := "word/"
	if strings.Contains(mime, "presentation") {
		format = "pptx"
		prefix = "ppt/slides/"
	}
	if strings.Contains(mime, "spreadsheet") {
		format = "xlsx"
		prefix = "xl/worksheets/"
	}
	m := DocumentMetadata{Format: format, ExtractionStatus: "complete"}
	zr, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		return m, nil, fmt.Errorf("malformed Office container: %w", e)
	}
	var files []*zip.File
	var expanded int64
	for _, f := range zr.File {
		if f.UncompressedSize64 > uint64(maxOfficeExpanded) || expanded > maxOfficeExpanded-int64(f.UncompressedSize64) {
			return m, nil, fmt.Errorf("Office expanded-size limit exceeded")
		}
		expanded += int64(f.UncompressedSize64)
		if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, ".xml") {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	var out strings.Builder
	for _, f := range files {
		r, e := f.Open()
		if e != nil {
			return m, nil, e
		}
		x, e := io.ReadAll(io.LimitReader(r, 16<<20))
		r.Close()
		if e != nil {
			return m, nil, e
		}
		label := filepath.Base(f.Name)
		serialized, e := serializeOfficeXML(format, label, x)
		if e != nil {
			return m, nil, e
		}
		out.WriteString(serialized)
	}
	text := []byte(out.String())
	m.ExtractedBytes = int64(len(text))
	if format == "pptx" {
		m.Pages = len(files)
	}
	return m, text, nil
}

func serializeOfficeXML(format, label string, x []byte) (string, error) {
	d := xml.NewDecoder(bytes.NewReader(x))
	var out strings.Builder
	switch format {
	case "xlsx":
		fmt.Fprintf(&out, "[sheet-part %s]\n", label)
		cell, element, formula, value := "", "", "", ""
		for {
			tok, e := d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return "", fmt.Errorf("invalid Office XML: %w", e)
			}
			switch v := tok.(type) {
			case xml.StartElement:
				if v.Name.Local == "c" {
					cell, formula, value = "", "", ""
					for _, a := range v.Attr {
						if a.Name.Local == "r" {
							cell = a.Value
						}
					}
				}
				if v.Name.Local == "f" || v.Name.Local == "v" || v.Name.Local == "t" {
					element = v.Name.Local
				}
			case xml.CharData:
				s := string(v)
				if element == "f" {
					formula += s
				} else if element == "v" || element == "t" {
					value += s
				}
			case xml.EndElement:
				if v.Name.Local == element {
					element = ""
				}
				if v.Name.Local == "c" && cell != "" {
					fmt.Fprintf(&out, "%s", cell)
					if strings.TrimSpace(formula) != "" {
						fmt.Fprintf(&out, " formula=%s", strings.TrimSpace(formula))
					}
					if strings.TrimSpace(value) != "" {
						fmt.Fprintf(&out, " cached=%s", strings.TrimSpace(value))
					}
					out.WriteByte('\n')
				}
			}
		}
	case "docx":
		fmt.Fprintf(&out, "[document %s]\n", label)
		inParagraph, inText := false, false
		style := ""
		var paragraph strings.Builder
		for {
			tok, e := d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return "", fmt.Errorf("invalid Office XML: %w", e)
			}
			switch v := tok.(type) {
			case xml.StartElement:
				if v.Name.Local == "p" {
					inParagraph = true
					style = ""
					paragraph.Reset()
				}
				if inParagraph && v.Name.Local == "pStyle" {
					for _, a := range v.Attr {
						if a.Name.Local == "val" {
							style = a.Value
						}
					}
				}
				if inParagraph && v.Name.Local == "t" {
					inText = true
				}
			case xml.CharData:
				if inText {
					paragraph.Write(v)
				}
			case xml.EndElement:
				if v.Name.Local == "t" {
					inText = false
				}
				if v.Name.Local == "p" && inParagraph {
					text := strings.TrimSpace(paragraph.String())
					if text != "" {
						if style != "" {
							fmt.Fprintf(&out, "[%s] ", style)
						}
						out.WriteString(text)
						out.WriteByte('\n')
					}
					inParagraph = false
				}
			}
		}
	default:
		n := regexp.MustCompile(`\d+`).FindString(label)
		if n == "" {
			n = label
		}
		fmt.Fprintf(&out, "[slide %s]\n", n)
		inText := false
		for {
			tok, e := d.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return "", fmt.Errorf("invalid Office XML: %w", e)
			}
			switch v := tok.(type) {
			case xml.StartElement:
				if v.Name.Local == "t" {
					inText = true
				}
			case xml.CharData:
				if inText {
					s := strings.TrimSpace(string(v))
					if s != "" {
						out.WriteString(s)
						out.WriteByte('\n')
					}
				}
			case xml.EndElement:
				if v.Name.Local == "t" {
					inText = false
				}
			}
		}
	}
	return out.String(), nil
}

func ceilDiv(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}
func roundedTokens(v float64) int64 { return int64(math.Ceil(v)) }
