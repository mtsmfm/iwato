package main

import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
)

// A conflict is retried while the policy itself has not changed (a fresh
// sandbox's object moving with its status), and given up at once when
// another revision appeared or the error is something else.
func TestRetryConflict(t *testing.T) {
	defer func(w time.Duration) { policyConflictWait = w }(policyConflictWait)
	policyConflictWait = time.Millisecond
	conflict := &v1.StatusError{Code: v1.ErrorConflict, Message: "persist policy revision failed due to concurrent modification"}
	run := func(fails int, failWith error, unchanged bool) (int, error) {
		n := 0
		err := retryConflict(context.Background(), func() error {
			if n++; n <= fails {
				return failWith
			}
			return nil
		}, func() bool { return unchanged })
		return n, err
	}
	if n, err := run(3, conflict, true); err != nil || n != 4 {
		t.Fatalf("retried %d times: %v", n, err)
	}
	if n, err := run(3, conflict, false); !v1.IsConflict(err) || n != 1 {
		t.Fatalf("another revision: %d tries, %v", n, err)
	}
	if n, err := run(3, errors.New("unavailable"), true); err == nil || n != 1 {
		t.Fatalf("other error: %d tries, %v", n, err)
	}
	if n, err := run(100, conflict, true); !v1.IsConflict(err) || n != policyConflictTries {
		t.Fatalf("gave up after %d tries: %v", n, err)
	}
}
