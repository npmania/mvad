package udp2tcp

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestForwardRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()

	srv := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		srv <- c
	}()

	tcp, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	go Forward(udp, tcp)

	peer := <-srv
	defer peer.Close()

	client, err := net.DialUDP("udp", nil, udp.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	payload := []byte("hello, mullvad")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}

	frame := make([]byte, 2+len(payload))
	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(peer, frame); err != nil {
		t.Fatal(err)
	}
	if int(frame[0])<<8|int(frame[1]) != len(payload) {
		t.Fatalf("framed length: got %d want %d", int(frame[0])<<8|int(frame[1]), len(payload))
	}
	if !bytes.Equal(frame[2:], payload) {
		t.Fatalf("framed body: got %q want %q", frame[2:], payload)
	}

	reply := []byte("world")
	out := append([]byte{0, byte(len(reply))}, reply...)
	if _, err := peer.Write(out); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, len(reply))
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Read(got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, reply) {
		t.Fatalf("reply: got %q want %q", got, reply)
	}
}
