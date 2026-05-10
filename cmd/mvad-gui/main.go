package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/npmania/mvad/internal/status"
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
}

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("mvad"), app.Size(unit.Dp(380), unit.Dp(280)))
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

			layoutUI(gtx, th, &st, &relay, &btn)
			e.Frame(gtx.Ops)
		}
	}
}

func layoutUI(gtx layout.Context, th *material.Theme, st *state, relay *widget.Editor, btn *widget.Clickable) {
	inset := layout.UniformInset(unit.Dp(16))
	inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return statusHeader(gtx, th, st)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				border := widget.Border{
					Color:        color.NRGBA{R: th.Fg.R, G: th.Fg.G, B: th.Fg.B, A: 0x66},
					CornerRadius: unit.Dp(4),
					Width:        unit.Dp(1),
				}
				return border.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						ed := material.Editor(th, relay, "relay (e.g. se-mma-wg-101)")
						ed.HintColor = color.NRGBA{R: th.Fg.R, G: th.Fg.G, B: th.Fg.B, A: 0x80}
						return ed.Layout(gtx)
					})
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if st.inputErr == "" {
					return layout.Dimensions{}
				}
				lbl := material.Body2(th, st.inputErr)
				lbl.Color = color.NRGBA{R: 0xc0, G: 0x20, B: 0x20, A: 0xff}
				return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, lbl.Layout)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Max.X = gtx.Dp(160)
					label := "Connect"
					if st.snap.Connected {
						label = "Disconnect"
					}
					if st.running {
						gtx = gtx.Disabled()
						label += "…"
					}
					return material.Button(th, btn, label).Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return footer(gtx, th, st)
			}),
		)
	})
}

func statusHeader(gtx layout.Context, th *material.Theme, st *state) layout.Dimensions {
	lines := headerLines(st)
	children := make([]layout.FlexChild, 0, len(lines))
	for i, line := range lines {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if i == 0 {
				return material.H6(th, line).Layout(gtx)
			}
			return material.Body1(th, line).Layout(gtx)
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func headerLines(st *state) []string {
	if !st.loadedAny {
		return []string{"loading…"}
	}
	var out []string
	s := st.snap
	if !s.Connected {
		out = append(out, "disconnected")
	} else {
		name := s.Relay
		if name == "" {
			name = s.Endpoint
		}
		head := "connected to " + name
		if s.Entry != "" {
			head += " via " + s.Entry
		}
		out = append(out, head)
		if s.TxBytes != 0 || s.RxBytes != 0 {
			out = append(out, fmt.Sprintf("%s ↑ / %s ↓", status.HumanBytes(s.TxBytes), status.HumanBytes(s.RxBytes)))
		}
		if hs, ok := parseTime(s.LastHandshake); ok {
			out = append(out, "last handshake "+status.HumanDuration(time.Since(hs))+" ago")
		}
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

func footer(gtx layout.Context, th *material.Theme, st *state) layout.Dimensions {
	type line struct {
		text  string
		color color.NRGBA
	}
	muted := color.NRGBA{R: th.Fg.R, G: th.Fg.G, B: th.Fg.B, A: 0x99}
	red := color.NRGBA{R: 0xc0, G: 0x20, B: 0x20, A: 0xff}

	var lines []line
	if st.running {
		lines = append(lines, line{text: "running…"})
	}
	if st.pollErr != nil {
		lines = append(lines, line{text: "status: " + st.pollErr.Error(), color: muted})
	}
	if st.cmdErr != nil {
		lines = append(lines, line{text: st.cmdName + ": " + st.cmdErr.Error(), color: red})
		if st.cmdOut != "" {
			lines = append(lines, line{text: strings.TrimSpace(st.cmdOut), color: red})
		}
	}
	if len(lines) == 0 {
		return layout.Dimensions{}
	}
	children := make([]layout.FlexChild, 0, len(lines))
	for _, l := range lines {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Body2(th, l.text)
			if l.color != (color.NRGBA{}) {
				lbl.Color = l.color
			}
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
