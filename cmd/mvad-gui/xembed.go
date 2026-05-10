package main

import (
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"log"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shape"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
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

	mu     sync.Mutex
	menu   []menuItem
	dark   bool
	wake   func()
	redraw chan struct{}

	popup     xproto.Window
	popupGC   xproto.Gcontext
	popupRows []popupRow
	popupYs   []int
	popupRowH int
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

func startXEmbed(ctx context.Context, polls <-chan pollResult, windowState <-chan bool, cmds chan<- trayCmd, favorites []string) (*xembed, error) {
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
	depth := screen.RootDepth

	if err := shape.Init(conn); err != nil {
		log.Printf("tray: depth24, shape=err: %v", err)
	} else {
		rects := buildShapeRects(pmDown, 22, 22)
		if err := shape.RectanglesChecked(conn, shape.SoSet, shape.SkBounding,
			xproto.ClipOrderingYXBanded, wid, 0, 0, rects).Check(); err != nil {
			log.Printf("tray: depth24, shape=err: %v", err)
		} else {
			log.Printf("tray: depth24, shape=ok")
		}
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
		redraw:   make(chan struct{}, 1),
		popupHov: -1,
	}
	x.menu = buildTrayMenu(favorites, false, "")
	log.Printf("tray: menu initialized (%d items)", len(x.menu))
	if _, err := popupFont(); err != nil {
		log.Printf("tray: popup font: %v", err)
	}
	x.put(pmDown)

	go x.read()
	go x.loop(ctx, polls, windowState)
	return x, nil
}

// shield() lays bytes out as A,R,G,B, so alpha is at offset 0.
func buildShapeRects(pix []byte, w, h int) []xproto.Rectangle {
	var out []xproto.Rectangle
	for y := range h {
		x := 0
		for x < w {
			for x < w && pix[(y*w+x)*4] < 128 {
				x++
			}
			start := x
			for x < w && pix[(y*w+x)*4] >= 128 {
				x++
			}
			if x > start {
				out = append(out, xproto.Rectangle{
					X:      int16(start),
					Y:      int16(y),
					Width:  uint16(x - start),
					Height: 1,
				})
			}
		}
	}
	return out
}

var (
	popupFaceOnce sync.Once
	popupFace     font.Face
	popupFaceErr  error
)

func popupFont() (font.Face, error) {
	popupFaceOnce.Do(func() {
		f, err := opentype.Parse(goregular.TTF)
		if err != nil {
			popupFaceErr = err
			return
		}
		popupFace, popupFaceErr = opentype.NewFace(f, &opentype.FaceOptions{
			Size:    13,
			DPI:     96,
			Hinting: font.HintingFull,
		})
	})
	return popupFace, popupFaceErr
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
		case <-x.redraw:
			x.drawPopup()
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
			log.Printf("tray: popup at %d,%d", e.RootX, e.RootY)
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
		x.doWake()
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

func (x *xembed) setWake(fn func()) {
	x.mu.Lock()
	x.wake = fn
	x.mu.Unlock()
}

func (x *xembed) setDark(v bool) {
	x.mu.Lock()
	x.dark = v
	x.mu.Unlock()
	select {
	case x.redraw <- struct{}{}:
	default:
	}
}

func (x *xembed) doWake() {
	x.mu.Lock()
	fn := x.wake
	x.mu.Unlock()
	if fn != nil {
		fn()
	}
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

const (
	popupPadX    = 12
	popupPadY    = 8
	popupSepH    = 9
	popupIndent  = 16
	popupBorderW = 1
)

type popupPalette struct {
	bg, fg, muted, accent, edge, accFg color.RGBA
}

var (
	popupLight = popupPalette{
		bg:     color.RGBA{0xFA, 0xFA, 0xF7, 0xFF},
		fg:     color.RGBA{0x1A, 0x1A, 0x1A, 0xFF},
		muted:  color.RGBA{0x6B, 0x6B, 0x6B, 0xFF},
		accent: color.RGBA{0x2E, 0x7D, 0x5B, 0xFF},
		edge:   color.RGBA{0xE2, 0xE2, 0xDF, 0xFF},
		accFg:  color.RGBA{0xFF, 0xFF, 0xFF, 0xFF},
	}
	popupDark = popupPalette{
		bg:     color.RGBA{0x14, 0x14, 0x16, 0xFF},
		fg:     color.RGBA{0xED, 0xED, 0xE8, 0xFF},
		muted:  color.RGBA{0x8A, 0x8A, 0x88, 0xFF},
		accent: color.RGBA{0x5D, 0xBF, 0x8E, 0xFF},
		edge:   color.RGBA{0x2A, 0x2A, 0x2C, 0xFF},
		accFg:  color.RGBA{0xFF, 0xFF, 0xFF, 0xFF},
	}
)

func (x *xembed) openPopup(rootX, rootY int16, shown bool) {
	x.closePopup()
	face, err := popupFont()
	if err != nil {
		log.Printf("tray: popup font: %v", err)
		return
	}
	rows := x.buildPopupRows(shown)
	if len(rows) == 0 {
		return
	}

	m := face.Metrics()
	ascent := m.Ascent.Ceil()
	descent := m.Descent.Ceil()
	rowH := ascent + descent + 6

	var maxAdv fixed.Int26_6
	totalH := 2*popupBorderW + 2*popupPadY
	ys := make([]int, len(rows))
	y := popupBorderW + popupPadY
	for i, r := range rows {
		ys[i] = y
		if r.sep {
			y += popupSepH
			totalH += popupSepH
			continue
		}
		adv := font.MeasureString(face, r.label)
		if r.indent {
			adv += fixed.I(popupIndent)
		}
		if adv > maxAdv {
			maxAdv = adv
		}
		y += rowH
		totalH += rowH
	}
	width := uint16(maxAdv.Ceil() + 2*popupPadX + 2*popupBorderW)
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
		log.Printf("tray: popup create: %v", err)
		return
	}

	gc, err := xproto.NewGcontextId(x.conn)
	if err != nil {
		xproto.DestroyWindow(x.conn, wid)
		return
	}
	xproto.CreateGC(x.conn, gc, xproto.Drawable(wid),
		xproto.GcForeground|xproto.GcBackground,
		[]uint32{0x000000, 0xFFFFFF})

	x.popup = wid
	x.popupGC = gc
	x.popupRows = rows
	x.popupYs = ys
	x.popupRowH = rowH
	x.popupW = int(width)
	x.popupH = int(height)
	x.popupHov = -1
	if err := xproto.MapWindowChecked(x.conn, wid).Check(); err != nil {
		log.Printf("tray: popup map: %v", err)
		x.closePopup()
		return
	}
	log.Printf("tray: popup mapped")
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
	face, err := popupFont()
	if err != nil {
		return
	}
	x.mu.Lock()
	pal := popupLight
	if x.dark {
		pal = popupDark
	}
	x.mu.Unlock()
	img := renderPopup(face, x.popupRows, x.popupYs, x.popupRowH, x.popupHov, x.popupW, x.popupH, pal)
	xproto.PutImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.popup), x.popupGC,
		uint16(x.popupW), uint16(x.popupH), 0, 0, 0, 24, rgbaToZPixmap24(img))
}

