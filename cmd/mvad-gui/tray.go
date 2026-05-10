package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

const (
	sniPath   = "/StatusNotifierItem"
	sniIface  = "org.kde.StatusNotifierItem"
	menuPath  = "/MenuBar"
	menuIface = "com.canonical.dbusmenu"
)

type pixmapEntry struct {
	Width  int32
	Height int32
	Pixels []byte
}

type toolTip struct {
	IconName string
	Pixmaps  []pixmapEntry
	Title    string
	Body     string
}

type menuLayout struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type menuItemProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

type sni struct {
	conn    *dbus.Conn
	busName string
	props   *prop.Properties
	cmds    chan<- trayCmd
	up      bool
	done    chan struct{}

	mu       sync.Mutex
	shown    bool
	revision uint32
	menu     []menuItem
	wake     func()
}

func sniWatcherOwned(conn *dbus.Conn) bool {
	var has bool
	err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

func startSNI(ctx context.Context, conn *dbus.Conn, polls <-chan pollResult, windowState <-chan bool, cmds chan<- trayCmd, favorites []string) (*sni, error) {
	busName := "org.kde.StatusNotifierItem-" + strconv.Itoa(os.Getpid()) + "-1"
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("dbus: bus name %s not primary (%v)", busName, reply)
	}

	t := &sni{conn: conn, busName: busName, cmds: cmds, revision: 1}
	t.menu = buildTrayMenu(favorites, false, "")

	if err := conn.Export(sniHandler{t}, sniPath, sniIface); err != nil {
		t.shutdown()
		return nil, err
	}

	pixmaps := []pixmapEntry{{Width: 22, Height: 22, Pixels: pmDown}}
	tip := toolTip{Title: "mvad", Body: "disconnected"}
	sniProps := prop.Map{
		sniIface: {
			"Category":            {Value: "ApplicationStatus", Emit: prop.EmitConst},
			"Id":                  {Value: "mvad", Emit: prop.EmitConst},
			"Title":               {Value: "mvad", Emit: prop.EmitConst},
			"Status":              {Value: "Active", Emit: prop.EmitTrue},
			"IconName":            {Value: "", Emit: prop.EmitTrue},
			"IconPixmap":          {Value: pixmaps, Emit: prop.EmitTrue},
			"AttentionIconName":   {Value: "", Emit: prop.EmitConst},
			"AttentionIconPixmap": {Value: []pixmapEntry{}, Emit: prop.EmitConst},
			"OverlayIconName":     {Value: "", Emit: prop.EmitConst},
			"OverlayIconPixmap":   {Value: []pixmapEntry{}, Emit: prop.EmitConst},
			"ToolTip":             {Value: tip, Emit: prop.EmitTrue},
			"ItemIsMenu":          {Value: false, Emit: prop.EmitConst},
			"Menu":                {Value: dbus.ObjectPath(menuPath), Emit: prop.EmitConst},
			"WindowId":            {Value: uint32(0), Emit: prop.EmitConst},
		},
	}
	p, err := prop.Export(conn, sniPath, sniProps)
	if err != nil {
		t.shutdown()
		return nil, err
	}
	t.props = p

	if err := conn.Export(menuHandler{t}, menuPath, menuIface); err != nil {
		t.shutdown()
		return nil, err
	}
	menuProps := prop.Map{
		menuIface: {
			"Version":       {Value: uint32(3), Emit: prop.EmitConst},
			"TextDirection": {Value: "ltr", Emit: prop.EmitConst},
			"Status":        {Value: "normal", Emit: prop.EmitTrue},
			"IconThemePath": {Value: []string{}, Emit: prop.EmitConst},
		},
	}
	if _, err := prop.Export(conn, menuPath, menuProps); err != nil {
		t.shutdown()
		return nil, err
	}

	watcher := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	call := watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, busName)
	if call.Err != nil {
		t.shutdown()
		return nil, call.Err
	}

	t.done = make(chan struct{})
	go t.loop(ctx, polls, windowState)
	return t, nil
}

func (t *sni) loop(ctx context.Context, polls <-chan pollResult, windowState <-chan bool) {
	defer close(t.done)
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-polls:
			up := r.err == nil && r.snap.Up
			if up == t.up {
				continue
			}
			t.up = up
			t.refresh(up)
		case s := <-windowState:
			t.mu.Lock()
			if t.shown == s {
				t.mu.Unlock()
				continue
			}
			t.shown = s
			t.revision++
			rev := t.revision
			t.mu.Unlock()
			_ = t.conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
		}
	}
}

