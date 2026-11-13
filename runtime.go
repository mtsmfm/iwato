package main

import (
	"context"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Status struct {
	Name   string `json:"name"`
	State  string `json:"status"`
	Detail string `json:"detail"`
	// RestartPending: a provider changed while the sandbox ran; restarting
	// it gives every terminal the change.
	RestartPending bool `json:"restartPending,omitempty"`
	// RecreateToUpdate: it runs older runtime images (the supervisor) than
	// new sandboxes get, until recreated (openshell_runtime.go).
	RecreateToUpdate bool `json:"recreateToUpdate,omitempty"`
	// GPU: the sandbox is configured with a GPU (the card shows a marker).
	GPU bool `json:"gpu,omitempty"`
	// Stopped: the terminals that stopped changing (notifier.go), by name,
	// with when (Unix seconds); the page marks them until viewed.
	Stopped map[string]int64 `json:"stopped,omitempty"`
	// AutoStopAt: when the sandbox stops, after its last terminal ended
	// (autostop.go; Unix seconds), unless a terminal runs by then.
	AutoStopAt int64 `json:"autoStopAt,omitempty"`
	// Pending: the agent's proposals waiting for the owner, for the badges
	// while the policy tab is not shown (pendingInList).
	Pending *[]PendingProposal `json:"pending,omitempty"`
}

// Terminal is an attachment to a terminal session. It carries termsync
// messages (internal/termsync) both ways: Write takes whole messages, Read
// returns them as a stream; the client's hello comes first. Resize is
// unused (the client's hello and resize messages size the session).
type Terminal interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// TermRef names one of a sandbox's terminal sessions. New allows starting
// it; without it a session that ended is not started again ("main" always is).
type TermRef struct {
	Name string
	New  bool
}

const mainTerm = "main"

var termNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// validTermName accepts the names of terminal sessions.
func validTermName(name string) bool { return termNamePattern.MatchString(name) }

// orderTerms puts terminals in the order they started, the order the page
// numbers its tabs (1, 2, … whatever the names). main with no known start
// (a sandbox's main process) comes first, others with none last; among
// those, main first and numbers by their value.
func orderTerms(names []string, started func(string) (time.Time, bool)) {
	group := func(name string) (int, time.Time) {
		at, ok := started(name)
		switch {
		case ok:
			return 1, at
		case name == mainTerm:
			return 0, at
		}
		return 2, at
	}
	slices.SortStableFunc(names, func(a, b string) int {
		ga, ta := group(a)
		gb, tb := group(b)
		if ga != gb {
			return ga - gb
		}
		if c := ta.Compare(tb); c != 0 {
			return c
		}
		if (a == mainTerm) != (b == mainTerm) {
			if a == mainTerm {
				return -1
			}
			return 1
		}
		na, ea := strconv.Atoi(a)
		nb, eb := strconv.Atoi(b)
		if ea == nil && eb == nil {
			return na - nb
		}
		return strings.Compare(a, b)
	})
}

type Runtime interface {
	Status(context.Context, Sandbox) Status
	Start(context.Context, Sandbox) error
	Attach(context.Context, Sandbox, TermRef, int, int) (Terminal, error)
}
