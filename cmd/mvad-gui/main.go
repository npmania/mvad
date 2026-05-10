package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/npmania/mvad/internal/mullvad"
	"github.com/npmania/mvad/internal/status"
)

type palette struct {
	bg, fg, muted, dim, accent, errFg color.NRGBA
}

var (
	lightPalette = palette{
		bg:     color.NRGBA{0xFA, 0xFA, 0xF7, 0xFF},
		fg:     color.NRGBA{0x1A, 0x1A, 0x1A, 0xFF},
		muted:  color.NRGBA{0x6B, 0x6B, 0x6B, 0xFF},
		dim:    color.NRGBA{0xE2, 0xE2, 0xDF, 0xFF},
		accent: color.NRGBA{0x2E, 0x7D, 0x5B, 0xFF},
		errFg:  color.NRGBA{0xC0, 0x20, 0x20, 0xFF},
	}
	darkPalette = palette{
		bg:     color.NRGBA{0x14, 0x14, 0x16, 0xFF},
		fg:     color.NRGBA{0xED, 0xED, 0xE8, 0xFF},
		muted:  color.NRGBA{0x8A, 0x8A, 0x88, 0xFF},
		dim:    color.NRGBA{0x2A, 0x2A, 0x2C, 0xFF},
		accent: color.NRGBA{0x5D, 0xBF, 0x8E, 0xFF},
		errFg:  color.NRGBA{0xE0, 0x6A, 0x6A, 0xFF},
	}
)

type state struct {
	snap      status.JSONOut
	pollErr   error
	cmdOut    string
	cmdErr    error
	cmdName   string
	running   bool
	loadedAny bool
	dark      bool

	filter       widget.Editor
	relayList    widget.List
	relays       []mullvad.Relay
	expanded     map[string]bool
	selected     string
	relayLoadErr string
	relayLoading bool

	btn     widget.Clickable
	refresh widget.Clickable
	toggle  widget.Clickable

	headerClicks map[string]*widget.Clickable
	rowClicks    map[string]*widget.Clickable
}

type row struct {
	country string
	relay   *mullvad.Relay
}

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("mvad"), app.Size(unit.Dp(420), unit.Dp(540)))
		if err := run(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

	var st state
	st.filter.SingleLine = true
	st.relayList.Axis = layout.Vertical
	st.expanded = map[string]bool{}
	st.headerClicks = map[string]*widget.Clickable{}
	st.rowClicks = map[string]*widget.Clickable{}

	snaps := make(chan status.JSONOut, 1)
	pollErrs := make(chan error, 1)
	cmdDone := make(chan cmdResult, 1)
	relayDone := make(chan relayLoadResult, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollStatus(ctx, w, snaps, pollErrs)

	st.relayLoading = true
	go loadRelays(ctx, w, false, relayDone)

	var ops op.Ops
	for {
		select {
		case s := <-snaps:
			st.snap = s
			st.pollErr = nil
			st.loadedAny = true
		case err := <-pollErrs:
			st.pollErr = err
			st.loadedAny = true
		case r := <-cmdDone:
			st.running = false
			st.cmdName = r.name
			st.cmdOut = r.out
			st.cmdErr = r.err
		case r := <-relayDone:
			st.relayLoading = false
			if r.err != nil {
				st.relayLoadErr = r.err.Error()
			} else {
				st.relayLoadErr = ""
				st.relays = r.relays
			}
		default:
		}

		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)

			if st.toggle.Clicked(gtx) {
				st.dark = !st.dark
			}
			if !st.relayLoading && st.refresh.Clicked(gtx) {
				st.relayLoading = true
				go loadRelays(ctx, w, true, relayDone)
			}
			if !st.running && st.btn.Clicked(gtx) {
				if st.snap.Connected {
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					go runCmd(ctx, w, cmdDone, "disconnect")
				} else if st.selected != "" {
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					go runCmd(ctx, w, cmdDone, "connect", "--", st.selected)
				}
			}

			layoutUI(gtx, th, &st)
			e.Frame(gtx.Ops)
		}
	}
}

func layoutUI(gtx layout.Context, th *material.Theme, st *state) {
	pal := lightPalette
	if st.dark {
		pal = darkPalette
	}
	paint.FillShape(gtx.Ops, pal.bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	th.Bg, th.Fg = pal.bg, pal.fg
	th.ContrastBg, th.ContrastFg = pal.fg, pal.bg

	if st.snap.Connected {
		if st.selected != "" || len(st.expanded) > 0 || st.filter.Text() != "" {
			st.selected = ""
			st.expanded = map[string]bool{}
			st.filter.SetText("")
		}
	}

	layout.Stack{}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return contentBody(gtx, th, st, pal)
			})
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return layout.NE.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return drawToggle(gtx, st, pal)
				})
			})
		}),
	)
}

func contentBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	if st.snap.Connected {
		return connectedBody(gtx, th, st, pal)
	}
	return disconnectedBody(gtx, th, st, pal)
}

func connectedBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	word, wordColor := headline(st, pal)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(24), word)
			lbl.Color = wordColor
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lines := subLines(st)
			if len(lines) == 0 {
				return layout.Dimensions{}
			}
			children := make([]layout.FlexChild, 0, len(lines))
			for _, line := range lines {
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(13), line)
					lbl.Color = pal.muted
					return lbl.Layout(gtx)
				}))
			}
			return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(28)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return actionButton(gtx, th, st, pal, "Disconnect", false)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return footer(gtx, th, st, pal)
		}),
	)
}

func disconnectedBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	word, wordColor := headline(st, pal)
	filterText := st.filter.Text()
	if st.selected != "" && filterText != "" {
		f := strings.ToLower(strings.TrimSpace(filterText))
		keep := false
		for i := range st.relays {
			if st.relays[i].Hostname == st.selected && relayMatches(&st.relays[i], f) {
				keep = true
				break
			}
		}
		if !keep {
			st.selected = ""
		}
	}
	rows := buildRows(st.relays, st.expanded, filterText)
	btnDisabled := st.selected == "" || st.running

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(24), word)
			lbl.Color = wordColor
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return filterRow(gtx, th, st, pal)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return relayListArea(gtx, th, st, pal, rows)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(28)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return actionButton(gtx, th, st, pal, "Connect", btnDisabled)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return footer(gtx, th, st, pal)
		}),
	)
}

func headline(st *state, pal palette) (string, color.NRGBA) {
	if !st.loadedAny {
		return "Loading…", pal.muted
	}
	if st.snap.Connected {
		return "Connected", pal.accent
	}
	return "Disconnected", pal.fg
}

func subLines(st *state) []string {
	s := st.snap
	if !s.Connected {
		return nil
	}
	var out []string
	name := s.Relay
	if name == "" {
		name = s.Endpoint
	}
	if name != "" {
		if s.Entry != "" {
			out = append(out, name+" via "+s.Entry)
		} else {
			out = append(out, name)
		}
	}
	if s.TxBytes != 0 || s.RxBytes != 0 {
		out = append(out, fmt.Sprintf("%s ↑ / %s ↓", status.HumanBytes(s.TxBytes), status.HumanBytes(s.RxBytes)))
	}
	if hs, ok := parseTime(s.LastHandshake); ok {
		out = append(out, "last handshake "+status.HumanDuration(time.Since(hs))+" ago")
	}
	if exp, ok := parseTime(s.AccountExpiry); ok {
		if time.Until(exp) <= 0 {
			out = append(out, "account expired")
		} else {
			out = append(out, "expires "+status.HumanExpiry(exp))
		}
	}
	if s.DeviceName != "" {
		out = append(out, "device: "+s.DeviceName)
	}
	return out
}

func filterRow(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return filterEditor(gtx, th, &st.filter, pal)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return refreshGlyph(gtx, th, st, pal)
		}),
	)
}

func filterEditor(gtx layout.Context, th *material.Theme, ed *widget.Editor, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), "Filter")
			lbl.Color = pal.muted
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			es := material.Editor(th, ed, "")
			es.Color = pal.fg
			es.HintColor = pal.muted
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return es.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			c := pal.dim
			if gtx.Focused(ed) {
				c = pal.accent
			}
			sz := image.Pt(gtx.Constraints.Max.X, max(gtx.Dp(1), 1))
			paint.FillShape(gtx.Ops, c, clip.Rect{Max: sz}.Op())
			return layout.Dimensions{Size: sz}
		}),
	)
}

func refreshGlyph(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return st.refresh.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		c := pal.fg
		if st.relayLoading {
			c = pal.muted
		}
		sz := gtx.Dp(24)
		gtx.Constraints.Min = image.Pt(sz, sz)
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(14), "↻")
			lbl.Color = c
			return lbl.Layout(gtx)
		})
	})
}

func relayListArea(gtx layout.Context, th *material.Theme, st *state, pal palette, rows []row) layout.Dimensions {
	switch {
	case st.relayLoading && len(st.relays) == 0:
		return centerLabel(gtx, th, pal.muted, "loading relays…")
	case st.relayLoadErr != "":
		return errorPlaceholder(gtx, th, pal, st.relayLoadErr)
	case len(st.relays) == 0:
		return centerLabel(gtx, th, pal.muted, "no relays cached — press ↻ to fetch")
	case len(rows) == 0:
		return centerLabel(gtx, th, pal.muted, fmt.Sprintf("no relays match %q", st.filter.Text()))
	}
	list := material.List(th, &st.relayList)
	return list.Layout(gtx, len(rows), func(gtx layout.Context, i int) layout.Dimensions {
		return rowLayout(gtx, th, st, pal, rows[i])
	})
}

