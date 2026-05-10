package main

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	"sync"

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

	font        xproto.Font
	fontAscent  int
	fontDescent int
	charW       int

	mu   sync.Mutex
	menu []menuItem

	popup     xproto.Window
	popupGC   xproto.Gcontext
	popupRows []popupRow
	popupYs   []int
	popupW    int
	popupH    int
	popupHov  int
}

type popupRow struct {
	label  string
	sep    bool
	indent bool
	noop   bool
	cmd    trayCmd
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
	depth, ok := tryARGB(conn, screen, wid)
	if ok {
		log.Printf("tray: argb")
	} else {
		mask := uint32(xproto.CwBackPixmap | xproto.CwEventMask)
		values := []uint32{
			xproto.BackPixmapParentRelative,
			xproto.EventMaskButtonPress | xproto.EventMaskExposure,
		}
		if err := xproto.CreateWindowChecked(conn, screen.RootDepth, wid, screen.Root,
			0, 0, 22, 22, 0,
			xproto.WindowClassInputOutput, screen.RootVisual,
			mask, values).Check(); err != nil {
			conn.Close()
			return nil, err
		}
		depth = screen.RootDepth
		log.Printf("tray: rgb")
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
		conn:     conn,
		wid:      wid,
		gc:       gc,
		depth:    depth,
		cmds:     cmds,
		events:   make(chan xgb.Event, 16),
		done:     make(chan struct{}),
		popupHov: -1,
	}
	x.openFont()
	x.put(pmDown)

	go x.read()
	go x.loop(ctx, polls, windowState)
	return x, nil
}

// tryARGB attempts to create an ARGB tray window using the first
// depth-32 TrueColor visual the screen advertises. tint2 doesn't
// broadcast _NET_SYSTEM_TRAY_VISUAL, so we don't ask, we just try.
func tryARGB(conn *xgb.Conn, screen *xproto.ScreenInfo, wid xproto.Window) (byte, bool) {
	for _, d := range screen.AllowedDepths {
		if d.Depth != 32 {
			continue
		}
		for _, v := range d.Visuals {
			if v.Class != xproto.VisualClassTrueColor {
				continue
			}
			cm, err := xproto.NewColormapId(conn)
			if err != nil {
				return 0, false
			}
			if err := xproto.CreateColormapChecked(conn, xproto.ColormapAllocNone, cm, screen.Root, v.VisualId).Check(); err != nil {
				continue
			}
			mask := uint32(xproto.CwBackPixel | xproto.CwBorderPixel | xproto.CwEventMask | xproto.CwColormap)
			values := []uint32{
				0,
				0,
				xproto.EventMaskButtonPress | xproto.EventMaskExposure,
				uint32(cm),
			}
			if err := xproto.CreateWindowChecked(conn, 32, wid, screen.Root,
				0, 0, 22, 22, 0,
				xproto.WindowClassInputOutput, v.VisualId,
				mask, values).Check(); err != nil {
				xproto.FreeColormap(conn, cm)
				continue
			}
			return 32, true
		}
	}
	return 0, false
}