func (t *sni) refresh(up bool) {
	pm := pmDown
	body := "disconnected"
	if up {
		pm = pmUp
		body = "connected"
	}
	pixmaps := []pixmapEntry{{Width: 22, Height: 22, Pixels: pm}}
	t.props.SetMust(sniIface, "IconPixmap", pixmaps)
	t.props.SetMust(sniIface, "ToolTip", toolTip{Title: "mvad", Body: body})
	_ = t.conn.Emit(sniPath, sniIface+".NewIcon")
	_ = t.conn.Emit(sniPath, sniIface+".NewToolTip")
}

func (t *sni) setMenu(items []menuItem) {
	t.mu.Lock()
	t.menu = items
	t.revision++
	rev := t.revision
	t.mu.Unlock()
	_ = t.conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
}

func (t *sni) setWake(fn func()) {
	t.mu.Lock()
	t.wake = fn
	t.mu.Unlock()
}

func (t *sni) doWake() {
	t.mu.Lock()
	fn := t.wake
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (t *sni) shutdown() {
	if t.conn == nil {
		return
	}
	if t.done != nil {
		<-t.done
	}
	if t.busName != "" {
		_, _ = t.conn.ReleaseName(t.busName)
	}
	_ = t.conn.Close()
	t.conn = nil
}

type sniHandler struct{ t *sni }

func (s sniHandler) Activate(x, y int32) *dbus.Error {
	s.send(trayCmd{kind: cmdShow})
	return nil
}

func (s sniHandler) SecondaryActivate(x, y int32) *dbus.Error {
	s.send(trayCmd{kind: cmdShow})
	return nil
}

func (s sniHandler) ContextMenu(x, y int32) *dbus.Error {
	return nil
}

func (s sniHandler) Scroll(delta int32, orientation string) *dbus.Error {
	return nil
}

func (s sniHandler) send(c trayCmd) {
	select {
	case s.t.cmds <- c:
		s.t.doWake()
	default:
	}
}

type menuItem struct {
	id       int32
	label    string
	sep      bool
	cmd      trayCmd
	children []menuItem
}

func buildTrayMenu(favorites []string, up bool, relay string) []menuItem {
	items := []menuItem{
		{id: 1, label: "Show", cmd: trayCmd{kind: cmdShow}},
		{id: 2, sep: true},
	}
	extras := false
	if len(favorites) > 0 {
		children := make([]menuItem, 0, len(favorites))
		for i, h := range favorites {
			children = append(children, menuItem{
				id:    1000 + int32(i),
				label: h,
				cmd:   trayCmd{kind: cmdConnectFavorite, relay: h},
			})
		}
		items = append(items, menuItem{
			id:       99,
			label:    "Connect to favorite",
			children: children,
		})
		extras = true
	}
	if up && relay != "" && !containsString(favorites, relay) {
		items = append(items, menuItem{
			id:    200,
			label: "Add current to favorites",
			cmd:   trayCmd{kind: cmdAddFavorite, relay: relay},
		})
		extras = true
	}
	if extras {
		items = append(items, menuItem{id: 201, sep: true})
	}
	items = append(items,
		menuItem{id: 3, label: "Connect", cmd: trayCmd{kind: cmdConnect}},
		menuItem{id: 4, label: "Settings", cmd: trayCmd{kind: cmdSettings}},
		menuItem{id: 5, label: "Account", cmd: trayCmd{kind: cmdAccount}},
		menuItem{id: 6, label: "Split", cmd: trayCmd{kind: cmdSplit}},
		menuItem{id: 7, sep: true},
		menuItem{id: 8, label: "Quit", cmd: trayCmd{kind: cmdQuit}},
	)
	return items
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func findByID(items []menuItem, id int32) (menuItem, bool) {
	for _, it := range items {
		if it.id == id {
			return it, true
		}
		if len(it.children) > 0 {
			if c, ok := findByID(it.children, id); ok {
				return c, true
			}
		}
	}
	return menuItem{}, false
}

func (t *sni) showHide() (string, trayCmd) {
	t.mu.Lock()
	s := t.shown
	t.mu.Unlock()
	if s {
		return "Hide", trayCmd{kind: cmdHide}
	}
	return "Show", trayCmd{kind: cmdShow}
}

func (t *sni) itemProps(it menuItem) map[string]dbus.Variant {
	if it.sep {
		return map[string]dbus.Variant{
			"type": dbus.MakeVariant("separator"),
		}
	}
	label := it.label
	if it.id == 1 {
		label, _ = t.showHide()
	}
	props := map[string]dbus.Variant{
		"label": dbus.MakeVariant(label),
	}
	if len(it.children) > 0 {
		props["children-display"] = dbus.MakeVariant("submenu")
	}
	return props
}

func (t *sni) layoutFor(it menuItem) menuLayout {
	out := menuLayout{
		ID:    it.id,
		Props: t.itemProps(it),
	}
	if len(it.children) > 0 {
		ch := make([]dbus.Variant, 0, len(it.children))
		for _, c := range it.children {
			ch = append(ch, dbus.MakeVariant(t.layoutFor(c)))
		}
		out.Children = ch
	}
	return out
}

type menuHandler struct{ t *sni }

func (m menuHandler) GetLayout(parentID, recursionDepth int32, propertyNames []string) (uint32, menuLayout, *dbus.Error) {
	m.t.mu.Lock()
	items := m.t.menu
	rev := m.t.revision
	m.t.mu.Unlock()
	if parentID == 0 {
		children := make([]dbus.Variant, 0, len(items))
		for _, it := range items {
			children = append(children, dbus.MakeVariant(m.t.layoutFor(it)))
		}
		return rev, menuLayout{
			ID:       0,
			Props:    map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")},
			Children: children,
		}, nil
	}
	it, ok := findByID(items, parentID)
	if !ok {
		return rev, menuLayout{ID: parentID}, nil
	}
	return rev, m.t.layoutFor(it), nil
}

func (m menuHandler) GetGroupProperties(ids []int32, propertyNames []string) ([]menuItemProps, *dbus.Error) {
	m.t.mu.Lock()
	items := m.t.menu
	m.t.mu.Unlock()
	var out []menuItemProps
	if len(ids) == 0 {
		walkItems(items, func(it menuItem) {
			out = append(out, menuItemProps{ID: it.id, Props: m.t.itemProps(it)})
		})
		return out, nil
	}
	for _, id := range ids {
		if it, ok := findByID(items, id); ok {
			out = append(out, menuItemProps{ID: it.id, Props: m.t.itemProps(it)})
		}
	}
	return out, nil
}

func walkItems(items []menuItem, fn func(menuItem)) {
	for _, it := range items {
		fn(it)
		if len(it.children) > 0 {
			walkItems(it.children, fn)
		}
	}
}

func (m menuHandler) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	m.t.mu.Lock()
	items := m.t.menu
	m.t.mu.Unlock()
	if it, ok := findByID(items, id); ok {
		if v, ok := m.t.itemProps(it)[name]; ok {
			return v, nil
		}
		return dbus.Variant{}, dbus.MakeFailedError(errors.New("property not found"))
	}
	return dbus.Variant{}, dbus.MakeFailedError(errors.New("item not found"))
}

func (m menuHandler) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	m.t.mu.Lock()
	items := m.t.menu
	m.t.mu.Unlock()
	it, ok := findByID(items, id)
	if !ok || it.sep {
		return nil
	}
	cmd := it.cmd
	if it.id == 1 {
		_, cmd = m.t.showHide()
	}
	select {
	case m.t.cmds <- cmd:
		m.t.doWake()
	default:
	}
	return nil
}

