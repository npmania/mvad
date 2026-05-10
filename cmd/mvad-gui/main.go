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
	inputErr  string
	loadedAny bool
	dark      bool
}

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("mvad"), app.Size(unit.Dp(380), unit.Dp(320)))
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

	var (
		relay    widget.Editor
		btn      widget.Clickable
		toggle   widget.Clickable
		st       state
		snaps    = make(chan status.JSONOut, 1)
		pollErrs = make(chan error, 1)
		cmdDone  = make(chan cmdResult, 1)
	)
	relay.SingleLine = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pollStatus(ctx, w, snaps, pollErrs)

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
		default:
		}

		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)

			if toggle.Clicked(gtx) {
				st.dark = !st.dark
			}

			if !st.running && btn.Clicked(gtx) {
				if st.snap.Connected {
					st.inputErr = ""
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					go runCmd(ctx, w, cmdDone, "disconnect")
				} else {
					name := strings.TrimSpace(relay.Text())
					if name == "" {
						st.inputErr = "enter a relay name"
					} else {
						st.inputErr = ""
						st.cmdErr = nil
						st.cmdOut = ""
						st.running = true
						go runCmd(ctx, w, cmdDone, "connect", "--", name)
					}
				}
			}

			layoutUI(gtx, th, &st, &relay, &btn, &toggle)
			e.Frame(gtx.Ops)
		}
	}
}

func layoutUI(gtx layout.Context, th *material.Theme, st *state, relay *widget.Editor, btn, toggle *widget.Clickable) {
	pal := lightPalette
	if st.dark {
		pal = darkPalette
	}
	paint.FillShape(gtx.Ops, pal.bg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	th.Bg, th.Fg = pal.bg, pal.fg
	th.ContrastBg, th.ContrastFg = pal.fg, pal.bg

	layout.Stack{}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return contentBody(gtx, th, st, relay, btn, pal)
			})
		}),
		layout.Expanded(func(gtx layout.Context) layout.Dimensions {
			return layout.NE.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return drawToggle(gtx, st, pal, toggle)
				})
			})
		}),
	)
}

func contentBody(gtx layout.Context, th *material.Theme, st *state, relay *widget.Editor, btn *widget.Clickable, pal palette) layout.Dimensions {
	word, wordColor := headline(st, pal)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(24), word)
			lbl.Color = wordColor
			return lbl.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !st.snap.Connected {
				return layout.Dimensions{}
			}
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
			if st.snap.Connected {
				return layout.Dimensions{}
			}
			return relayInput(gtx, th, relay, pal)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if st.inputErr == "" {
				return layout.Dimensions{}
			}
			lbl := material.Label(th, unit.Sp(12), st.inputErr)
			lbl.Color = pal.errFg
			return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, lbl.Layout)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(28)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return actionButton(gtx, th, st, btn, pal)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: gtx.Constraints.Min}
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

func relayInput(gtx layout.Context, th *material.Theme, ed *widget.Editor, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), "Relay")
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

func actionButton(gtx layout.Context, th *material.Theme, st *state, btn *widget.Clickable, pal palette) layout.Dimensions {
	label := "Connect"
	if st.snap.Connected {
		label = "Disconnect"
	}
	if st.running {
		gtx = gtx.Disabled()
		label += "…"
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = gtx.Dp(140)
		return btn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
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

func drawToggle(gtx layout.Context, st *state, pal palette, click *widget.Clickable) layout.Dimensions {
	sz := image.Pt(gtx.Dp(32), gtx.Dp(32))
	return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
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

func readStatus(ctx context.Context) (status.JSONOut, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "mvad", "status", "--format=json").Output()
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
	full := append([]string{"mvad"}, args...)
	out, err := exec.CommandContext(ctx, "pkexec", full...).CombinedOutput()
	select {
	case done <- cmdResult{name: args[0], out: string(out), err: err}:
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
