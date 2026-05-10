package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font/gofont"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/npmania/mvad/internal/config"
	"github.com/npmania/mvad/internal/mullvad"
	"github.com/npmania/mvad/internal/split"
	"github.com/npmania/mvad/internal/status"
)

const ifname = "mvad-wg0"

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

type viewKind int

const (
	viewConnect viewKind = iota
	viewSettings
	viewAccount
	viewSplit
)

type splitPID struct {
	pid  int
	comm string
}

type state struct {
	ctx context.Context

	cmdDone          chan cmdResult
	relayDone        chan relayLoadResult
	splitDone        chan splitLoadResult
	runDone          chan runResult
	acctDone         chan acctLoadResult
	deviceRemoveDone chan deviceRemoveResult

	snap        status.Snapshot
	pollErr     error
	cmdOut      string
	cmdErr      error
	cmdName     string
	running     bool
	runningName string
	loadedAny   bool
	dark        bool

	cfg *config.Config

	filter       widget.Editor
	relayList    widget.List
	relays       []mullvad.Relay
	expanded     map[string]bool
	selected     string
	relayLoadErr string
	relayLoading bool

	btn       widget.Clickable
	reconnect widget.Clickable
	refresh   widget.Clickable
	toggle    widget.Clickable

	headerClicks map[string]*widget.Clickable
	rowClicks    map[string]*widget.Clickable

	view viewKind

	allowLAN   widget.Bool
	lockdownOn widget.Bool
	transport  string

	tabConn, tabSet, tabAcct, tabSplit widget.Clickable
	trWG, trTCP                        widget.Clickable
	openAcct                           widget.Clickable
	logout                             widget.Clickable
	logoutArmed                        time.Time

	loginToken widget.Editor
	loginBtn   widget.Clickable
	signupBtn  widget.Clickable
	loginErr   string
	signupOut  string

	acctRefresh    widget.Clickable
	acctRefreshing bool
	acctRefreshErr string

	devices        []mullvad.Device
	devicesLoaded  bool
	devicesErr     string
	deviceClicks   map[string]*widget.Clickable
	deviceArmed    map[string]time.Time
	deviceRemoving string

	splitList    widget.List
	splitRefresh widget.Clickable
	addPID       widget.Editor
	runCmdEd     widget.Editor
	runBtn       widget.Clickable
	clearBtn     widget.Clickable
	clearArmed   time.Time
	splitPIDs    []splitPID
	splitErr     string
	splitLoading bool
	splitLoaded  bool
	runStarting  []string
}

type row struct {
	country string
	relay   *mullvad.Relay
}

type trayCmd int

const (
	cmdShow trayCmd = iota
	cmdConnect
	cmdSettings
	cmdAccount
	cmdSplit
	cmdQuit
	cmdHide
)

var errQuit = errors.New("quit")

func setWindowState(ch chan bool, s bool) {
	for {
		select {
		case ch <- s:
			return
		case <-ch:
		}
	}
}

func applyTrayCmd(st *state, c trayCmd) {
	switch c {
	case cmdConnect:
		st.view = viewConnect
	case cmdSettings:
		st.view = viewSettings
	case cmdAccount:
		st.view = viewAccount
	case cmdSplit:
		st.view = viewSplit
	}
}

func main() {
	var hidden bool
	flag.BoolVar(&hidden, "hidden", false, "start without showing the window (tray-only)")
	flag.Parse()

	var st state
	st.filter.SingleLine = true
	st.relayList.Axis = layout.Vertical
	st.expanded = map[string]bool{}
	st.headerClicks = map[string]*widget.Clickable{}
	st.rowClicks = map[string]*widget.Clickable{}
	st.deviceClicks = map[string]*widget.Clickable{}
	st.deviceArmed = map[string]time.Time{}
	st.splitList.Axis = layout.Vertical
	st.addPID.SingleLine = true
	st.addPID.Submit = true
	st.runCmdEd.SingleLine = true
	st.loginToken.SingleLine = true

	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = &config.Config{}
	}
	st.cfg = cfg
	st.allowLAN.Value = cfg.AllowLAN
	st.lockdownOn.Value = cfg.LockdownOn
	st.transport = cfg.LastTransport
	if st.transport != "tcp" {
		st.transport = "wireguard"
	}

	pollsWin := make(chan pollResult, 1)
	pollsTray := make(chan pollResult, 1)
	trayCmds := make(chan trayCmd)
	windowState := make(chan bool, 1)

	st.cmdDone = make(chan cmdResult, 1)
	st.relayDone = make(chan relayLoadResult, 1)
	st.splitDone = make(chan splitLoadResult, 1)
	st.runDone = make(chan runResult, 1)
	st.acctDone = make(chan acctLoadResult, 1)
	st.deviceRemoveDone = make(chan deviceRemoveResult, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st.ctx = ctx

	go pollStatus(ctx, pollsWin, pollsTray)

	tr, err := startTray(ctx, pollsTray, windowState, trayCmds)
	if err != nil {
		log.Printf("tray: %v (no SNI watcher; install snixembed for tint2/legacy trays, or AppIndicator extension for GNOME)", err)
		tr = nil
	}

	go func() {
		defer func() {
			cancel()
			if tr != nil {
				tr.shutdown()
			}
			os.Exit(0)
		}()

		windowed := !hidden || tr == nil
		for {
			if windowed {
				w := new(app.Window)
				w.Option(app.Title("mvad"), app.Size(unit.Dp(420), unit.Dp(540)))
				if tr != nil {
					setWindowState(windowState, true)
				}
				err := run(w, &st, pollsWin, trayCmds)
				if errors.Is(err, errQuit) {
					return
				}
				if err != nil {
					log.Fatal(err)
				}
				windowed = false
				if tr == nil {
					return
				}
				setWindowState(windowState, false)
				continue
			}
			select {
			case c := <-trayCmds:
				switch c {
				case cmdQuit:
					return
				case cmdHide:
				default:
					applyTrayCmd(&st, c)
					windowed = true
				}
			case <-pollsWin:
			case <-ctx.Done():
				return
			}
		}
	}()
	app.Main()
}

