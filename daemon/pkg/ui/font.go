package ui

import (
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/mdi.ttf assets/mdi.css
var assets embed.FS

var (
	iconMap = make(map[string]string)
	mdiFont *sfnt.Font
	txtFont *sfnt.Font

	// Font face caches — created once, reused forever
	mdiFaceCache = make(map[float64]font.Face)
	txtFaceCache = make(map[float64]font.Face)
)

func Init() error {
	// Load Icon Font
	fontBytes, err := assets.ReadFile("assets/mdi.ttf")
	if err != nil {
		return fmt.Errorf("read mdi: %v", err)
	}
	mdiFont, err = sfnt.Parse(fontBytes)
	if err != nil {
		return fmt.Errorf("parse mdi: %v", err)
	}

	// Load Text Font (Go Regular)
	txtFontBytes, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		return fmt.Errorf("parse text: %v", err)
	}
	txtFont = txtFontBytes

	// Parse CSS
	cssBytes, err := assets.ReadFile("assets/mdi.css")
	if err != nil {
		return fmt.Errorf("read css: %v", err)
	}
	parseCSS(string(cssBytes))

	// Pre-populate font face caches for common sizes
	for _, sz := range []float64{16, 18, 20, 22, 24, 28, 32, 36, 42, 44, 48, 64, 72, 80} {
		getMdiFace(sz)
		getTxtFace(sz)
	}

	fmt.Printf("UI Init: Loaded %d icons\n", len(iconMap))
	return nil
}

// getMdiFace returns a cached font.Face for the MDI icon font at the given size.
func getMdiFace(size float64) font.Face {
	if f, ok := mdiFaceCache[size]; ok {
		return f
	}
	f, err := opentype.NewFace(mdiFont, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil
	}
	mdiFaceCache[size] = f
	return f
}

// getTxtFace returns a cached font.Face for the text font at the given size.
func getTxtFace(size float64) font.Face {
	if f, ok := txtFaceCache[size]; ok {
		return f
	}
	f, err := opentype.NewFace(txtFont, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	txtFaceCache[size] = f
	return f
}

func parseCSS(css string) {
	// Simple manual parsing because Regex can be brittle with newlines
	// Search for ".mdi-"

	lines := strings.Split(css, "\n")
	var currentName string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, ".mdi-") && strings.Contains(line, "::before") {
			// .mdi-lightbulb::before {
			parts := strings.Split(line, "::before")
			if len(parts) > 0 {
				currentName = strings.TrimPrefix(parts[0], ".mdi-")
			}
		} else if strings.HasPrefix(line, "content:") && currentName != "" {
			// content: "\F0335";
			// Extract Hex inside quotes
			start := strings.Index(line, "\"\\")
			if start != -1 {
				hexCode := line[start+2 : start+2+5] // Assume 4-5 chars? usually 4 (F0335 is 5?)
				// Wait, unicode escape \F0335 is 6 chars?
				// content: "\F0335";
				// Let's find end quote
				end := strings.LastIndex(line, "\"")
				if end > start+2 {
					hexCode = line[start+2 : end]
					var r rune
					fmt.Sscanf(hexCode, "%x", &r)
					iconMap[currentName] = string(r)
					// fmt.Printf("Loaded: %s -> %x\n", currentName, r)
				}
			}
			currentName = "" // Reset
		}
	}
}

func GetIconList() []string {
	var list []string
	for k := range iconMap {
		list = append(list, k)
	}
	return list
}

func DrawIcon(dst draw.Image, x, y int, size float64, iconName string, col color.Color) {
	if mdiFont == nil {
		return
	}
	code, ok := iconMap[iconName]
	if !ok {
		return
	}

	face := getMdiFace(size)
	if face == nil {
		return
	}

	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(x-int(size/2), y+int(size/2.5)),
	}

	d.DrawString(code)
}

