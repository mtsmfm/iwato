package client

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeMain serves the "openshell-main" subsystem on one end of a connection
// (the relay) and attaches a MainProcess on the other. end is called with the
// channel and the relay once attached. sizes, if not nil, gets the rows of
// each window-change.
func fakeMain(t *testing.T, end func(ch ssh.Channel, relay net.Conn), sizes chan<- uint32) *MainProcess {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	// TCP, not net.Pipe: both ends of SSH write their version first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	go func() {
		_, chans, reqs, err := ssh.NewServerConn(b, config)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, creqs, err := nc.Accept()
			if err != nil {
				return
			}
			go func() {
				for r := range creqs {
					_ = r.Reply(true, nil)
					if r.Type == "window-change" && sizes != nil && len(r.Payload) >= 8 {
						sizes <- binary.BigEndian.Uint32(r.Payload[4:8])
					}
					if r.Type == "subsystem" {
						_, _ = ch.Write([]byte("hello"))
						go end(ch, b)
					}
				}
			}()
		}
	}()
	conn, chans, reqs, err := ssh.NewClientConn(a, "sandbox", &ssh.ClientConfig{User: "sandbox", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	m, err := attach(ssh.NewClient(conn, chans, reqs), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// readEnd reads the output to its end and returns why it ended.
func readEnd(t *testing.T, m *MainProcess) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := m.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the output did not end")
		return nil
	}
}

// A main process whose channel OpenShell ends has exited; a relay that
// drops leaves it running (ErrRelayLost: attach again), and so does a
// connection Iwato closes itself.
func TestMainProcessEnd(t *testing.T) {
	exited := fakeMain(t, func(ch ssh.Channel, _ net.Conn) { _ = ch.CloseWrite(); _ = ch.Close() }, nil)
	if err := readEnd(t, exited); err != io.EOF {
		t.Errorf("channel closed: %v, want io.EOF", err)
	}
	dropped := fakeMain(t, func(_ ssh.Channel, relay net.Conn) { relay.Close() }, nil)
	if err := readEnd(t, dropped); !errors.Is(err, ErrRelayLost) {
		t.Errorf("relay dropped: %v, want ErrRelayLost", err)
	}
	closed := fakeMain(t, func(ssh.Channel, net.Conn) {}, nil)
	go func() { time.Sleep(50 * time.Millisecond); closed.Close() }()
	if err := readEnd(t, closed); err != io.EOF {
		t.Errorf("closed by Iwato: %v, want io.EOF", err)
	}
}

// Attaching shrinks the terminal by a row and restores it, in that order,
// so that the main process redraws its whole screen.
func TestMainProcessRepaint(t *testing.T) {
	sizes := make(chan uint32, 4)
	m := fakeMain(t, func(ssh.Channel, net.Conn) {}, sizes)
	defer m.Close()
	for _, want := range []uint32{23, 24} {
		select {
		case got := <-sizes:
			if got != want {
				t.Fatalf("rows %d, want %d", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no window-change to %d rows", want)
		}
	}
}
