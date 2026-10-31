package main

import (
	"context"
	"time"
)

// Gateway-owned refresh of a provider's credential (OpenShell's
// ConfigureProviderRefresh and friends): Iwato hands the gateway an OAuth
// grant once, and the gateway mints the access tokens from then on.
// OpenShell and Fake implement providerRefresher.

// RefreshInfo is a credential's refresh state, without values.
type RefreshInfo struct {
	// Status is the gateway's: configured, refreshed, error,
	// reauthorization_required, configuration_required,
	// investigation_required; not_configured when there is none.
	Status                                               string
	ExpiresAt, NextRefreshAt, LastRefreshAt, LastErrorAt time.Time
	// Recovery: retry, reauthorize, fix_configuration, investigate.
	Recovery, FailureCode, LastError string
}

type providerRefresher interface {
	// ConfigureRefresh starts a new authorization (a new placeholder).
	ConfigureRefresh(ctx context.Context, provider, key string, material map[string]string, secretKeys []string, expiresAt time.Time) error
	// DeleteRefresh returns the credential to manual management (no error
	// when there is no refresh).
	DeleteRefresh(ctx context.Context, provider, key string) error
	RotateRefresh(ctx context.Context, provider, key string) (RefreshInfo, error)
	RefreshStatus(ctx context.Context, provider, key string) (RefreshInfo, error)
}

// RefreshView is RefreshInfo for the page.
type RefreshView struct {
	Status        string `json:"status"`
	Recovery      string `json:"recovery,omitempty"`
	FailureCode   string `json:"failureCode,omitempty"`
	Error         string `json:"error,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
	NextRefreshAt string `json:"nextRefreshAt,omitempty"`
	LastRefreshAt string `json:"lastRefreshAt,omitempty"`
}

func refreshView(i RefreshInfo) *RefreshView {
	at := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(time.RFC3339)
	}
	v := &RefreshView{Status: safeCode(i.Status), Recovery: safeCode(i.Recovery), FailureCode: safeCode(i.FailureCode), ExpiresAt: at(i.ExpiresAt), NextRefreshAt: at(i.NextRefreshAt), LastRefreshAt: at(i.LastRefreshAt)}
	if i.LastError != "" {
		v.Error = truncate(safeCLIError(errorString(i.LastError)), 300)
	}
	return v
}

type errorString string

func (e errorString) Error() string { return string(e) }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