func run(w *app.Window, st *state, polls <-chan pollResult, trayCmds <-chan trayCmd) error {
	th := material.NewTheme()
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))

	wctx, wcancel := context.WithCancel(st.ctx)
	defer wcancel()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-wctx.Done():
				return
			case <-t.C:
				w.Invalidate()
			}
		}
	}()

	if !st.relayLoading && len(st.relays) == 0 && st.relayLoadErr == "" {
		st.relayLoading = true
		go loadRelays(st.ctx, w, false, st.relayDone)
	}

	var ops op.Ops
	for {
		select {
		case r := <-polls:
			if r.err != nil {
				st.pollErr = r.err
			} else {
				st.snap = r.snap
				st.pollErr = nil
			}
			st.loadedAny = true
		case r := <-st.cmdDone:
			if r.name != "xdg-open" {
				st.running = false
				st.runningName = ""
			}
			st.cmdName = r.name
			st.cmdOut = r.out
			st.cmdErr = r.err
			switch r.name {
			case "lockdown":
				if r.err == nil {
					st.cfg.LockdownOn = st.lockdownOn.Value
					_ = st.cfg.Save()
				} else {
					st.lockdownOn.Value = !st.lockdownOn.Value
				}
			case "logout":
				if r.err == nil {
					if c, err := config.Load(); err == nil && c != nil {
						st.cfg = c
					}
					st.devices = nil
					st.devicesLoaded = false
					st.devicesErr = ""
					st.deviceArmed = map[string]time.Time{}
					st.deviceClicks = map[string]*widget.Clickable{}
					st.deviceRemoving = ""
				}
			case "login":
				if r.err == nil {
					if c, err := config.Load(); err == nil && c != nil {
						st.cfg = c
					}
					st.loginToken.SetText("")
					st.devices = nil
					st.devicesLoaded = false
					st.devicesErr = ""
					if !st.acctRefreshing && st.cfg.AccountToken != "" {
						st.acctRefreshing = true
						st.acctRefreshErr = ""
						go loadAccount(st.ctx, w, st.acctDone)
					}
				}
			case "signup":
				if r.err == nil {
					if c, err := config.Load(); err == nil && c != nil {
						st.cfg = c
					}
					st.signupOut = strings.TrimSpace(r.out)
					st.devices = nil
					st.devicesLoaded = false
					st.devicesErr = ""
					if !st.acctRefreshing && st.cfg.AccountToken != "" {
						st.acctRefreshing = true
						st.acctRefreshErr = ""
						go loadAccount(st.ctx, w, st.acctDone)
					}
				}
			case "split":
				if !st.splitLoading {
					st.splitLoading = true
					go loadSplit(st.ctx, w, st.splitDone)
				}
			}
		case r := <-st.relayDone:
			st.relayLoading = false
			if r.err != nil {
				st.relayLoadErr = r.err.Error()
			} else {
				st.relayLoadErr = ""
				st.relays = r.relays
			}
		case r := <-st.splitDone:
			st.splitLoading = false
			st.splitLoaded = true
			if r.err != nil {
				st.splitErr = r.err.Error()
				st.splitPIDs = nil
			} else {
				st.splitErr = ""
				st.splitPIDs = r.pids
			}
		case r := <-st.runDone:
			for i, c := range st.runStarting {
				if c == r.cmdline {
					st.runStarting = append(st.runStarting[:i], st.runStarting[i+1:]...)
					break
				}
			}
			st.cmdName = "run"
			st.cmdOut = r.out
			st.cmdErr = r.err
			if !st.splitLoading {
				st.splitLoading = true
				go loadSplit(st.ctx, w, st.splitDone)
			}
		case r := <-st.acctDone:
			st.acctRefreshing = false
			if r.err != nil {
				st.acctRefreshErr = r.err.Error()
				st.devicesErr = r.err.Error()
			} else {
				st.acctRefreshErr = ""
				st.devicesErr = ""
				st.devices = r.devices
				st.devicesLoaded = true
				if c, err := config.Load(); err == nil && c != nil {
					st.cfg = c
				}
			}
		case r := <-st.deviceRemoveDone:
			st.deviceRemoving = ""
			if r.err != nil {
				st.devicesErr = r.err.Error()
			} else {
				st.devicesErr = ""
				delete(st.deviceClicks, r.id)
				delete(st.deviceArmed, r.id)
				if !st.acctRefreshing && st.cfg.AccountToken != "" {
					st.acctRefreshing = true
					st.acctRefreshErr = ""
					go loadAccount(st.ctx, w, st.acctDone)
				}
			}
		case c := <-trayCmds:
			switch c {
			case cmdHide:
				w.Perform(system.ActionClose)
			case cmdQuit:
				return errQuit
			default:
				applyTrayCmd(st, c)
			}
		default:
		}

		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return nil
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)

			if st.toggle.Clicked(gtx) {
				st.dark = !st.dark
			}
			if st.tabConn.Clicked(gtx) {
				st.view = viewConnect
			}
			if st.tabSet.Clicked(gtx) {
				st.view = viewSettings
			}
			if st.tabAcct.Clicked(gtx) {
				st.view = viewAccount
				if st.cfg.AccountToken != "" && !st.devicesLoaded && !st.acctRefreshing {
					st.acctRefreshing = true
					st.acctRefreshErr = ""
					go loadAccount(st.ctx, w, st.acctDone)
				}
			}
			if st.tabSplit.Clicked(gtx) {
				st.view = viewSplit
				if !st.splitLoaded && !st.splitLoading {
					st.splitLoading = true
					go loadSplit(st.ctx, w, st.splitDone)
				}
			}
			if !st.relayLoading && st.refresh.Clicked(gtx) {
				st.relayLoading = true
				go loadRelays(st.ctx, w, true, st.relayDone)
			}
			if !st.splitLoading && st.splitRefresh.Clicked(gtx) {
				st.splitLoading = true
				go loadSplit(st.ctx, w, st.splitDone)
			}
			for {
				ev, ok := st.addPID.Update(gtx)
				if !ok {
					break
				}
				if _, ok := ev.(widget.SubmitEvent); !ok {
					continue
				}
				text := strings.TrimSpace(st.addPID.Text())
				n, err := strconv.Atoi(text)
				if err != nil || n <= 0 {
					st.cmdErr = fmt.Errorf("invalid pid %q", text)
					st.cmdOut = ""
					st.cmdName = "split-add"
					continue
				}
				if st.running {
					continue
				}
				st.addPID.SetText("")
				st.cmdErr = nil
				st.cmdOut = ""
				st.running = true
				st.runningName = "split-add"
				go runCmd(st.ctx, w, st.cmdDone, "split", "add-pid", strconv.Itoa(n))
			}
			if len(st.runStarting) == 0 && st.runBtn.Clicked(gtx) {
				text := strings.TrimSpace(st.runCmdEd.Text())
				fields := strings.Fields(text)
				if len(fields) > 0 {
					st.runCmdEd.SetText("")
					st.runStarting = append(st.runStarting, text)
					args := append([]string{"run", "--"}, fields...)
					go runOutside(st.ctx, w, st.runDone, text, args...)
				}
			}
			if !st.running && st.clearBtn.Clicked(gtx) {
				if !st.clearArmed.IsZero() && time.Since(st.clearArmed) < 5*time.Second {
					st.clearArmed = time.Time{}
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					st.runningName = "split-clear"
					go runCmd(st.ctx, w, st.cmdDone, "split", "clear")
				} else {
					st.clearArmed = time.Now()
				}
			}
			if !st.clearArmed.IsZero() && time.Since(st.clearArmed) >= 5*time.Second {
				st.clearArmed = time.Time{}
			}
			if st.allowLAN.Update(gtx) {
				st.cfg.AllowLAN = st.allowLAN.Value
				_ = st.cfg.Save()
			}
			if st.lockdownOn.Update(gtx) {
				if st.running {
					st.lockdownOn.Value = !st.lockdownOn.Value
				} else {
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					st.runningName = "lockdown"
					sub := "off"
					if st.lockdownOn.Value {
						sub = "on"
					}
					go runCmd(st.ctx, w, st.cmdDone, "lockdown", sub)
				}
			}
			if st.trWG.Clicked(gtx) {
				st.transport = "wireguard"
			}
			if st.trTCP.Clicked(gtx) {
				st.transport = "tcp"
			}
			if st.openAcct.Clicked(gtx) {
				go runExternal(st.ctx, w, st.cmdDone, "xdg-open", "https://mullvad.net/account")
			}
			if !st.running && st.btn.Clicked(gtx) {
				if st.snap.Up {
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					st.runningName = "disconnect"
					go runCmd(st.ctx, w, st.cmdDone, "disconnect")
				} else if st.selected != "" {
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					st.runningName = "connect"
					args := []string{"connect"}
					if st.allowLAN.Value {
						args = append(args, "--allow-lan")
					}
					if st.transport == "tcp" {
						args = append(args, "--transport=tcp")
					}
					args = append(args, "--", st.selected)
					go runCmd(st.ctx, w, st.cmdDone, args...)
				}
			}
			if !st.running && st.reconnect.Clicked(gtx) && st.cfg.LastRelay != "" {
				st.cmdErr = nil
				st.cmdOut = ""
				st.running = true
				st.runningName = "reconnect"
				args := []string{"reconnect"}
				if st.allowLAN.Value {
					args = append(args, "--allow-lan")
				}
				go runCmd(st.ctx, w, st.cmdDone, args...)
			}
			if !st.running && st.loginBtn.Clicked(gtx) {
				token := strings.TrimSpace(st.loginToken.Text())
				if token == "" {
					st.loginErr = "enter account number"
				} else {
					st.loginErr = ""
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					st.runningName = "login"
					go runCmd(st.ctx, w, st.cmdDone, "login", "--", token)
				}
			}
			if !st.running && st.signupBtn.Clicked(gtx) {
				st.cmdErr = nil
				st.cmdOut = ""
				st.running = true
				st.runningName = "signup"
				go runCmd(st.ctx, w, st.cmdDone, "signup")
			}
			if !st.running && st.logout.Clicked(gtx) {
				if !st.logoutArmed.IsZero() && time.Since(st.logoutArmed) < 5*time.Second {
					st.logoutArmed = time.Time{}
					st.cmdErr = nil
					st.cmdOut = ""
					st.running = true
					st.runningName = "logout"
					go runCmd(st.ctx, w, st.cmdDone, "logout")
				} else {
					st.logoutArmed = time.Now()
				}
			}
			if !st.logoutArmed.IsZero() && time.Since(st.logoutArmed) >= 5*time.Second {
				st.logoutArmed = time.Time{}
			}
			if !st.acctRefreshing && st.acctRefresh.Clicked(gtx) && st.cfg.AccountToken != "" {
				st.acctRefreshing = true
				st.acctRefreshErr = ""
				go loadAccount(st.ctx, w, st.acctDone)
			}
			if st.cfg.AccountToken != "" {
				now := time.Now()
				for _, d := range st.devices {
					if d.ID == st.cfg.DeviceID {
						continue
					}
					c, ok := st.deviceClicks[d.ID]
					if !ok {
						c = &widget.Clickable{}
						st.deviceClicks[d.ID] = c
					}
					if !c.Clicked(gtx) {
						continue
					}
					if t, armed := st.deviceArmed[d.ID]; armed && now.Sub(t) < 5*time.Second {
						delete(st.deviceArmed, d.ID)
						if st.deviceRemoving == "" {
							st.deviceRemoving = d.ID
							st.devicesErr = ""
							go removeDevice(st.ctx, w, st.deviceRemoveDone, st.cfg.AccountToken, d.ID)
						}
					} else {
						st.deviceArmed[d.ID] = now
					}
				}
				for id, t := range st.deviceArmed {
					if now.Sub(t) >= 5*time.Second {
						delete(st.deviceArmed, id)
					}
				}
			}

			layoutUI(gtx, th, st)
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

	if st.snap.Up {
		if st.selected != "" || len(st.expanded) > 0 || st.filter.Text() != "" {
			st.selected = ""
			st.expanded = map[string]bool{}
			st.filter.SetText("")
		}
	}

	layout.Stack{}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			dims := layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return tabStrip(gtx, th, st, pal)
					}),
					layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						return panelBody(gtx, th, st, pal)
					}),
				)
			})
			dims.Size.X = max(dims.Size.X, gtx.Constraints.Max.X)
			dims.Size.Y = max(dims.Size.Y, gtx.Constraints.Max.Y)
			return dims
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

func panelBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	switch st.view {
	case viewSettings:
		return settingsBody(gtx, th, st, pal)
	case viewAccount:
		return accountBody(gtx, th, st, pal)
	case viewSplit:
		return splitBody(gtx, th, st, pal)
	}
	if st.snap.Up {
		return connectedBody(gtx, th, st, pal)
	}
	return disconnectedBody(gtx, th, st, pal)
}

func tabStrip(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return tab(gtx, th, &st.tabConn, pal, "Connect", st.view == viewConnect)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return tab(gtx, th, &st.tabSet, pal, "Settings", st.view == viewSettings)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return tab(gtx, th, &st.tabAcct, pal, "Account", st.view == viewAccount)
		}),
		layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return tab(gtx, th, &st.tabSplit, pal, "Split", st.view == viewSplit)
		}),
	)
}

func tab(gtx layout.Context, th *material.Theme, c *widget.Clickable, pal palette, label string, active bool) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(14), label)
			lbl.Color = pal.muted
			if active {
				lbl.Color = pal.fg
			}
			macro := op.Record(gtx.Ops)
			dims := lbl.Layout(gtx)
			call := macro.Stop()
			call.Add(gtx.Ops)
			h := dims.Size.Y
			if active {
				gap := gtx.Dp(2)
				push := op.Offset(image.Pt(0, dims.Size.Y+gap)).Push(gtx.Ops)
				paint.FillShape(gtx.Ops, pal.accent, clip.Rect{Max: image.Pt(dims.Size.X, gtx.Dp(1))}.Op())
				push.Pop()
				h += gap + gtx.Dp(1)
			}
			return layout.Dimensions{Size: image.Pt(dims.Size.X, h)}
		})
	})
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
			return actionButton(gtx, th, &st.btn, pal, "Disconnect", st.running, st.runningName == "disconnect")
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return reconnectLink(gtx, th, st, pal)
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
			return actionButton(gtx, th, &st.btn, pal, "Connect", btnDisabled, st.runningName == "connect")
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return reconnectLink(gtx, th, st, pal)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return footer(gtx, th, st, pal)
		}),
	)
}

func settingsBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return settingsRow(gtx, th, pal, "Allow LAN", "", &st.allowLAN)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return transportSection(gtx, th, st, pal)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return settingsRow(gtx, th, pal, "Lockdown", "persistent kill-switch", &st.lockdownOn)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !st.snap.Up {
				return layout.Dimensions{}
			}
			return layout.Inset{Top: unit.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(12), "applies on next connect")
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return footer(gtx, th, st, pal)
		}),
	)
}

func settingsRow(gtx layout.Context, th *material.Theme, pal palette, title, sub string, b *widget.Bool) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(14), title)
					lbl.Color = pal.fg
					return lbl.Layout(gtx)
				}),
			}
			if sub != "" {
				children = append(children,
					layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						lbl := material.Label(th, unit.Sp(12), sub)
						lbl.Color = pal.muted
						return lbl.Layout(gtx)
					}),
				)
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return togglePill(gtx, b, pal)
		}),
	)
}

func transportSection(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(14), "Transport")
			lbl.Color = pal.fg
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return transportLabel(gtx, th, &st.trWG, pal, "wireguard", st.transport == "wireguard")
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(16)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return transportLabel(gtx, th, &st.trTCP, pal, "tcp", st.transport == "tcp")
				}),
			)
		}),
	)
}

