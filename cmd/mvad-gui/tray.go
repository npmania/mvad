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
}

func sniWatcherOwned(conn *dbus.Conn) bool {
	var has bool
	err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has)
	return err == nil && has
}

func startSNI(ctx context.Context, conn *dbus.Conn, polls <-chan pollResult, windowState <-chan bool, cmds chan<- trayCmd) (*sni, error) {
	busName := "org.kde.StatusNotifierItem-" + strconv.Itoa(os.Getpid()) + "-1"
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("dbus: bus name %s not primary (%v)", busName, reply)
	}

	t := &sni{conn: conn, busName: busName, cmds: cmds}

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
			t.mu.Unlock()
			t.revision++
			_ = t.conn.Emit(menuPath, menuIface+".LayoutUpdated", t.revision, int32(0))
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
	s.send(cmdShow)
	return nil
}

func (s sniHandler) SecondaryActivate(x, y int32) *dbus.Error {
	s.send(cmdShow)
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
	default:
	}
}

type menuItem struct {
	id    int32
	label string
	sep   bool
	cmd   trayCmd
}

var menuItems = []menuItem{
	{1, "Show", false, cmdShow},
	{2, "", true, 0},
	{3, "Connect", false, cmdConnect},
	{4, "Settings", false, cmdSettings},
	{5, "Account", false, cmdAccount},
	{6, "Split", false, cmdSplit},
	{7, "", true, 0},
	{8, "Quit", false, cmdQuit},
}

func (t *sni) showHide() (string, trayCmd) {
	t.mu.Lock()
	s := t.shown
	t.mu.Unlock()
	if s {
		return "Hide", cmdHide
	}
	return "Show", cmdShow
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
	return map[string]dbus.Variant{
		"label": dbus.MakeVariant(label),
	}
}

type menuHandler struct{ t *sni }

func (m menuHandler) GetLayout(parentID, recursionDepth int32, propertyNames []string) (uint32, menuLayout, *dbus.Error) {
	if parentID != 0 {
		return 0, menuLayout{ID: parentID}, nil
	}
	children := make([]dbus.Variant, 0, len(menuItems))
	for _, it := range menuItems {
		children = append(children, dbus.MakeVariant(menuLayout{
			ID:    it.id,
			Props: m.t.itemProps(it),
		}))
	}
	root := menuLayout{
		ID:       0,
		Props:    map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")},
		Children: children,
	}
	return 1, root, nil
}

func (m menuHandler) GetGroupProperties(ids []int32, propertyNames []string) ([]menuItemProps, *dbus.Error) {
	var out []menuItemProps
	want := ids
	if len(want) == 0 {
		for _, it := range menuItems {
			out = append(out, menuItemProps{ID: it.id, Props: m.t.itemProps(it)})
		}
		return out, nil
	}
	for _, id := range want {
		for _, it := range menuItems {
			if it.id == id {
				out = append(out, menuItemProps{ID: it.id, Props: m.t.itemProps(it)})
				break
			}
		}
	}
	return out, nil
}

func (m menuHandler) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	for _, it := range menuItems {
		if it.id == id {
			if v, ok := m.t.itemProps(it)[name]; ok {
				return v, nil
			}
			return dbus.Variant{}, dbus.MakeFailedError(errors.New("property not found"))
		}
	}
	return dbus.Variant{}, dbus.MakeFailedError(errors.New("item not found"))
}

func (m menuHandler) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	for _, it := range menuItems {
		if it.id == id && !it.sep {
			cmd := it.cmd
			if it.id == 1 {
				_, cmd = m.t.showHide()
			}
			select {
			case m.t.cmds <- cmd:
			default:
			}
			return nil
		}
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
