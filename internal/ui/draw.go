package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"rsc.io/qr"
)

// label renders wrapping text.
func (u *UI) label(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
	l := material.Label(u.mth, u.mth.TextSize, s)
	l.Color = c
	l.WrapPolicy = text.WrapHeuristically
	return l.Layout(gtx)
}

// line renders a single, truncated line of text.
func (u *UI) line(gtx layout.Context, s string, c color.NRGBA) layout.Dimensions {
	return u.lineAligned(gtx, s, c, text.Start)
}

func (u *UI) lineAligned(gtx layout.Context, s string, c color.NRGBA, align text.Alignment) layout.Dimensions {
	l := material.Label(u.mth, u.mth.TextSize, s)
	l.Color = c
	l.MaxLines = 1
	l.Truncator = "…"
	l.Alignment = align
	return l.Layout(gtx)
}

// cols returns a widget of exactly n character cells wide.
func (u *UI) cols(n int, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		width := min(n*u.charW, gtx.Constraints.Max.X)
		gtx.Constraints.Min.X, gtx.Constraints.Max.X = width, width
		d := w(gtx)
		d.Size.X = width
		return d
	}
}

func fill(gtx layout.Context, c color.NRGBA) layout.Dimensions {
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: gtx.Constraints.Min}.Op())
	return layout.Dimensions{Size: gtx.Constraints.Min}
}

// withBg draws w over a solid background.
func withBg(gtx layout.Context, c color.NRGBA, w layout.Widget) layout.Dimensions {
	return layout.Background{}.Layout(gtx, func(gtx layout.Context) layout.Dimensions { return fill(gtx, c) }, w)
}

func hrule(c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		size := image.Pt(gtx.Constraints.Max.X, max(1, gtx.Dp(1)))
		paint.FillShape(gtx.Ops, c, clip.Rect{Max: size}.Op())
		return layout.Dimensions{Size: size}
	}
}

func vrule(c color.NRGBA) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		size := image.Pt(max(1, gtx.Dp(1)), gtx.Constraints.Max.Y)
		paint.FillShape(gtx.Ops, c, clip.Rect{Max: size}.Op())
		return layout.Dimensions{Size: size}
	}
}

// pad insets by a number of character cells horizontally and lines vertically.
func (u *UI) pad(h, v float32, w layout.Widget) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		x := unit.Dp(float32(u.charW) * h / gtx.Metric.PxPerDp)
		y := unit.Dp(float32(u.lineH) * v / gtx.Metric.PxPerDp)
		return layout.Inset{Left: x, Right: x, Top: y, Bottom: y}.Layout(gtx, w)
	}
}

func luminance(c color.NRGBA) float32 {
	return 0.2126*float32(c.R) + 0.7152*float32(c.G) + 0.0722*float32(c.B)
}

// drawQR renders url as a QR code: light quiet zone, dark modules.
func (u *UI) drawQR(gtx layout.Context, url string, light, dark color.NRGBA) layout.Dimensions {
	if u.qrURL != url {
		code, err := qr.Encode(url, qr.M)
		if err != nil {
			return layout.Dimensions{}
		}
		u.qrURL, u.qrCode = url, code
	}
	if luminance(dark) > luminance(light) {
		light, dark = dark, light
	}
	const quiet = 3
	n := u.qrCode.Size
	total := n + 2*quiet
	m := max(2, min(gtx.Dp(300), gtx.Constraints.Max.X, gtx.Constraints.Max.Y)/total)
	side := m * total

	paint.FillShape(gtx.Ops, light, clip.Rect{Max: image.Pt(side, side)}.Op())
	var p clip.Path
	p.Begin(gtx.Ops)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !u.qrCode.Black(x, y) {
				continue
			}
			x0, y0 := float32((x+quiet)*m), float32((y+quiet)*m)
			s := float32(m)
			p.MoveTo(f32.Pt(x0, y0))
			p.LineTo(f32.Pt(x0+s, y0))
			p.LineTo(f32.Pt(x0+s, y0+s))
			p.LineTo(f32.Pt(x0, y0+s))
			p.Close()
		}
	}
	paint.FillShape(gtx.Ops, dark, clip.Outline{Path: p.End()}.Op())
	return layout.Dimensions{Size: image.Pt(side, side)}
}

// measure caches the size of one monospace cell.
func (u *UI) measure(gtx layout.Context) {
	if u.charW != 0 && u.measuredAt == u.mth.TextSize {
		return
	}
	rec := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
	d := material.Label(u.mth, u.mth.TextSize, "MMMMMMMMMM").Layout(g)
	rec.Stop()
	u.charW = max(1, (d.Size.X+9)/10)
	u.lineH = max(1, d.Size.Y)
	u.measuredAt = u.mth.TextSize
}

// emoji renders a single emoji, preferring the color emoji font over the
// monochrome glyphs many terminal fonts ship.
func (u *UI) emoji(gtx layout.Context, e string) layout.Dimensions {
	l := material.Label(u.mth, u.mth.TextSize, e)
	l.Font.Typeface = "Noto Color Emoji, " + u.mth.Face
	l.MaxLines = 1
	return l.Layout(gtx)
}