func centerLabel(gtx layout.Context, th *material.Theme, c color.NRGBA, txt string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Label(th, unit.Sp(13), txt)
		lbl.Color = c
		return lbl.Layout(gtx)
	})
}

func errorPlaceholder(gtx layout.Context, th *material.Theme, pal palette, msg string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), "relays: "+firstLine(msg))
				lbl.Color = pal.errFg
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), "press ↻ to retry")
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			}),
		)
	})
}

func rowLayout(gtx layout.Context, th *material.Theme, st *state, pal palette, r row) layout.Dimensions {
	if r.relay == nil {
		return countryHeader(gtx, th, st, pal, r.country)
	}
	return relayRow(gtx, th, st, pal, r.relay)
}

func countryHeader(gtx layout.Context, th *material.Theme, st *state, pal palette, country string) layout.Dimensions {
	c := st.headerClicks[country]
	if c == nil {
		c = &widget.Clickable{}
		st.headerClicks[country] = c
	}
	if c.Clicked(gtx) {
		st.expanded[country] = !st.expanded[country]
	}
	open := st.expanded[country] || st.filter.Text() != ""
	glyph := "▸"
	if open {
		glyph = "▾"
	}
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(14), country)
					lbl.Color = pal.fg
					return lbl.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(14), glyph)
					lbl.Color = pal.muted
					return lbl.Layout(gtx)
				}),
			)
		})
	})
}

func relayRow(gtx layout.Context, th *material.Theme, st *state, pal palette, r *mullvad.Relay) layout.Dimensions {
	rc := st.rowClicks[r.Hostname]
	if rc == nil {
		rc = &widget.Clickable{}
		st.rowClicks[r.Hostname] = rc
	}
	if rc.Clicked(gtx) {
		st.selected = r.Hostname
	}
	selected := st.selected == r.Hostname
	return rc.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Stack{}.Layout(gtx,
			layout.Stacked(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Left: unit.Dp(10), Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(13), r.Hostname)
							lbl.Color = pal.fg
							return lbl.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(12), r.City)
							lbl.Color = pal.muted
							return lbl.Layout(gtx)
						}),
					)
				})
			}),
			layout.Expanded(func(gtx layout.Context) layout.Dimensions {
				if !selected {
					return layout.Dimensions{}
				}
				sz := image.Pt(gtx.Dp(2), gtx.Constraints.Min.Y)
				paint.FillShape(gtx.Ops, pal.accent, clip.Rect{Max: sz}.Op())
				return layout.Dimensions{Size: sz}
			}),
		)
	})
}

func actionButton(gtx layout.Context, th *material.Theme, st *state, pal palette, label string, disabled bool) layout.Dimensions {
	if st.running {
		disabled = true
		label += "…"
	}
	if disabled {
		gtx = gtx.Disabled()
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = gtx.Dp(140)
		return st.btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			macro := op.Record(gtx.Ops)
			dims := layout.UniformInset(unit.Dp(10)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(14), label)
					lbl.Color = pal.fg
					return lbl.Layout(gtx)
				})
			})
			call := macro.Stop()
			rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(4))
			border := clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(gtx.Dp(1), 1))}.Op()
			paint.FillShape(gtx.Ops, pal.fg, border)
			call.Add(gtx.Ops)
			return dims
		})
	})
}

func drawToggle(gtx layout.Context, st *state, pal palette) layout.Dimensions {
	sz := image.Pt(gtx.Dp(32), gtx.Dp(32))
	return st.toggle.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if st.dark {
			drawSun(gtx, pal)
		} else {
			drawMoon(gtx, pal)
		}
		return layout.Dimensions{Size: sz}
	})
}

func drawMoon(gtx layout.Context, pal palette) {
	cx, cy := gtx.Dp(16), gtx.Dp(16)
	r := gtx.Dp(8)
	base := clip.Ellipse{Min: image.Pt(cx-r, cy-r), Max: image.Pt(cx+r, cy+r)}.Op(gtx.Ops)
	paint.FillShape(gtx.Ops, pal.muted, base)
	ox, oy := gtx.Dp(3), gtx.Dp(3)
	cut := clip.Ellipse{Min: image.Pt(cx-r+ox, cy-r-oy), Max: image.Pt(cx+r+ox, cy+r-oy)}.Op(gtx.Ops)
	paint.FillShape(gtx.Ops, pal.bg, cut)
}

