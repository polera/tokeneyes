package tokeneyes

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectorInspectsMixedMediaAndExtractsOfficeText(t *testing.T) {
	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 385, 769))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shot.png"), pngBytes.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	wav := fixtureWAV(8000, 1, 8000)
	if err := os.WriteFile(filepath.Join(root, "sound.wav"), wav, 0o600); err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n1 0 obj << /Type /Page /MediaBox [0 0 612 792] >> stream\nBT (hello pdf) Tj ET\nendstream\nendobj\n%%EOF")
	if err := os.WriteFile(filepath.Join(root, "report.pdf"), pdf, 0o600); err != nil {
		t.Fatal(err)
	}
	docx := fixtureOffice("word/document.xml", `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Heading</w:t></w:r></w:p></w:body></w:document>`)
	if err := os.WriteFile(filepath.Join(root, "notes.docx"), docx, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewFileCollector().Collect(context.Background(), CollectRequest{Root: root, Paths: []string{"shot.png", "sound.wav", "report.pdf", "notes.docx"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Assets) != 4 || len(c.Sources) != 4 {
		t.Fatalf("assets=%d sources=%d warnings=%v", len(c.Assets), len(c.Sources), c.Warnings)
	}
	var sawImage, sawAudio, sawPDF, sawOffice bool
	textFor := func(id string) string {
		for _, s := range c.Sources {
			if s.AssetID == id {
				return string(s.ExtractedText)
			}
		}
		return ""
	}
	for _, a := range c.Assets {
		switch a.SourceKind {
		case "image":
			sawImage = a.Image.Width == 385 && a.Image.Height == 769
		case "audio":
			sawAudio = a.Audio.DurationMillis == 1000 && a.Audio.SampleRateHz == 8000
		case "document":
			if a.Document.Format == "pdf" {
				sawPDF = a.Document.Pages == 1 && strings.Contains(textFor(a.ID), "hello pdf")
			} else {
				sawOffice = strings.Contains(textFor(a.ID), "Heading")
			}
		}
	}
	if !sawImage || !sawAudio || !sawPDF || !sawOffice {
		t.Fatalf("inspection failed: %+v", c.Assets)
	}
}

func TestPublishedImageAndAudioFormulaBoundaries(t *testing.T) {
	g, _ := DefaultCatalog().Resolve("gemini")
	c, _, err := countImageRule(*g.Media.Image, 384, 384, "auto")
	if err != nil || c.Expected != 258 {
		t.Fatalf("small=%+v err=%v", c, err)
	}
	c, _, err = countImageRule(*g.Media.Image, 769, 769, "auto")
	if err != nil || c.Expected != 1032 {
		t.Fatalf("tiles=%+v err=%v", c, err)
	}
	a, _ := DefaultCatalog().Resolve("claude-opus-4-8")
	c, _, err = countImageRule(*a.Media.Image, 28, 29, "high")
	if err != nil || c.Expected != 2 {
		t.Fatalf("patch boundary=%+v err=%v", c, err)
	}
	if roundedTokens(g.Media.Audio.TokensPerSecond*1.5) != 48 {
		t.Fatal("audio duration formula is not 32 tokens/sec")
	}
}

func TestOfficeSerializationPreservesStructureAndFormula(t *testing.T) {
	doc, err := serializeOfficeXML("docx", "document.xml", []byte(`<w:document xmlns:w="w"><w:body><w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Title</w:t></w:r></w:p></w:body></w:document>`))
	if err != nil || !strings.Contains(doc, "[Heading1] Title") {
		t.Fatalf("doc=%q err=%v", doc, err)
	}
	sheet, err := serializeOfficeXML("xlsx", "sheet1.xml", []byte(`<worksheet><sheetData><row><c r="A1"><f>SUM(B1:B2)</f><v>3</v></c></row></sheetData></worksheet>`))
	if err != nil || !strings.Contains(sheet, "A1 formula=SUM(B1:B2) cached=3") {
		t.Fatalf("sheet=%q err=%v", sheet, err)
	}
}

func TestOrderedOverrideGlobSupportsDoubleStar(t *testing.T) {
	matched, err := matchOverrideGlob("archive/**/*.pdf", "archive/2026/q1/report.pdf")
	if err != nil || !matched {
		t.Fatalf("matched=%t err=%v", matched, err)
	}
	matched, _ = matchOverrideGlob("archive/**/*.pdf", "other/report.pdf")
	if matched {
		t.Fatal("glob matched unrelated path")
	}
}

func TestMixedEngineMarksUnsupportedAudioWithoutZeroComponent(t *testing.T) {
	wav := fixtureWAV(8000, 1, 8000)
	sha := "0123456789abcdef"
	s := Source{Label: "meeting.wav", Kind: "audio", AssetID: "asset-1", DetectedMIME: "audio/wav", SHA256: sha, Bytes: int64(len(wav)), Content: wav}
	e := NewEngine(DefaultCatalog())
	run, err := e.Analyze(context.Background(), AnalyzeRequest{Collection: Collection{Sources: []Source{s}}, Models: []string{"gpt-5.5", "gemini"}, OutputTokens: []int64{0}, Processing: "native"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range run.Results {
		if r.Model == "gpt-5.5" {
			if r.CapabilityStatus != "unsupported" || len(r.CountComponents) != 0 {
				t.Fatalf("OpenAI audio=%+v", r)
			}
		} else {
			if r.CapabilityStatus != "supported" || len(r.CountComponents) != 1 || r.CountComponents[0].Expected != 32 {
				t.Fatalf("Gemini audio=%+v", r)
			}
		}
	}
	if !run.Incomplete {
		t.Fatal("unsupported comparison did not mark run incomplete")
	}
}

func fixtureWAV(rate, channels, samples int) []byte {
	data := make([]byte, samples*channels*2)
	b := make([]byte, 44+len(data))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:32], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(b[32:34], uint16(channels*2))
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:44], uint32(len(data)))
	copy(b[44:], data)
	return b
}
func fixtureOffice(name, xml string) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create(name)
	_, _ = w.Write([]byte(xml))
	_ = z.Close()
	return b.Bytes()
}