type eventTuple struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

func (m menuHandler) EventGroup(events []eventTuple) ([]int32, *dbus.Error) {
	for _, e := range events {
		_ = m.Event(e.ID, e.EventID, e.Data, e.Timestamp)
	}
	return nil, nil
}

func (m menuHandler) AboutToShow(id int32) (bool, *dbus.Error) {
	return false, nil
}

func (m menuHandler) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return nil, nil, nil
}

func shield(c color.RGBA) []byte {
	const sz = 22
	out := make([]byte, sz*sz*4)
	for y := range sz {
		for x := range sz {
			n := 0
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					fx := float64(x) + 0.25 + float64(dx)*0.5
					fy := float64(y) + 0.25 + float64(dy)*0.5
					if inShield(fx, fy) {
						n++
					}
				}
			}
			a := float64(n) / 4
			i := (y*sz + x) * 4
			out[i+0] = byte(a * 255)
			out[i+1] = c.R
			out[i+2] = c.G
			out[i+3] = c.B
		}
	}
	return out
}

func inShield(x, y float64) bool {
	if y < 2 || y > 21 {
		return false
	}
	if y < 9 {
		t := (y - 2) / 7
		half := t * 9
		return x >= 11-half && x <= 11+half
	}
	dx := (x - 11) / 9
	dy := (y - 9) / 12
	return dx*dx+dy*dy <= 1
}

var (
	pmUp   = shield(color.RGBA{0x5D, 0xBF, 0x8E, 0xFF})
	pmDown = shield(color.RGBA{0xC0, 0x20, 0x20, 0xFF})
)
