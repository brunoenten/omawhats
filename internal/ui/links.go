package ui

import (
	"image"
	"image/color"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf8"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"
)

// Matches http(s) and www. URLs; trailing punctuation is trimmed after the match.
var linkRE = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"'()\[\]{}]+`)

type linkSpan struct {
	text string
	url  string // empty for plain text
}

func linkSpans(s string) []linkSpan {
	if s == "" {
		return nil
	}
	matches := linkRE.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return []linkSpan{{text: s}}
	}
	var out []linkSpan
	prev := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		raw := s[start:end]
		url, trim := trimLink(raw)
		end -= trim
		if end <= start {
			continue
		}
		if start > prev {
			out = append(out, linkSpan{text: s[prev:start]})
		}
		out = append(out, linkSpan{text: s[start:end], url: url})
		prev = end
	}
	if prev < len(s) {
		out = append(out, linkSpan{text: s[prev:]})
	}
	if len(out) == 0 {
		return []linkSpan{{text: s}}
	}
	return out
}

func trimLink(raw string) (url string, trimmed int) {
	url = raw
	for len(url) > 0 {
		r, size := utf8.DecodeLastRuneInString(url)
		if !strings.ContainsRune(".,;:!?)]}>\"'", r) {
			break
		}
		url = url[:len(url)-size]
		trimmed += size
	}
	if strings.HasPrefix(strings.ToLower(url), "www.") {
		url = "https://" + url
	}
	return url, trimmed
}

func openURL(raw string, status func(string)) {
	u := raw
	if strings.HasPrefix(strings.ToLower(u), "www.") {
		u = "https://" + u
	}
	low := strings.ToLower(u)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return
	}
	go func() {
		cmd := exec.Command("xdg-open", u)
		if err := cmd.Start(); err != nil {
			status("xdg-open: " + err.Error())
			return
		}
		go cmd.Wait()
	}()
}

// linkLabel renders wrapping text with clickable http(s)/www links.
func (u *UI) linkLabel(gtx layout.Context, key, s string, c color.NRGBA) layout.Dimensions {
	spans := linkSpans(s)
	if len(spans) == 1 && spans[0].url == "" {
		return u.label(gtx, s, c)
	}
	return u.flowLinks(gtx, key, spans, c)
}

type flowFrag struct {
	text string
	url  string
	idx  int
}

func (u *UI) flowLinks(gtx layout.Context, key string, spans []linkSpan, c color.NRGBA) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	if maxW <= 0 || u.charW <= 0 {
		return layout.Dimensions{}
	}

	var frags []flowFrag
	for i, sp := range spans {
		if sp.url != "" {
			frags = append(frags, splitWide(sp.text, sp.url, i, maxW/u.charW)...)
			continue
		}
		for _, tok := range tokenize(sp.text) {
			frags = append(frags, flowFrag{text: tok, idx: i})
		}
	}

	type item struct {
		flowFrag
		w int
	}
	var lines [][]item
	var cur []item
	curW := 0
	flush := func() {
		lines = append(lines, cur) // keep empty lines from explicit \n
		cur, curW = nil, 0
	}
	for _, f := range frags {
		if f.url == "" && f.text == "\n" {
			flush()
			continue
		}
		w := utf8.RuneCountInString(f.text) * u.charW
		space := f.url == "" && strings.TrimSpace(f.text) == ""
		if space && curW == 0 {
			continue // drop leading wrap whitespace
		}
		if curW > 0 && curW+w > maxW {
			flush()
			if space {
				continue
			}
		}
		// Hard-break a leftover overlong fragment.
		for w > maxW && f.url == "" {
			n := maxW / u.charW
			if n < 1 {
				n = 1
			}
			head, rest := splitRunes(f.text, n)
			cur = append(cur, item{flowFrag{head, f.url, f.idx}, n * u.charW})
			flush()
			f.text = rest
			w = utf8.RuneCountInString(f.text) * u.charW
		}
		if f.text == "" {
			continue
		}
		cur = append(cur, item{f, w})
		curW += w
	}
	if len(cur) > 0 || len(lines) == 0 {
		flush()
	}

	linkC := u.th.Cyan
	children := make([]layout.FlexChild, 0, len(lines))
	for _, line := range lines {
		line := line
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if len(line) == 0 {
				return layout.Dimensions{Size: image.Pt(0, u.lineH)}
			}
			gtx.Constraints.Min.X = 0
			var parts []layout.FlexChild
			for _, it := range line {
				it := it
				parts = append(parts, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					if it.url == "" {
						return u.line(gtx, it.text, c)
					}
					ck := key + "\x00" + it.url + "\x00" + it.text
					click := u.linkClicks[ck]
					if click == nil {
						click = new(widget.Clickable)
						u.linkClicks[ck] = click
					}
					if click.Clicked(gtx) {
						openURL(it.url, u.store.SetStatus)
					}
					return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						dims := u.line(gtx, it.text, linkC)
						y := dims.Size.Y - max(1, gtx.Dp(1))
						paint.FillShape(gtx.Ops, linkC, clip.Rect{
							Min: image.Pt(0, y),
							Max: image.Pt(dims.Size.X, dims.Size.Y),
						}.Op())
						return dims
					})
				}))
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, parts...)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	// 0 = word, 1 = spaces/tabs, 2 = newline (emitted as its own token)
	classOf := func(r rune) int {
		switch r {
		case '\n':
			return 2
		case ' ', '\t':
			return 1
		default:
			return 0
		}
	}
	r0, _ := utf8.DecodeRuneInString(s)
	class := classOf(r0)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		c := classOf(r)
		if c == 2 {
			if i > start {
				out = append(out, s[start:i])
			}
			out = append(out, "\n")
			i += size
			start = i
			if i < len(s) {
				r2, _ := utf8.DecodeRuneInString(s[i:])
				class = classOf(r2)
			}
			continue
		}
		if c != class && i > start {
			out = append(out, s[start:i])
			start = i
			class = c
		}
		i += size
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func splitWide(text, url string, idx, maxRunes int) []flowFrag {
	if maxRunes < 1 {
		maxRunes = 1
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return []flowFrag{{text: text, url: url, idx: idx}}
	}
	var out []flowFrag
	for text != "" {
		head, rest := splitRunes(text, maxRunes)
		out = append(out, flowFrag{text: head, url: url, idx: idx})
		text = rest
	}
	return out
}

func splitRunes(s string, n int) (head, rest string) {
	if n <= 0 {
		return "", s
	}
	i := 0
	for n > 0 && i < len(s) {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n--
	}
	return s[:i], s[i:]
}
