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
	wid, err := xproto.NewWindowId(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := xproto.CreateWindowChecked(conn, screen.RootDepth, wid, screen.Root,
		0, 0, 22, 22, 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwBackPixmap|xproto.CwEventMask,
		[]uint32{
			xproto.BackPixmapParentRelative,
			xproto.EventMaskButtonPress | xproto.EventMaskExposure,
		}).Check(); err != nil {
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
		cmds:   cmds,
		events: make(chan xgb.Event, 16),
		done:   make(chan struct{}),
	}
	x.put(pmDown)

	go x.read()
	go x.loop(ctx, polls, windowState)
	return x, nil
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
		22, 22, 0, 0, 0, 24, toZPixmap(argb))
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
// into the wire layout expected by PutImage(ZPixmap, depth=24) on a
// little-endian X server: a 32-bit pixel laid out as B,G,R,pad.
func toZPixmap(argb []byte) []byte {
	out := make([]byte, len(argb))
	for i := 0; i < len(argb); i += 4 {
		out[i+0] = argb[i+3]
		out[i+1] = argb[i+2]
		out[i+2] = argb[i+1]
		out[i+3] = 0
	}
	return out
}