func transportLabel(gtx layout.Context, th *material.Theme, c *widget.Clickable, pal palette, label string, active bool) layout.Dimensions {
	return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Label(th, unit.Sp(13), label)
		lbl.Color = pal.muted
		if active {
			lbl.Color = pal.fg
		}
		macro := op.Record(gtx.Ops)
		dims := lbl.Layout(gtx)
		call := macro.Stop()
		call.Add(gtx.Ops)
		h := dims.Size.Y
		if active {
			gap := gtx.Dp(2)
			push := op.Offset(image.Pt(0, dims.Size.Y+gap)).Push(gtx.Ops)
			paint.FillShape(gtx.Ops, pal.accent, clip.Rect{Max: image.Pt(dims.Size.X, gtx.Dp(1))}.Op())
			push.Pop()
			h += gap + gtx.Dp(1)
		}
		return layout.Dimensions{Size: image.Pt(dims.Size.X, h)}
	})
}

func togglePill(gtx layout.Context, b *widget.Bool, pal palette) layout.Dimensions {
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		w, h := gtx.Dp(28), gtx.Dp(14)
		track := pal.dim
		if b.Value {
			track = pal.accent
		}
		rr := clip.UniformRRect(image.Rectangle{Max: image.Pt(w, h)}, h/2)
		paint.FillShape(gtx.Ops, track, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(gtx.Dp(1), 1))}.Op())
		d := h - gtx.Dp(4)
		x := gtx.Dp(2)
		if b.Value {
			x = w - d - gtx.Dp(2)
		}
		y := (h - d) / 2
		knob := image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+d, y+d)}
		knobRR := clip.UniformRRect(knob, d/2)
		if b.Value {
			paint.FillShape(gtx.Ops, pal.accent, knobRR.Op(gtx.Ops))
		} else {
			paint.FillShape(gtx.Ops, pal.muted, clip.Stroke{Path: knobRR.Path(gtx.Ops), Width: float32(max(gtx.Dp(1), 1))}.Op())
		}
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
}

func accountBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	if strings.TrimSpace(st.loginToken.Text()) != "" {
		st.loginErr = ""
	}
	var children []layout.FlexChild
	if st.cfg.DeviceID != "" {
		children = accountInfoRows(th, st, pal)
	} else {
		children = accountSignInRows(th, st, pal)
	}
	children = append(children,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: gtx.Constraints.Min}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return footer(gtx, th, st, pal)
		}),
	)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func accountInfoRows(th *material.Theme, st *state, pal palette) []layout.FlexChild {
	cfg := st.cfg
	expText := "expiry unknown"
	if !cfg.AccountExpiry.IsZero() {
		if time.Until(cfg.AccountExpiry) <= 0 {
			expText = "account expired"
		} else {
			expText = "expires " + status.HumanExpiry(cfg.AccountExpiry)
		}
	}
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					rows := []layout.FlexChild{
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(14), expText)
							lbl.Color = pal.fg
							return lbl.Layout(gtx)
						}),
					}
					if cfg.DeviceName != "" {
						rows = append(rows,
							layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								lbl := material.Label(th, unit.Sp(13), "device  "+cfg.DeviceName)
								lbl.Color = pal.muted
								return lbl.Layout(gtx)
							}),
						)
					}
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return refreshGlyph(gtx, th, &st.acctRefresh, st.acctRefreshing, pal)
				}),
			)
		}),
	}
	if st.acctRefreshErr != "" {
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(6)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(12), st.acctRefreshErr)
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			}),
		)
	}
	children = append(children, devicesSection(th, st, pal)...)
	logoutLabel := "Logout"
	if !st.logoutArmed.IsZero() && time.Since(st.logoutArmed) < 5*time.Second {
		logoutLabel = "Confirm?"
	}
	children = append(children,
		layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hairline(gtx, pal.dim)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return st.openAcct.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(14), "Open account page ↗")
				lbl.Color = pal.accent
				return lbl.Layout(gtx)
			})
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(20)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hairline(gtx, pal.dim)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(28)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return actionButton(gtx, th, &st.logout, pal, logoutLabel, st.running, st.runningName == "logout")
		}),
	)
	return children
}

func accountSignInRows(th *material.Theme, st *state, pal palette) []layout.FlexChild {
	loginDisabled := strings.TrimSpace(st.loginToken.Text()) == "" || st.running
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(16), "not signed in")
			lbl.Color = pal.muted
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return splitEditor(gtx, th, &st.loginToken, pal, "Account number")
		}),
	}
	if st.loginErr != "" {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(12), st.loginErr)
				lbl.Color = pal.errFg
				return lbl.Layout(gtx)
			})
		}))
	}
	children = append(children,
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return actionButton(gtx, th, &st.loginBtn, pal, "Login", loginDisabled, st.runningName == "login")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(24)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hairline(gtx, pal.dim)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(24)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return actionButton(gtx, th, &st.signupBtn, pal, "Sign up", st.running, st.runningName == "signup")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(24)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hairline(gtx, pal.dim)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(24)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return st.openAcct.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(14), "Open account page ↗")
				lbl.Color = pal.accent
				return lbl.Layout(gtx)
			})
		}),
	)
	return children
}

func devicesSection(th *material.Theme, st *state, pal palette) []layout.FlexChild {
	if st.devicesLoaded && len(st.devices) == 0 && st.devicesErr == "" {
		return nil
	}
	children := []layout.FlexChild{
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return hairline(gtx, pal.dim)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), "Devices")
			lbl.Color = pal.muted
			return lbl.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
	}
	switch {
	case st.devicesLoaded:
		for i := range st.devices {
			d := st.devices[i]
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return deviceRow(gtx, th, st, pal, d)
			}))
		}
	case st.devicesErr == "":
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(13), "loading…")
			lbl.Color = pal.muted
			return lbl.Layout(gtx)
		}))
	}
	if st.devicesErr != "" {
		msg := st.devicesErr
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(12), firstLine(msg))
				lbl.Color = pal.errFg
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(2)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(12), "press ↻ to retry")
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			}),
		)
	}
	return children
}

func deviceRow(gtx layout.Context, th *material.Theme, st *state, pal palette, d mullvad.Device) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), d.Name)
				lbl.Color = pal.fg
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if d.ID == st.cfg.DeviceID {
					lbl := material.Label(th, unit.Sp(12), "this device")
					lbl.Color = pal.muted
					return lbl.Layout(gtx)
				}
				if d.ID == st.deviceRemoving {
					lbl := material.Label(th, unit.Sp(12), "removing…")
					lbl.Color = pal.muted
					return lbl.Layout(gtx)
				}
				c, ok := st.deviceClicks[d.ID]
				if !ok {
					c = &widget.Clickable{}
					st.deviceClicks[d.ID] = c
				}
				label := "remove"
				col := pal.fg
				if t, armed := st.deviceArmed[d.ID]; armed && time.Since(t) < 5*time.Second {
					label = "Confirm?"
					col = pal.errFg
				}
				return c.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(13), label)
					lbl.Color = col
					return lbl.Layout(gtx)
				})
			}),
		)
	})
}

func hairline(gtx layout.Context, c color.NRGBA) layout.Dimensions {
	sz := image.Pt(gtx.Constraints.Max.X, max(gtx.Dp(1), 1))
	paint.FillShape(gtx.Ops, c, clip.Rect{Max: sz}.Op())
	return layout.Dimensions{Size: sz}
}

func headline(st *state, pal palette) (string, color.NRGBA) {
	if !st.loadedAny {
		return "Loading…", pal.muted
	}
	if st.snap.Up {
		return "Connected", pal.accent
	}
	return "Disconnected", pal.fg
}

