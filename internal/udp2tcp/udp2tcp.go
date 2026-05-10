// Package udp2tcp implements Mullvad's UDP-over-TCP framing:
// each datagram is preceded by a 2-byte big-endian length.
// See https://github.com/mullvad/udp-over-tcp.
package udp2tcp

import (
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
)

const maxFrame = 65535

// Forward shuttles UDP datagrams between udp and tcp until either side errors.
// The first received UDP source becomes the destination for tcp->udp datagrams,
// and is updated on every udp->tcp packet.
func Forward(udp *net.UDPConn, tcp net.Conn) error {
	var dst atomic.Pointer[net.UDPAddr]
	errc := make(chan error, 2)

	go func() {
		buf := make([]byte, maxFrame+2)
		for {
			n, src, err := udp.ReadFromUDP(buf[2:])
			if err != nil {
				errc <- err
				return
			}
			dst.Store(src)
			binary.BigEndian.PutUint16(buf[:2], uint16(n))
			if _, err := tcp.Write(buf[:2+n]); err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() {
		var hdr [2]byte
		buf := make([]byte, maxFrame)
		for {
			if _, err := io.ReadFull(tcp, hdr[:]); err != nil {
				errc <- err
				return
			}
			n := binary.BigEndian.Uint16(hdr[:])
			if _, err := io.ReadFull(tcp, buf[:n]); err != nil {
				errc <- err
				return
			}
			to := dst.Load()
			if to == nil {
				continue
			}
			if _, err := udp.WriteToUDP(buf[:n], to); err != nil {
				errc <- err
				return
			}
		}
	}()

	return <-errc
}
