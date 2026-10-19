package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"iwato/internal/termsync/termsynctest"
)

// wsTerm carries termsync messages over the page's WebSocket as the page
// does: one binary message each, its kind byte and then the payload.
type wsTerm struct {
	ws  *websocket.Conn
	buf []byte
}

func (c *wsTerm) Read(b []byte) (int, error) {
	for len(c.buf) == 0 {
		kind, p, err := c.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		if kind != websocket.BinaryMessage || len(p) == 0 {
			continue
		}
		head := []byte{p[0], 0, 0, 0, 0}
		binary.BigEndian.PutUint32(head[1:], uint32(len(p)-1))
		c.buf = append(head, p[1:]...)
	}
	n := copy(b, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// Write takes one whole termsync message (termsync.WriteMessage).
func (c *wsTerm) Write(b []byte) (int, error) {
	if err := c.ws.WriteMessage(websocket.BinaryMessage, append([]byte{b[0]}, b[5:]...)); err != nil {
		return 0, err
	}
	return len(b), nil
}

// viewWS attaches to the terminal on ws at cols×rows.
func viewWS(t *testing.T, ws *websocket.Conn, cols, rows int) *termsynctest.Client {
	t.Helper()
	v, err := termsynctest.Start(&wsTerm{ws: ws}, cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// waitText waits until want shows on the terminal (its screen or the
// history seen) and returns the text.
func waitText(t *testing.T, v *termsynctest.Client, want string) string {
	t.Helper()
	text, err := v.WaitFor(want, 10*time.Second)
	if err != nil {
		t.Fatalf("waiting for %q: %v; got %q", want, err, strings.TrimSpace(text))
	}
	return text
}