func subLines(st *state) []string {
	s := st.snap
	if !s.Up {
		return nil
	}
	var out []string
	name := s.Relay
	if name == "" && s.PeerEndpoint.IsValid() {
		name = s.PeerEndpoint.String()
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
	if !s.LastHandshake.IsZero() {
		out = append(out, "last handshake "+status.HumanDuration(time.Since(s.LastHandshake))+" ago")
	}
	if !s.AccountExpiry.IsZero() {
		if time.Until(s.AccountExpiry) <= 0 {
			out = append(out, "account expired")
		} else {
			out = append(out, "expires "+status.HumanExpiry(s.AccountExpiry))
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
			return refreshGlyph(gtx, th, &st.refresh, st.relayLoading, pal)
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

func refreshGlyph(gtx layout.Context, th *material.Theme, click *widget.Clickable, loading bool, pal palette) layout.Dimensions {
	return click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		c := pal.fg
		if loading {
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

func reconnectLink(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	if st.cfg.LastRelay == "" {
		return layout.Dimensions{}
	}
	if st.running {
		gtx = gtx.Disabled()
	}
	label := "Reconnect to " + st.cfg.LastRelay
	if st.runningName == "reconnect" {
		label += "…"
	}
	return layout.Inset{Top: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return st.reconnect.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), label)
				lbl.Color = pal.fg
				return lbl.Layout(gtx)
			})
		})
	})
}

func actionButton(gtx layout.Context, th *material.Theme, btn *widget.Clickable, pal palette, label string, disabled bool, busy bool) layout.Dimensions {
	if busy {
		label += "…"
	}
	if disabled {
		gtx = gtx.Disabled()
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
	if st.signupOut != "" {
		lines = append(lines, line{text: "new account " + st.signupOut, c: pal.muted})
	}
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

type pollResult struct {
	snap status.Snapshot
	err  error
}

func pollStatus(ctx context.Context, win, tray chan<- pollResult) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		r := pollResult{}
		r.snap, r.err = readStatus()
		select {
		case win <- r:
		default:
		}
		if tray != nil {
			select {
			case tray <- r:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func readStatus() (status.Snapshot, error) {
	s, err := status.Read(ifname)
	if err != nil && !errors.Is(err, status.ErrNotConnected) {
		return status.Snapshot{}, err
	}
	if cfg, cerr := config.Load(); cerr == nil {
		s.Relay = cfg.LastRelay
		s.Entry = cfg.LastEntryRelay
		s.AccountExpiry = cfg.AccountExpiry
		s.DeviceName = cfg.DeviceName
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

func runExternal(ctx context.Context, w *app.Window, done chan<- cmdResult, name string, args ...string) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	select {
	case done <- cmdResult{name: name, out: string(out), err: err}:
	case <-ctx.Done():
	}
	w.Invalidate()
}

type relayLoadResult struct {
	relays []mullvad.Relay
	err    error
}

func loadRelays(ctx context.Context, w *app.Window, refresh bool, done chan<- relayLoadResult) {
	res := loadRelaysNow(ctx, refresh)
	select {
	case done <- res:
	case <-ctx.Done():
	}
	w.Invalidate()
}

func loadRelaysNow(ctx context.Context, refresh bool) relayLoadResult {
	var res relayLoadResult
	cfg, err := config.Load()
	if err != nil {
		res.err = err
		return res
	}
	if !refresh && len(cfg.RelayCache) != 0 && time.Since(cfg.RelaysFetchedAt) < 24*time.Hour {
		var relays []mullvad.Relay
		if err := json.Unmarshal(cfg.RelayCache, &relays); err == nil {
			res.relays = relays
			return res
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	relays, err := mullvad.New().Relays(cctx)
	if err != nil {
		res.err = err
		return res
	}
	res.relays = relays
	if data, err := json.Marshal(relays); err == nil {
		cfg.RelayCache = data
		cfg.RelaysFetchedAt = time.Now()
		_ = cfg.Save()
	}
	return res
}

type acctLoadResult struct {
	devices []mullvad.Device
	err     error
}

type deviceRemoveResult struct {
	id  string
	err error
}

func loadAccount(ctx context.Context, w *app.Window, done chan<- acctLoadResult) {
	res := loadAccountNow(ctx)
	select {
	case done <- res:
	case <-ctx.Done():
	}
	w.Invalidate()
}

func loadAccountNow(ctx context.Context) acctLoadResult {
	cfg, err := config.Load()
	if err != nil {
		return acctLoadResult{err: err}
	}
	if cfg == nil || cfg.AccountToken == "" {
		return acctLoadResult{err: errors.New("not logged in")}
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c := mullvad.New()
	exp, err := c.AccountExpiry(cctx, cfg.AccountToken)
	if err != nil {
		return acctLoadResult{err: err}
	}
	cfg.AccountExpiry = exp
	var devs []mullvad.Device
	if cfg.DeviceID != "" {
		devs, err = c.ListDevices(cctx, cfg.AccountToken)
		if err != nil {
			return acctLoadResult{err: err}
		}
		for _, d := range devs {
			if d.ID == cfg.DeviceID {
				cfg.DeviceName = d.Name
				break
			}
		}
	}
	if err := cfg.Save(); err != nil {
		return acctLoadResult{err: err}
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Created.Before(devs[j].Created) })
	return acctLoadResult{devices: devs}
}

func removeDevice(ctx context.Context, w *app.Window, done chan<- deviceRemoveResult, token, id string) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	err := mullvad.New().RevokeDevice(cctx, token, id)
	select {
	case done <- deviceRemoveResult{id: id, err: err}:
	case <-ctx.Done():
	}
	w.Invalidate()
}

type splitLoadResult struct {
	pids []splitPID
	err  error
}

type runResult struct {
	cmdline string
	out     string
	err     error
}

func loadSplit(ctx context.Context, w *app.Window, done chan<- splitLoadResult) {
	var res splitLoadResult
	pids, err := split.ListPIDs()
	if err != nil {
		res.err = err
	} else {
		for _, pid := range pids {
			res.pids = append(res.pids, splitPID{pid: pid, comm: readComm(pid)})
		}
	}
	select {
	case done <- res:
	case <-ctx.Done():
	}
	w.Invalidate()
}

func readComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return "(gone)"
	}
	return strings.TrimSpace(string(data))
}

func runOutside(ctx context.Context, w *app.Window, done chan<- runResult, cmdline string, args ...string) {
	full := append([]string{"mvad"}, args...)
	out, err := exec.CommandContext(ctx, "pkexec", full...).CombinedOutput()
	select {
	case done <- runResult{cmdline: cmdline, out: string(out), err: err}:
	case <-ctx.Done():
	}
	w.Invalidate()
}

func splitBody(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					lbl := material.Label(th, unit.Sp(14), "processes")
					lbl.Color = pal.fg
					return lbl.Layout(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return splitRefreshGlyph(gtx, th, st, pal)
				}),
			)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return splitListArea(gtx, th, st, pal)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return splitEditor(gtx, th, &st.addPID, pal, "Add PID")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return splitRunBlock(gtx, th, st, pal)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return splitClearLink(gtx, th, st, pal)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return footer(gtx, th, st, pal)
		}),
	)
}