func drawSun(gtx layout.Context, pal palette) {
	cx, cy := gtx.Dp(16), gtx.Dp(16)
	r := gtx.Dp(5)
	disc := clip.Ellipse{Min: image.Pt(cx-r, cy-r), Max: image.Pt(cx+r, cy+r)}.Op(gtx.Ops)
	paint.FillShape(gtx.Ops, pal.muted, disc)
	inR, outR := gtx.Dp(8), gtx.Dp(14)
	rayW := max(gtx.Dp(2), 1)
	half := rayW / 2
	for i := range 8 {
		angle := float32(i) * math.Pi / 4
		stack := op.Affine(f32.Affine2D{}.Rotate(f32.Pt(float32(cx), float32(cy)), angle)).Push(gtx.Ops)
		ray := clip.Rect{Min: image.Pt(cx-half, cy-outR), Max: image.Pt(cx-half+rayW, cy-inR)}.Op()
		paint.FillShape(gtx.Ops, pal.muted, ray)
		stack.Pop()
	}
}

func footer(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	type line struct {
		text string
		c    color.NRGBA
	}
	var lines []line
	if st.running {
		lines = append(lines, line{text: "running…", c: pal.muted})
	}
	if st.pollErr != nil {
		lines = append(lines, line{text: "status: " + st.pollErr.Error(), c: pal.muted})
	}
	if st.cmdErr != nil {
		lines = append(lines, line{text: st.cmdName + ": " + st.cmdErr.Error(), c: pal.errFg})
		if st.cmdOut != "" {
			lines = append(lines, line{text: strings.TrimSpace(st.cmdOut), c: pal.errFg})
		}
	}
	if len(lines) == 0 {
		return layout.Dimensions{}
	}
	children := make([]layout.FlexChild, 0, len(lines))
	for _, l := range lines {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), l.text)
			lbl.Color = l.c
			return lbl.Layout(gtx)
		}))
	}
	return layout.Inset{Top: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func relayMatches(r *mullvad.Relay, lower string) bool {
	if lower == "" {
		return true
	}
	return strings.Contains(strings.ToLower(r.Hostname), lower) ||
		strings.Contains(strings.ToLower(r.Country), lower) ||
		strings.Contains(strings.ToLower(r.City), lower)
}

func buildRows(relays []mullvad.Relay, expanded map[string]bool, filter string) []row {
	f := strings.ToLower(strings.TrimSpace(filter))
	groups := map[string][]*mullvad.Relay{}
	for i := range relays {
		r := &relays[i]
		if !relayMatches(r, f) {
			continue
		}
		groups[r.Country] = append(groups[r.Country], r)
	}
	names := make([]string, 0, len(groups))
	for c := range groups {
		names = append(names, c)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	var out []row
	for _, c := range names {
		out = append(out, row{country: c})
		if !expanded[c] && f == "" {
			continue
		}
		items := groups[c]
		sort.Slice(items, func(i, j int) bool {
			return items[i].Hostname < items[j].Hostname
		})
		for _, r := range items {
			out = append(out, row{country: c, relay: r})
		}
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func pollStatus(ctx context.Context, w *app.Window, snaps chan<- status.JSONOut, errs chan<- error) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		s, err := readStatus(ctx)
		if err != nil {
			select {
			case errs <- err:
			default:
			}
		} else {
			select {
			case snaps <- s:
			default:
			}
		}
		w.Invalidate()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func mvadPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "mvad"
	}
	p := filepath.Join(filepath.Dir(exe), "mvad")
	if _, err := os.Stat(p); err != nil {
		return "mvad"
	}
	return p
}

func readStatus(ctx context.Context) (status.JSONOut, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, mvadPath(), "status", "--format=json").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return status.JSONOut{}, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return status.JSONOut{}, err
	}
	var s status.JSONOut
	if err := json.Unmarshal(out, &s); err != nil {
		return status.JSONOut{}, err
	}
	return s, nil
}

type cmdResult struct {
	name string
	out  string
	err  error
}

func runCmd(ctx context.Context, w *app.Window, done chan<- cmdResult, args ...string) {
	// pkexec must invoke the path the polkit policy is keyed on.
	full := append([]string{"mvad"}, args...)
	out, err := exec.CommandContext(ctx, "pkexec", full...).CombinedOutput()
	select {
	case done <- cmdResult{name: args[0], out: string(out), err: err}:
	case <-ctx.Done():
	}
	w.Invalidate()
}

type relayLoadResult struct {
	relays []mullvad.Relay
	err    error
}

func loadRelays(ctx context.Context, w *app.Window, refresh bool, done chan<- relayLoadResult) {
	args := []string{"relays", "--json"}
	if refresh {
		args = append(args, "--refresh")
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, mvadPath(), args...).Output()
	var res relayLoadResult
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			res.err = fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		} else {
			res.err = err
		}
	} else if uerr := json.Unmarshal(out, &res.relays); uerr != nil {
		res.err = uerr
	}
	select {
	case done <- res:
	case <-ctx.Done():
	}
	w.Invalidate()
}

func parseTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
