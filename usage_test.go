package main

import (
	"testing"
	"time"
)

func TestParseUsage(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	c, err := parseClaudeUsage([]byte(`{"five_hour":{"utilization":42.7,"resets_at":"2026-11-11T13:30:00+00:00"},"seven_day":{"utilization":18,"resets_at":"2026-11-14T00:00:00Z"},"seven_day_opus":null,"extra_usage":{"is_enabled":false}}`), now)
	if err != nil || len(c) != 2 || c[0].Kind != "5h" || c[0].Used != 42.7 || !c[0].ResetsAt.Equal(now.Add(90*time.Minute)) || c[1].Kind != "7d" {
		t.Fatalf("claude: %+v %v", c, err)
	}
	// Other models' weeks are rows of limits[]; one a fixed key has is not repeated.
	c, err = parseClaudeUsage([]byte(`{"five_hour":null,"seven_day":{"utilization":52,"resets_at":"2026-11-13T18:00:00.848550+00:00"},"seven_day_opus":{"utilization":5,"resets_at":null},"limits":[{"kind":"weekly_all","percent":52},{"kind":"weekly_scoped","percent":74,"resets_at":"2026-11-13T17:59:00Z","scope":{"model":{"display_name":"Fable"}}},{"kind":"weekly_scoped","percent":5,"scope":{"model":{"display_name":"Opus"}}},{"kind":"weekly_scoped","percent":1,"scope":{"surface":{"display_name":"x"}}}]}`), now)
	if err != nil || len(c) != 3 || c[0].Kind != "7d" || c[0].ResetsAt.IsZero() || c[1].Kind != "7d-opus" || c[2].Kind != "7d-model" || c[2].Model != "Fable" || c[2].Used != 74 || c[2].ResetsAt.Hour() != 17 {
		t.Fatalf("claude limits: %+v %v", c, err)
	}
	x, err := parseCodexUsage([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":3,"limit_window_seconds":18000,"reset_after_seconds":600},"secondary_window":{"used_percent":88.5,"limit_window_seconds":604800,"reset_at":1794830400}}}`), now)
	if err != nil || len(x) != 2 || x[0].Kind != "5h" || !x[0].ResetsAt.Equal(now.Add(10*time.Minute)) || x[1].Kind != "7d" || x[1].Used != 88.5 || x[1].ResetsAt.Unix() != 1794830400 {
		t.Fatalf("codex: %+v %v", x, err)
	}
	// A weekly-only plan has no primary window.
	if x, err := parseCodexUsage([]byte(`{"rate_limit":{"primary_window":null,"secondary_window":{"used_percent":10,"limit_window_seconds":604800}}}`), now); err != nil || len(x) != 1 || x[0].Kind != "7d" {
		t.Fatalf("weekly only: %+v %v", x, err)
	}
	if _, err := parseClaudeUsage([]byte(`{"error":"x"}`), now); err == nil {
		t.Fatal("an answer without windows")
	}
	for out, want := range map[string]int{"{\"a\":1}\n200": 200, "{}\n401\n": 401, usageNoToken + "\n": 0} {
		if _, status, err := splitUsageOutput([]byte(out)); err != nil || status != want {
			t.Errorf("%q: %d %v", out, status, err)
		}
	}
	if _, _, err := splitUsageOutput([]byte("sh: curl: not found")); err == nil {
		t.Error("an error message is not a status")
	}
}
