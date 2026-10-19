package dav

import (
	"encoding/binary"
	"io"
	"sync"
	"time"
)

// meter watches one SFTP channel's bytes. Every SFTP v3 request gets
// exactly one response, so the packets sent and received tell whether the
// server owes answers, and the time of the last byte from it whether it is
// still working on them. pkg/sftp has no timeouts of its own: a request
// the server never answers waits forever, and so does everything queued
// behind it (OpenShell serves a channel's requests one after another).
type meter struct {
	mu       sync.Mutex
	in, out  framer
	sent     int64     // requests counted as sent (when their bytes go out)
	received int64     // responses read completely
	progress time.Time // the last byte from the server, or the first unanswered request
}

// owed reports how many requests wait for an answer and since when the
// server sent nothing.
func (m *meter) owed() (int64, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent - m.received, m.progress
}

// framer counts whole packets in a stream of SFTP packets (a 4-byte
// length, then that many bytes), however the stream is cut.
type framer struct {
	hdr  [4]byte
	nhdr int
	left uint32
}

func (f *framer) feed(b []byte) (packets int64) {
	for len(b) > 0 {
		if f.left == 0 {
			k := copy(f.hdr[f.nhdr:], b)
			f.nhdr += k
			b = b[k:]
			if f.nhdr < 4 {
				break
			}
			f.nhdr = 0
			f.left = binary.BigEndian.Uint32(f.hdr[:])
			if f.left == 0 {
				packets++
			}
			continue
		}
		k := f.left
		if uint64(len(b)) < uint64(k) {
			k = uint32(len(b))
		}
		f.left -= k
		b = b[k:]
		if f.left == 0 {
			packets++
		}
	}
	return packets
}

type meterReader struct {
	r io.Reader
	m *meter
}

func (r meterReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if n > 0 {
		r.m.mu.Lock()
		r.m.received += r.m.in.feed(b[:n])
		r.m.progress = time.Now()
		r.m.mu.Unlock()
	}
	return n, err
}

type meterWriter struct {
	w io.WriteCloser
	m *meter
}

// Write counts the bytes before they go: a write the server does not take
// (its window is full because it stopped reading) is owed an answer too.
func (w meterWriter) Write(b []byte) (int, error) {
	w.m.mu.Lock()
	if w.m.sent <= w.m.received {
		w.m.progress = time.Now()
	}
	w.m.sent += w.m.out.feed(b)
	w.m.mu.Unlock()
	return w.w.Write(b)
}

func (w meterWriter) Close() error { return w.w.Close() }