func splitRefreshGlyph(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return st.splitRefresh.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		c := pal.fg
		if st.splitLoading {
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

func splitListArea(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	if st.splitErr != "" {
		return splitErrorPlaceholder(gtx, th, pal, st.splitErr)
	}
	if st.splitLoading && len(st.splitPIDs) == 0 && len(st.runStarting) == 0 {
		return centerLabel(gtx, th, pal.muted, "loading…")
	}
	rows := len(st.splitPIDs) + len(st.runStarting)
	if rows == 0 {
		return centerLabel(gtx, th, pal.muted, "no processes")
	}
	list := material.List(th, &st.splitList)
	return list.Layout(gtx, rows, func(gtx layout.Context, i int) layout.Dimensions {
		if i < len(st.splitPIDs) {
			return splitPIDRow(gtx, th, pal, st.splitPIDs[i])
		}
		return splitPendingRow(gtx, th, pal, st.runStarting[i-len(st.splitPIDs)])
	})
}

func splitPIDRow(gtx layout.Context, th *material.Theme, pal palette, p splitPID) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(56)
				lbl := material.Label(th, unit.Sp(13), strconv.Itoa(p.pid))
				lbl.Color = pal.fg
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), p.comm)
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			}),
		)
	})
}

func splitPendingRow(gtx layout.Context, th *material.Theme, pal palette, cmdline string) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(2), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = gtx.Dp(56)
				lbl := material.Label(th, unit.Sp(13), "…")
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), cmdline)
				lbl.Color = pal.muted
				return lbl.Layout(gtx)
			}),
		)
	})
}

func splitErrorPlaceholder(gtx layout.Context, th *material.Theme, pal palette, msg string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				lbl := material.Label(th, unit.Sp(13), firstLine(msg))
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

func splitEditor(gtx layout.Context, th *material.Theme, ed *widget.Editor, pal palette, label string) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			lbl := material.Label(th, unit.Sp(12), label)
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

func splitRunBlock(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return splitEditor(gtx, th, &st.runCmdEd, pal, "Run outside tunnel")
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Min}
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					busy := len(st.runStarting) > 0
					label := "Run"
					if busy {
						label += "…"
					}
					if busy {
						gtx = gtx.Disabled()
					}
					return st.runBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						macro := op.Record(gtx.Ops)
						dims := layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							lbl := material.Label(th, unit.Sp(14), label)
							lbl.Color = pal.fg
							return lbl.Layout(gtx)
						})
						call := macro.Stop()
						rr := clip.UniformRRect(image.Rectangle{Max: dims.Size}, gtx.Dp(4))
						border := clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(gtx.Dp(1), 1))}.Op()
						paint.FillShape(gtx.Ops, pal.fg, border)
						call.Add(gtx.Ops)
						return dims
					})
				}),
			)
		}),
	)
}

func splitClearLink(gtx layout.Context, th *material.Theme, st *state, pal palette) layout.Dimensions {
	label := "Clear all"
	if !st.clearArmed.IsZero() && time.Since(st.clearArmed) < 5*time.Second {
		label = "Confirm?"
	}
	return st.clearBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		lbl := material.Label(th, unit.Sp(13), label)
		lbl.Color = pal.fg
		return lbl.Layout(gtx)
	})
}
