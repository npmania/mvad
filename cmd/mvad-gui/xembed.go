package main

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const (
	xembedMapped          = 1
	systemTrayRequestDock = 0
)

type xembed struct {
	conn   *xgb.Conn
	wid    xproto.Window
	gc     xproto.Gcontext
	depth  byte
	cmds   chan<- trayCmd
	events chan xgb.Event
	done   chan struct{}
}

func startXEmbed(ctx context.Context, polls <-chan pollResult, windowState <-chan bool, cmds chan<- trayCmd) (*xembed, error) {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}

	atoms := map[string]xproto.Atom{}
	for _, n := range []string{
		"_NET_SYSTEM_TRAY_S0",
		"_NET_SYSTEM_TRAY_OPCODE",
		"_NET_SYSTEM_TRAY_VISUAL",
		"_XEMBED_INFO",
	} {
		r, err := xproto.InternAtom(conn, false, uint16(len(n)), n).Reply()
		if err != nil {
			conn.Close()
			return nil, err
		}
		atoms[n] = r.Atom
	}

	owner, err := xproto.GetSelectionOwner(conn, atoms["_NET_SYSTEM_TRAY_S0"]).Reply()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if owner.Owner == xproto.WindowNone {
		conn.Close()
		return nil, errors.New("no system tray host")
	}

	screen := xproto.Setup(conn).DefaultScreen(conn)
	depth, visual := pickVisual(conn, screen, owner.Owner, atoms["_NET_SYSTEM_TRAY_VISUAL"])

	wid, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	var (
		mask   uint32
		values []uint32
	)
	if depth == screen.RootDepth && visual == screen.RootVisual {
		mask = xproto.CwBackPixmap | xproto.CwEventMask
		values = []uint32{
			xproto.BackPixmapParentRelative,
			xproto.EventMaskButtonPress | xproto.EventMaskExposure,
		}
	} else {
		cm, err := xproto.NewColormapId(conn)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if err := xproto.CreateColormapChecked(conn, xproto.ColormapAllocNone, cm, screen.Root, visual).Check(); err != nil {
			conn.Close()
			return nil, err
		}
		mask = xproto.CwBackPixel | xproto.CwBorderPixel | xproto.CwEventMask | xproto.CwColormap
		values = []uint32{
			0,
			0,
			xproto.EventMaskButtonPress | xproto.EventMaskExposure,
			uint32(cm),
		}
	}
	if err := xproto.CreateWindowChecked(conn, depth, wid, screen.Root,
		0, 0, 22, 22, 0,
		xproto.WindowClassInputOutput, visual,
		mask, values).Check(); err != nil {
		conn.Close()
		return nil, err
	}

	info := make([]byte, 8)
	binary.LittleEndian.PutUint32(info[0:], 0)
	binary.LittleEndian.PutUint32(info[4:], xembedMapped)
	xproto.ChangeProperty(conn, xproto.PropModeReplace, wid,
		atoms["_XEMBED_INFO"], atoms["_XEMBED_INFO"], 32, 2, info)

	ev := xproto.ClientMessageEvent{
		Format: 32,
		Window: owner.Owner,
		Type:   atoms["_NET_SYSTEM_TRAY_OPCODE"],
		Data: xproto.ClientMessageDataUnionData32New([]uint32{
			0,
			systemTrayRequestDock,
			uint32(wid),
			0, 0,
		}),
	}
	xproto.SendEvent(conn, false, owner.Owner, 0, string(ev.Bytes()))

	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		xproto.DestroyWindow(conn, wid)
		conn.Close()
		return nil, err
	}
	xproto.CreateGC(conn, gc, xproto.Drawable(wid), 0, nil)

	x := &xembed{
		conn:   conn,
		wid:    wid,
		gc:     gc,
		depth:  depth,
		cmds:   cmds,
		events: make(chan xgb.Event, 16),
		done:   make(chan struct{}),
	}
	x.put(pmDown)

	go x.read()
	go x.loop(ctx, polls, windowState)
	return x, nil
}

// pickVisual reads _NET_SYSTEM_TRAY_VISUAL from the tray manager and
// looks the id up in the screen's depth list. ARGB panels advertise a
// depth-32 TrueColor visual; absent or unknown means stick with the
// root visual.
func pickVisual(conn *xgb.Conn, screen *xproto.ScreenInfo, owner xproto.Window, atom xproto.Atom) (byte, xproto.Visualid) {
	r, err := xproto.GetProperty(conn, false, owner, atom, xproto.AtomCardinal, 0, 1).Reply()
	if err != nil || r == nil || r.Format != 32 || r.ValueLen < 1 || len(r.Value) < 4 {
		return screen.RootDepth, screen.RootVisual
	}
	id := xproto.Visualid(xgb.Get32(r.Value))
	if id == 0 {
		return screen.RootDepth, screen.RootVisual
	}
	for _, d := range screen.AllowedDepths {
		for _, v := range d.Visuals {
			if v.VisualId == id {
				return d.Depth, id
			}
		}
	}
	return screen.RootDepth, screen.RootVisual
}

func (x *xembed) read() {
	for {
		ev, xerr := x.conn.WaitForEvent()
		if ev == nil && xerr == nil {
			close(x.events)
			return
		}
		if ev != nil {
			x.events <- ev
		}
	}
}

func (x *xembed) loop(ctx context.Context, polls <-chan pollResult, windowState <-chan bool) {
	defer close(x.done)
	var shown, up bool
	last := pmDown
	events := x.events
	for {
		select {
		case <-ctx.Done():
			xproto.DestroyWindow(x.conn, x.wid)
			x.conn.Close()
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch e := ev.(type) {
			case xproto.ButtonPressEvent:
				if e.Detail == 1 {
					if shown {
						x.send(trayCmd{kind: cmdHide})
					} else {
						x.send(trayCmd{kind: cmdShow})
					}
				}
			case xproto.ExposeEvent:
				x.put(last)
			}
		case r := <-polls:
			now := r.err == nil && r.snap.Up
			if now == up {
				continue
			}
			up = now
			if up {
				last = pmUp
			} else {
				last = pmDown
			}
			x.put(last)
		case s := <-windowState:
			shown = s
		}
	}
}

func (x *xembed) put(argb []byte) {
	xproto.PutImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.wid), x.gc,
		22, 22, 0, 0, 0, x.depth, toZPixmap(x.depth, argb))
}

func (x *xembed) send(c trayCmd) {
	select {
	case x.cmds <- c:
	default:
	}
}

func (x *xembed) shutdown() {
	<-x.done
}

func (x *xembed) setMenu(items []menuItem) {}

// toZPixmap converts the in-memory ARGB byte order produced by shield()
// into the wire layout PutImage(ZPixmap) expects on a little-endian X
// server. Depth 24: B,G,R,pad. Depth 32 ARGB visual: B,G,R,A.
func toZPixmap(depth byte, argb []byte) []byte {
	out := make([]byte, len(argb))
	for i := 0; i < len(argb); i += 4 {
		out[i+0] = argb[i+3]
		out[i+1] = argb[i+2]
		out[i+2] = argb[i+1]
		if depth == 32 {
			out[i+3] = argb[i+0]
		}
	}
	return out
}
