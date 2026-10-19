package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestLogRingKeepsLinesAndFollows(t *testing.T) {
	var r logRing
	fmt.Fprint(&r, "one\ntwo\r\nthr")
	if lines, next := r.since(-1); strings.Join(lines, "|") != "one|two" || next != 2 {
		t.Fatal(lines, next)
	}
	fmt.Fprint(&r, "ee\n")
	if lines, next := r.since(2); strings.Join(lines, "|") != "three" || next != 3 {
		t.Fatal(lines, next)
	}
	for i := 0; i < logRingLines+5; i++ {
		fmt.Fprintln(&r, i)
	}
	if lines, _ := r.since(0); len(lines) != logRingLines || lines[0] != "5" {
		t.Fatal(len(lines), lines[0])
	}
}

func TestPreparationShowsTheStepAndRetries(t *testing.T) {
	o := &OpenShell{ready: make(chan struct{}), dir: t.TempDir()}
	if p := o.Preparation(); p.Phase != "checking" {
		t.Fatal(p)
	}
	o.report("gateway_starting", nil, "ゲートウェイを起動しています…")
	if p := o.Preparation(); p.Phase != "building" || p.Code != "gateway_starting" || p.Message != "ゲートウェイを起動しています…" || !strings.HasSuffix(p.Log, "gateway-build.log") {
		t.Fatal(p)
	}
	if o.RetryPreparation(t.Context()) {
		t.Fatal("retried while preparing")
	}
}