func (x *xembed) openFont() {
	f, err := xproto.NewFontId(x.conn)
	if err != nil {
		return
	}
	const name = "fixed"
	if err := xproto.OpenFontChecked(x.conn, f, uint16(len(name)), name).Check(); err != nil {
		return
	}
	q, err := xproto.QueryFont(x.conn, xproto.Fontable(f)).Reply()
	if err != nil {
		xproto.CloseFont(x.conn, f)
		return
	}
	x.font = f
	x.fontAscent = int(q.FontAscent)
	x.fontDescent = int(q.FontDescent)
	x.charW = int(q.MaxBounds.CharacterWidth)
	if x.charW <= 0 {
		x.charW = 7
	}
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
			x.closePopup()
			xproto.DestroyWindow(x.conn, x.wid)
			x.conn.Close()
			return
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			x.handleEvent(ev, shown, last)
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

func (x *xembed) handleEvent(ev xgb.Event, shown bool, last []byte) {
	switch e := ev.(type) {
	case xproto.ButtonPressEvent:
		switch {
		case e.Event == x.wid && e.Detail == 1:
			if shown {
				x.send(trayCmd{kind: cmdHide})
			} else {
				x.send(trayCmd{kind: cmdShow})
			}
		case e.Event == x.wid && e.Detail == 3:
			x.openPopup(e.RootX, e.RootY, shown)
		case e.Event == x.popup && e.Detail == 1:
			x.clickPopup(int(e.EventX), int(e.EventY))
		}
	case xproto.MotionNotifyEvent:
		if e.Event == x.popup {
			x.hoverPopup(int(e.EventY))
		}
	case xproto.LeaveNotifyEvent:
		if e.Event == x.popup {
			x.closePopup()
		}
	case xproto.ExposeEvent:
		switch e.Window {
		case x.wid:
			x.put(last)
		case x.popup:
			x.drawPopup()
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

func (x *xembed) setMenu(items []menuItem) {
	x.mu.Lock()
	x.menu = items
	x.mu.Unlock()
}

func (x *xembed) buildPopupRows(shown bool) []popupRow {
	x.mu.Lock()
	items := x.menu
	x.mu.Unlock()
	var rows []popupRow
	for _, it := range items {
		row := popupRow{
			label: it.label,
			sep:   it.sep,
			cmd:   it.cmd,
			noop:  len(it.children) > 0,
		}
		if it.id == 1 {
			if shown {
				row.label = "Hide"
				row.cmd = trayCmd{kind: cmdHide}
			} else {
				row.label = "Show"
				row.cmd = trayCmd{kind: cmdShow}
			}
		}
		rows = append(rows, row)
		for _, c := range it.children {
			rows = append(rows, popupRow{
				label:  c.label,
				indent: true,
				cmd:    c.cmd,
			})
		}
	}
	return rows
}

// Popup colors are written as raw RGB literals; that assumes the root
// visual is TrueColor, which has held on every X server we care about
// for two decades.
const (
	popupPadX   = 10
	popupPadY   = 6
	popupSepH   = 7
	popupIndent = 14
)

func (x *xembed) openPopup(rootX, rootY int16, shown bool) {
	x.closePopup()
	if x.font == 0 {
		return
	}
	rows := x.buildPopupRows(shown)
	if len(rows) == 0 {
		return
	}

	rowH := x.fontAscent + x.fontDescent + 4
	maxW := 0
	totalH := popupPadY * 2
	ys := make([]int, len(rows))
	y := popupPadY
	for i, r := range rows {
		ys[i] = y
		if r.sep {
			y += popupSepH
			totalH += popupSepH
			continue
		}
		w := len(r.label) * x.charW
		if r.indent {
			w += popupIndent
		}
		if w > maxW {
			maxW = w
		}
		y += rowH
		totalH += rowH
	}
	width := uint16(maxW + 2*popupPadX)
	height := uint16(totalH)

	screen := xproto.Setup(x.conn).DefaultScreen(x.conn)
	px := int(rootX)
	py := int(rootY) - int(height)
	if py < 0 {
		py = int(rootY) + 22
	}
	if px+int(width) > int(screen.WidthInPixels) {
		px = int(screen.WidthInPixels) - int(width)
	}
	if px < 0 {
		px = 0
	}

	wid, err := xproto.NewWindowId(x.conn)
	if err != nil {
		return
	}
	mask := uint32(xproto.CwBackPixel | xproto.CwOverrideRedirect | xproto.CwEventMask)
	values := []uint32{
		0xFFFFFF,
		1,
		uint32(xproto.EventMaskButtonPress |
			xproto.EventMaskEnterWindow |
			xproto.EventMaskLeaveWindow |
			xproto.EventMaskPointerMotion |
			xproto.EventMaskExposure),
	}
	if err := xproto.CreateWindowChecked(x.conn, screen.RootDepth, wid, screen.Root,
		int16(px), int16(py), width, height, 0,
		xproto.WindowClassInputOutput, screen.RootVisual,
		mask, values).Check(); err != nil {
		return
	}

	gc, err := xproto.NewGcontextId(x.conn)
	if err != nil {
		xproto.DestroyWindow(x.conn, wid)
		return
	}
	xproto.CreateGC(x.conn, gc, xproto.Drawable(wid),
		xproto.GcForeground|xproto.GcBackground|xproto.GcFont,
		[]uint32{0x000000, 0xFFFFFF, uint32(x.font)})

	x.popup = wid
	x.popupGC = gc
	x.popupRows = rows
	x.popupYs = ys
	x.popupW = int(width)
	x.popupH = int(height)
	x.popupHov = -1
	xproto.MapWindow(x.conn, wid)
}

func (x *xembed) closePopup() {
	if x.popup == 0 {
		return
	}
	xproto.FreeGC(x.conn, x.popupGC)
	xproto.DestroyWindow(x.conn, x.popup)
	x.popup = 0
	x.popupGC = 0
	x.popupRows = nil
	x.popupYs = nil
	x.popupHov = -1
}

func (x *xembed) drawPopup() {
	if x.popup == 0 {
		return
	}
	rowH := x.fontAscent + x.fontDescent + 4

	xproto.ChangeGC(x.conn, x.popupGC, xproto.GcForeground, []uint32{0xFFFFFF})
	xproto.PolyFillRectangle(x.conn, xproto.Drawable(x.popup), x.popupGC,
		[]xproto.Rectangle{{X: 0, Y: 0, Width: uint16(x.popupW), Height: uint16(x.popupH)}})

	if x.popupHov >= 0 {
		xproto.ChangeGC(x.conn, x.popupGC, xproto.GcForeground, []uint32{0xCCCCCC})
		xproto.PolyFillRectangle(x.conn, xproto.Drawable(x.popup), x.popupGC,
			[]xproto.Rectangle{{X: 0, Y: int16(x.popupYs[x.popupHov]), Width: uint16(x.popupW), Height: uint16(rowH)}})
	}

	xproto.ChangeGC(x.conn, x.popupGC, xproto.GcForeground, []uint32{0x999999})
	for i, r := range x.popupRows {
		if !r.sep {
			continue
		}
		y := x.popupYs[i] + popupSepH/2
		xproto.PolyFillRectangle(x.conn, xproto.Drawable(x.popup), x.popupGC,
			[]xproto.Rectangle{{X: int16(popupPadX), Y: int16(y), Width: uint16(x.popupW - 2*popupPadX), Height: 1}})
	}

	for i, r := range x.popupRows {
		if r.sep {
			continue
		}
		bg := uint32(0xFFFFFF)
		if i == x.popupHov {
			bg = 0xCCCCCC
		}
		fg := uint32(0x000000)
		if r.noop {
			fg = 0x666666
		}
		xproto.ChangeGC(x.conn, x.popupGC,
			xproto.GcForeground|xproto.GcBackground,
			[]uint32{fg, bg})
		px := popupPadX
		if r.indent {
			px += popupIndent
		}
		py := x.popupYs[i] + 2 + x.fontAscent
		label := r.label
		if len(label) > 255 {
			label = label[:255]
		}
		xproto.ImageText8(x.conn, byte(len(label)), xproto.Drawable(x.popup), x.popupGC,
			int16(px), int16(py), label)
	}
}

func (x *xembed) hoverPopup(y int) {
	if x.popup == 0 {
		return
	}
	rowH := x.fontAscent + x.fontDescent + 4
	hov := -1
	for i, r := range x.popupRows {
		if r.sep || r.noop {
			continue
		}
		if y >= x.popupYs[i] && y < x.popupYs[i]+rowH {
			hov = i
			break
		}
	}
	if hov == x.popupHov {
		return
	}
	x.popupHov = hov
	x.drawPopup()
}

func (x *xembed) clickPopup(eventX, eventY int) {
	if x.popup == 0 {
		return
	}
	if eventX < 0 || eventX >= x.popupW || eventY < 0 || eventY >= x.popupH {
		x.closePopup()
		return
	}
	rowH := x.fontAscent + x.fontDescent + 4
	for i, r := range x.popupRows {
		if r.sep {
			continue
		}
		if eventY >= x.popupYs[i] && eventY < x.popupYs[i]+rowH {
			if r.noop {
				return
			}
			cmd := r.cmd
			x.closePopup()
			x.send(cmd)
			return
		}
	}
	x.closePopup()
}

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
