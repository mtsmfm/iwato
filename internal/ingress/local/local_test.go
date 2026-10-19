package local

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A browser's preconnected socket sends its request later than the header
// wait: it is still routed by its Host. The bytes read ahead reach
// whoever gets the connection.
func TestSplitWaitsForTheFirstBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken := make(chan string, 1)
	l := Split(ln, func(c net.Conn, r *http.Request) bool {
		if r.Host != "3000.dev.localhost" {
			return false
		}
		b := make([]byte, 3)
		io.ReadFull(c, b)
		taken <- string(b)
		c.Close()
		return true
	}, 50*time.Millisecond, 1<<10)
	defer l.Close()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(200 * time.Millisecond)
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: 3000.dev.localhost\r\n\r\n")
	select {
	case b := <-taken:
		if b != "GET" {
			t.Fatalf("read %q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("not taken")
	}

	// Not its host, or not HTTP: Accept returns it with every byte.
	for _, first := range []string{"GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n", "\x16\x03\x01 not http"} {
		c2, _ := net.Dial("tcp", ln.Addr().String())
		io.WriteString(c2, first)
		got, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, len(first))
		if _, err := io.ReadFull(got, b); err != nil || string(b) != first {
			t.Fatalf("%q %v", b, err)
		}
		got.Close()
		c2.Close()
	}
}

func TestParseHost(t *testing.T) {
	if tg, ok := ParseHost("5173.My-App.LOCALHOST:17600", 17600); !ok || tg.Sandbox != "my-app" || tg.Port != 5173 {
		t.Fatal(tg, ok)
	}
	if _, ok := ParseHost("65535.dev.localhost", 17600); !ok {
		t.Fatal("a Host without the port")
	}
	for _, h := range []string{"3000.xn--dev.localhost:17600", "3000.de--v.localhost:17600", "3000.9dev.localhost:17600", "3000.dev.localhost:017600", "[::1]:17600", "3000.dev.localhost:17600:17600"} {
		if _, ok := ParseHost(h, 17600); ok {
			t.Error(h)
		}
	}
}
