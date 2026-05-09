//go:build linux

package status

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl"
)

func isPermErr(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

func read(iface string) (Snapshot, error) {
	state, err := os.ReadFile(filepath.Join("/sys/class/net", iface, "operstate"))
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, ErrNotConnected
	}
	if err != nil {
		return Snapshot{}, err
	}
	oper := strings.TrimSpace(string(state))
	s := Snapshot{
		Iface:     iface,
		OperState: oper,
		Up:        oper == "up" || oper == "unknown",
	}
	c, err := wgctrl.New()
	if err != nil {
		if isPermErr(err) {
			return s, nil
		}
		return s, err
	}
	defer c.Close()
	d, err := c.Device(iface)
	if err != nil {
		if isPermErr(err) {
			return s, nil
		}
		return s, err
	}
	if len(d.Peers) > 0 {
		p := d.Peers[0]
		s.PeerKey = p.PublicKey
		s.RxBytes = p.ReceiveBytes
		s.TxBytes = p.TransmitBytes
		s.LastHandshake = p.LastHandshakeTime
		if p.Endpoint != nil {
			s.PeerEndpoint = p.Endpoint.AddrPort()
		}
	}
	return s, nil
}
