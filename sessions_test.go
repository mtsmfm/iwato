package main

import (
	"sync"
)

// endSpy keeps the error that ended the WebSocket (its close frame).
type endSpy struct {
	*wsTerm
	mu  sync.Mutex
	err error
}

func (e *endSpy) Read(b []byte) (int, error) {
	n, err := e.wsTerm.Read(b)
	if err != nil {
		e.mu.Lock()
		e.err = err
		e.mu.Unlock()
	}
	return n, err
}
