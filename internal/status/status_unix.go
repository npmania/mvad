//go:build linux || darwin

package status

import (
	"errors"
	"net/netip"
	"os"

	"golang.zx2c4.com/wireguard/wgctrl"
)

func isPermErr(err error) bool {
	return errors.Is(err, os.ErrPermission)
}

// peer fills in the WireGuard peer of s.Iface. A caller denied access
// to the device gets s back unchanged.
func peer(s Snapshot) (Snapshot, error) {
	c, err := wgctrl.New()
	if err != nil {
		if isPermErr(err) {
			return s, nil
		}
		return s, err
	}
	defer c.Close()
	d, err := c.Device(s.Iface)
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
			// The userspace UAPI path yields 16-byte IPv4 addresses.
			ap := p.Endpoint.AddrPort()
			s.PeerEndpoint = netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
		}
	}
	return s, nil
}
