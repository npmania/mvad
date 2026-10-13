package main

import "github.com/npmania/mvad/internal/split"

type menuItem struct {
	id       int32
	label    string
	sep      bool
	cmd      trayCmd
	children []menuItem
}

func buildTrayMenu(favorites []string, up bool) []menuItem {
	items := []menuItem{
		{id: 1, label: "Show", cmd: trayCmd{kind: cmdShow}},
		{id: 2, sep: true},
	}
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
		items = append(items, menuItem{id: 201, sep: true})
	}
	if up {
		items = append(items,
			menuItem{id: 10, label: "Disconnect", cmd: trayCmd{kind: cmdDisconnect}},
			menuItem{id: 9, label: "Reconnect", cmd: trayCmd{kind: cmdReconnect}},
		)
	} else {
		items = append(items,
			menuItem{id: 3, label: "Connect", cmd: trayCmd{kind: cmdConnect}},
		)
	}
	items = append(items,
		menuItem{id: 4, label: "Settings", cmd: trayCmd{kind: cmdSettings}},
		menuItem{id: 5, label: "Account", cmd: trayCmd{kind: cmdAccount}},
	)
	if split.Supported {
		items = append(items, menuItem{id: 6, label: "Split", cmd: trayCmd{kind: cmdSplit}})
	}
	items = append(items,
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