func DrawText(dst draw.Image, x, y int, size float64, text string, col color.Color) {
	if txtFont == nil {
		return
	}

	face := getTxtFace(size)
	if face == nil {
		return
	}

	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
	}

	// Measure width to center
	width := d.MeasureString(text).Ceil()

	d.Dot = fixed.P(x-width/2, y+int(size/2.5))
	d.DrawString(text)
}

// MeasureTextWidth returns the width of text in pixels at the given size.
func MeasureTextWidth(text string, size float64) int {
	if txtFont == nil {
		return 0
	}

	face := getTxtFace(size)
	if face == nil {
		return 0
	}

	d := &font.Drawer{
		Face: face,
	}

	return d.MeasureString(text).Ceil()
}

// DrawTextLeft draws text left-aligned (not centered).
func DrawTextLeft(dst draw.Image, x, y int, size float64, text string, col color.Color) {
	if txtFont == nil {
		return
	}

	face := getTxtFace(size)
	if face == nil {
		return
	}

	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(x, y+int(size/2.5)),
	}

	d.DrawString(text)
}

// DrawTextScrolling draws text with horizontal scrolling if it exceeds maxWidth.
// offset is the scroll position in pixels (negative values scroll left).
func DrawTextScrolling(dst draw.Image, x, y int, size float64, text string, col color.Color, maxWidth int, offset int) {
	if txtFont == nil {
		return
	}

	face := getTxtFace(size)
	if face == nil {
		return
	}

	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
	}

	width := d.MeasureString(text).Ceil()

	// If text fits, center it
	if width <= maxWidth {
		d.Dot = fixed.P(x+(maxWidth-width)/2, y+int(size/2.5))
		d.DrawString(text)
		return
	}

	// Text is too long, apply scrolling
	// Create a clipping rectangle
	clipRect := image.Rect(x, y-int(size), x+maxWidth, y+int(size))
	clippedDst := &ClippedImage{
		Image: dst,
		Clip:  clipRect,
	}

	d.Dst = clippedDst
	d.Dot = fixed.P(x-offset, y+int(size/2.5))
	d.DrawString(text)
}

// ClippedImage is a draw.Image that clips all drawing operations to a rectangle.
type ClippedImage struct {
	Image draw.Image
	Clip  image.Rectangle
}

func (c *ClippedImage) ColorModel() color.Model {
	return c.Image.ColorModel()
}

func (c *ClippedImage) Bounds() image.Rectangle {
	return c.Clip
}

func (c *ClippedImage) At(x, y int) color.Color {
	if image.Pt(x, y).In(c.Clip) {
		return c.Image.At(x, y)
	}
	return color.Transparent
}

func (c *ClippedImage) Set(x, y int, col color.Color) {
	if image.Pt(x, y).In(c.Clip) {
		c.Image.Set(x, y, col)
	}
}

// WrapText splits text into lines that fit within maxWidth at the given font size.
// It breaks on word boundaries (spaces), falling back to character-level breaks
// if a single word exceeds maxWidth.
func WrapText(text string, size float64, maxWidth int) []string {
	if txtFont == nil || maxWidth <= 0 {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	currentLine := words[0]

	for _, word := range words[1:] {
		candidate := currentLine + " " + word
		if MeasureTextWidth(candidate, size) <= maxWidth {
			currentLine = candidate
		} else {
			lines = append(lines, currentLine)
			currentLine = word
			// If a single word exceeds maxWidth, it stays on its own line
		}
	}
	lines = append(lines, currentLine)
	return lines
}

// DrawTextLeftClipped draws left-aligned text clipped to a rectangle.
func DrawTextLeftClipped(dst draw.Image, x, y int, size float64, text string, col color.Color, clipRect image.Rectangle) {
	if txtFont == nil {
		return
	}

	face := getTxtFace(size)
	if face == nil {
		return
	}

	clippedDst := &ClippedImage{
		Image: dst,
		Clip:  clipRect,
	}

	d := &font.Drawer{
		Dst:  clippedDst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(x, y+int(size/2.5)),
	}

	d.DrawString(text)
}