func renderPopup(face font.Face, rows []popupRow, ys []int, rowH, hov, w, h int, pal popupPalette) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: pal.bg}, image.Point{}, draw.Src)

	edge := &image.Uniform{C: pal.edge}
	draw.Draw(img, image.Rect(0, 0, w, popupBorderW), edge, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, h-popupBorderW, w, h), edge, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, popupBorderW, h), edge, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(w-popupBorderW, 0, w, h), edge, image.Point{}, draw.Src)

	ascent := face.Metrics().Ascent.Ceil()

	for i, r := range rows {
		y := ys[i]
		if r.sep {
			sy := y + popupSepH/2
			draw.Draw(img,
				image.Rect(popupPadX, sy, w-popupPadX, sy+1),
				&image.Uniform{C: pal.muted}, image.Point{}, draw.Src)
			continue
		}
		fg := pal.fg
		if r.noop {
			fg = pal.muted
		}
		if i == hov && !r.noop {
			draw.Draw(img,
				image.Rect(popupBorderW, y, w-popupBorderW, y+rowH),
				&image.Uniform{C: pal.accent}, image.Point{}, draw.Src)
			fg = pal.accFg
		}
		px := popupBorderW + popupPadX
		if r.indent {
			px += popupIndent
		}
		baseline := y + ascent + 3
		d := font.Drawer{
			Dst:  img,
			Src:  &image.Uniform{C: fg},
			Face: face,
			Dot:  fixed.Point26_6{X: fixed.I(px), Y: fixed.I(baseline)},
		}
		d.DrawString(r.label)
	}
	return img
}

// rgbaToZPixmap24 reorders image.RGBA's R,G,B,A bytes into the depth-24
// B,G,R,pad layout PutImage(ZPixmap) expects on a little-endian X server.
func rgbaToZPixmap24(img *image.RGBA) []byte {
	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	out := make([]byte, w*h*4)
	for y := range h {
		srow := img.Pix[y*img.Stride : y*img.Stride+w*4]
		drow := out[y*w*4 : (y+1)*w*4]
		for x := range w {
			drow[x*4+0] = srow[x*4+2]
			drow[x*4+1] = srow[x*4+1]
			drow[x*4+2] = srow[x*4+0]
			drow[x*4+3] = 0
		}
	}
	return out
}

func (x *xembed) hoverPopup(y int) {
	if x.popup == 0 {
		return
	}
	hov := -1
	for i, r := range x.popupRows {
		if r.sep || r.noop {
			continue
		}
		if y >= x.popupYs[i] && y < x.popupYs[i]+x.popupRowH {
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
	for i, r := range x.popupRows {
		if r.sep {
			continue
		}
		if eventY >= x.popupYs[i] && eventY < x.popupYs[i]+x.popupRowH {
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
