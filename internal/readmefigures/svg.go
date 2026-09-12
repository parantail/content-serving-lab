package readmefigures

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"strings"
	"unicode"
)

// Chart chrome shared by every README figure. Data marks use three fixed
// categorical hues: orange for the state before an intervention, blue for the
// adopted intervention and green for a context stream. Text never carries a
// series colour; a swatch beside the text does.
const (
	colorBefore  = "#eb6834"
	colorAfter   = "#2a78d6"
	colorContext = "#199e70"
	colorRange   = "#b7d3f6"
	colorBand    = "#f3f2ee"
	inkPrimary   = "#0b0b0b"
	inkSecondary = "#52514e"
	inkMuted     = "#898781"
	gridColor    = "#e1e0d9"
	axisColor    = "#c3c2b7"
	surface      = "#ffffff"
	fontStack    = `system-ui,-apple-system,"Segoe UI","Apple SD Gothic Neo","Malgun Gothic","Noto Sans KR",sans-serif`
)

type canvas struct {
	buf    bytes.Buffer
	width  int
	height int
}

func newCanvas(width, height int) *canvas {
	c := &canvas{width: width, height: height}
	fmt.Fprintf(&c.buf, `<?xml version="1.0" encoding="UTF-8"?>`+"\n")
	fmt.Fprintf(&c.buf, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img">`+"\n", width, height, width, height)
	fmt.Fprintf(&c.buf, `<style>text{font-family:%s;fill:%s}.title{font-size:20px;font-weight:700}.subtitle{font-size:13px;fill:%s}.group{font-size:14px;font-weight:600}.label{font-size:13px}.value{font-size:13px;font-weight:600}.small{font-size:11px;fill:%s}.muted{font-size:12px;fill:%s}.axis{stroke:%s;stroke-width:1}.grid{stroke:%s;stroke-width:1}</style>`+"\n", fontStack, inkPrimary, inkSecondary, inkMuted, inkMuted, axisColor, gridColor)
	fmt.Fprintf(&c.buf, `<rect width="100%%" height="100%%" fill="%s"/>`+"\n", surface)
	return c
}

func (c *canvas) bytes() []byte {
	c.buf.WriteString("</svg>\n")
	return c.buf.Bytes()
}

func (c *canvas) text(x, y float64, class, anchor, s string) {
	if anchor == "" {
		anchor = "start"
	}
	fmt.Fprintf(&c.buf, `<text x="%s" y="%s" class="%s" text-anchor="%s">%s</text>`+"\n", num(x), num(y), class, anchor, html.EscapeString(s))
}

func (c *canvas) line(x1, y1, x2, y2 float64, class string) {
	fmt.Fprintf(&c.buf, `<line x1="%s" y1="%s" x2="%s" y2="%s" class="%s"/>`+"\n", num(x1), num(y1), num(x2), num(y2), class)
}

func (c *canvas) rect(x, y, w, h float64, fill string, radius float64) {
	fmt.Fprintf(&c.buf, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`+"\n", num(x), num(y), num(w), num(h), num(radius), fill)
}

// hbar draws a horizontal bar that is square at the baseline (left) and rounded
// at the data end (right). Bars thinner than the radius fall back to a rectangle.
func (c *canvas) hbar(x, y, w, h float64, fill string) {
	const radius = 4.0
	if w <= radius {
		c.rect(x, y, w, h, fill, 0)
		return
	}
	fmt.Fprintf(&c.buf, `<path d="M%s,%s h%s a%s,%s 0 0 1 %s,%s v%s a%s,%s 0 0 1 -%s,%s h-%s z" fill="%s"/>`+"\n",
		num(x), num(y), num(w-radius), num(radius), num(radius), num(radius), num(radius), num(h-2*radius), num(radius), num(radius), num(radius), num(radius), num(w-radius), fill)
}

func (c *canvas) dot(x, y, r float64, fill string) {
	// A 2px surface ring keeps overlapping markers legible.
	fmt.Fprintf(&c.buf, `<circle cx="%s" cy="%s" r="%s" fill="%s" stroke="%s" stroke-width="2"/>`+"\n", num(x), num(y), num(r), fill, surface)
}

func (c *canvas) polyline(points [][2]float64, stroke string) {
	var b strings.Builder
	for i, p := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(num(p[0]))
		b.WriteByte(',')
		b.WriteString(num(p[1]))
	}
	fmt.Fprintf(&c.buf, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`+"\n", b.String(), stroke)
}

// swatchText writes a coloured key square followed by ink-coloured text and
// returns the x position after the text.
func (c *canvas) swatchText(x, y float64, fill, class, s string) float64 {
	c.rect(x, y-10, 12, 12, fill, 2)
	c.text(x+18, y, class, "start", s)
	return x + 18 + textWidth(s, classSize(class)) + 28
}

func classSize(class string) float64 {
	switch class {
	case "title":
		return 20
	case "group":
		return 14
	case "small":
		return 11
	case "muted":
		return 12
	default:
		return 13
	}
}

// textWidth estimates rendered width without font metrics: Hangul and Han
// glyphs are treated as one em, other characters as 0.58 em. It is used only
// to keep labels clear of marks, so a conservative estimate is enough.
func textWidth(s string, size float64) float64 {
	width := 0.0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Han, r) || r == '·':
			width += size
		case r == ' ' || r == '.' || r == ',' || r == 'l' || r == 'i':
			width += 0.3 * size
		default:
			width += 0.58 * size
		}
	}
	return width
}

func num(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}

func formatThousands(v int64) string {
	s := fmt.Sprintf("%d", v)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

func seconds(ms float64) string {
	switch {
	case ms == 0:
		return "0초"
	case ms < 1000:
		return fmt.Sprintf("%.2f초", ms/1000)
	case math.Mod(ms, 1000) == 0:
		return fmt.Sprintf("%.0f초", ms/1000)
	default:
		return fmt.Sprintf("%.1f초", ms/1000)
	}
}

func mib(bytes float64) string {
	return formatThousands(int64(bytes/1024/1024+0.5)) + " MiB"
}

func vcpu(cpuQuota string, memoryBytes int64) string {
	// cgroup quota "100000 100000" is one full CPU period.
	fields := strings.Fields(cpuQuota)
	cpus := "?"
	if len(fields) == 2 {
		var quota, period float64
		if _, err := fmt.Sscanf(cpuQuota, "%f %f", &quota, &period); err == nil && period > 0 {
			cpus = num(quota / period)
		}
	}
	return fmt.Sprintf("%s vCPU·%s GiB", cpus, num(float64(memoryBytes)/1024/1024/1024))
}
