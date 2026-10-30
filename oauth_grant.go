package main

import (
	"context"
	"log"
	"strings"
	"time"
)

// oauthGrant is what an OAuth flow (atlassian_flow.go, xai.go,
// claude_oauth.go) hands to the gateway once it has the tokens.
type oauthGrant struct {
	// log prefixes the log lines ("atlassian oauth").
	log string
	// name is the provider to create (empty: Iwato names it) or, with
	// reconnect, the one whose grant is replaced.
	name      string
	reconnect bool
	// profile and key: the provider's profile and the credential the
	// gateway refreshes; clientID the OAuth client the refresh uses.
	profile, key, clientID string
	// noRefresh is the failure without a refresh token; refreshCode and
	// refreshHint the warning's code and what it adds when the gateway's
	// first refresh fails.
	noRefresh                flowText
	refreshCode, refreshHint string
	// named reports the name created.
	named func(string)
}

// handOverGrant creates the provider (or, to reconnect, replaces its
// credential), hands the refresh token to the gateway (ConfigureRefresh)
// and forgets it, then rotates once to see that the gateway can refresh.
// It returns the flow's result: status (done or failed), message, warning
// and the refresh state.
func (s *Server) handOverGrant(ctx context.Context, g oauthGrant, tokens oauthTokens) (string, flowText, flowText, *RefreshView) {
	label := g.name
	if label == "" {
		label = "(new)"
	}
	// What the server grants (lifetimes and field names, never values).
	log.Printf("%s: %s: token response fields %s, lifetimes %v s, refresh_token %t, scope %q", g.log, label, strings.Join(tokens.Fields, ","), tokens.Lifetimes, tokens.RefreshToken != "", safeScope(tokens.Scope))
	if tokens.RefreshToken == "" {
		return "failed", g.noRefresh, flowText{}, nil
	}
	var expiresAt time.Time
	if tokens.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	material := map[string]string{"client_id": g.clientID, "refresh_token": tokens.RefreshToken}
	creds := map[string]string{g.key: tokens.AccessToken}
	// Iwato keeps neither: the gateway owns the grant (and rotates it).
	tokens = oauthTokens{}
	defer clear(material)
	defer clear(creds)
	rt := s.rt.(providerRuntime)
	ref := s.rt.(providerRefresher)
	s.providerMu.Lock()
	defer s.providerMu.Unlock()
	// A reset ended the flow (cancelOAuthFlows): nothing goes to the fresh gateway.
	if ctx.Err() != nil {
		log.Printf("%s: %s: canceled", g.log, label)
		return "canceled", coded("oauth_canceled", nil, "やめました。"), flowText{}, nil
	}
	name := g.name
	if g.reconnect {
		err := ref.DeleteRefresh(ctx, name, g.key)
		if err == nil {
			err = rt.UpdateProvider(ctx, name, creds)
		}
		if err == nil {
			err = ref.ConfigureRefresh(ctx, name, g.key, material, []string{"refresh_token"}, expiresAt)
		}
		if err != nil {
			log.Printf("%s: %s: reconnect: %v", g.log, name, english(err))
			return "failed", coded("oauth_handover_failed", map[string]any{"detail": safeCLIError(err)}, "ゲートウェイに渡せませんでした: "+safeCLIError(err)), flowText{}, nil
		}
	} else {
		var err error
		name, err = createProviderNamed(ctx, rt, name, g.profile, creds)
		if err != nil {
			log.Printf("%s: %s: create: %v", g.log, label, english(err))
			return "failed", coded("oauth_create_failed", map[string]any{"detail": safeCLIError(err)}, "プロバイダーを作れませんでした: "+safeCLIError(err)), flowText{}, nil
		}
		if g.named != nil {
			g.named(name)
		}
		if err := ref.ConfigureRefresh(ctx, name, g.key, material, []string{"refresh_token"}, expiresAt); err != nil {
			log.Printf("%s: %s: configure refresh: %v", g.log, name, english(err))
			if derr := rt.DeleteProvider(ctx, name); derr != nil {
				log.Printf("%s: %s: remove the provider again: %v", g.log, name, english(derr))
			}
			return "failed", coded("oauth_refresh_setup_failed", map[string]any{"detail": safeCLIError(err)}, "ゲートウェイに更新を設定できませんでした: "+safeCLIError(err)), flowText{}, nil
		}
	}
	clear(material)
	clear(creds)
	log.Printf("%s: %s: the gateway refreshes it now", g.log, name)
	// One refresh right away: shows that the server takes the gateway's
	// refresh and what lifetime it grants.
	var warning flowText
	info, err := ref.RotateRefresh(ctx, name, g.key)
	if err != nil {
		log.Printf("%s: %s: first refresh failed: %v", g.log, name, english(err))
		if st, serr := ref.RefreshStatus(ctx, name, g.key); serr == nil {
			info = st
		}
		state := strings.Trim(safeCode(info.Status)+" "+safeCode(info.FailureCode), " ")
		warning = coded(g.refreshCode, map[string]any{"state": state}, "接続しましたが、ゲートウェイの最初の更新が失敗しました ("+state+")。今のアクセストークンは期限まで使えますが、その後は使えなくなります。"+g.refreshHint)
	} else if !info.ExpiresAt.IsZero() && !info.LastRefreshAt.IsZero() {
		log.Printf("%s: %s: the gateway refreshed it; access token lifetime %s (the profile caps it at 8h), next refresh %s", g.log, name, info.ExpiresAt.Sub(info.LastRefreshAt).Round(time.Second), info.NextRefreshAt.Format(time.RFC3339))
	}
	message := coded("oauth_connected", map[string]any{"name": name}, name+" として接続しました。")
	if g.reconnect {
		message = coded("oauth_reconnected", nil, "再接続しました。")
		if n := s.markProviderChanged(ctx, name); n > 0 {
			message = coded("oauth_reconnected_restart", nil, "再接続しました。付けているサンドボックスで動いているプロセスは古い値のままなので、再起動してください。")
		}
	}
	return "done", message, warning, refreshView(info)
}

// oauthLife is the context of the OAuth flows started since the last
// reset; cancelOAuthFlows ends it.
func (s *Server) oauthLife() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flowCtx == nil {
		s.flowCtx, s.flowEnd = context.WithCancel(s.ctx)
	}
	return s.flowCtx
}

// cancelOAuthFlows ends every OAuth flow (a reset): a waiting one is
// canceled, so its callback is refused, and one handing its grant over
// stops. With wait it returns once no handover holds providerMu (the
// caller must not hold configMu: a handover may wait for it).
func (s *Server) cancelOAuthFlows(wait bool) {
	s.mu.Lock()
	if s.flowEnd != nil {
		s.flowEnd()
	}
	s.flowCtx, s.flowEnd = nil, nil
	s.mu.Unlock()
	canceled := coded("oauth_canceled", nil, "やめました。")
	s.oauth.mu.Lock()
	if f := s.oauth.current; f != nil {
		if f.status == "waiting" || f.status == "exchanging" {
			f.status, f.message = "canceled", canceled
		}
		f.stop()
		s.oauth.current = nil
	}
	s.oauth.mu.Unlock()
	s.claude.mu.Lock()
	if f := s.claude.current; f != nil {
		if f.status == "waiting" || f.status == "exchanging" {
			f.status, f.message = "canceled", canceled
		}
		f.stop()
		s.claude.current = nil
	}
	s.claude.mu.Unlock()
	s.xai.mu.Lock()
	if f := s.xai.current; f != nil {
		if f.status == "waiting" || f.status == "exchanging" {
			f.status, f.message = "canceled", canceled
		}
		f.cancel()
		s.xai.current = nil
	}
	s.xai.mu.Unlock()
	if wait {
		s.providerMu.Lock()
		//lint:ignore SA2001 a barrier: a handover in progress ends first
		s.providerMu.Unlock()
	}
}

// flowText is an OAuth flow's message or warning: its Japanese text, and
// the code and params the page translates (ADR 0013) when it has one.
type flowText struct {
	Text   string
	Code   string
	Params map[string]any
}

func coded(code string, params map[string]any, text string) flowText {
	return flowText{Text: text, Code: code, Params: params}
}

// flowCodes are a flow view's codes for its message and warning.
type flowCodes struct {
	MessageCode   string         `json:"messageCode,omitempty"`
	MessageParams map[string]any `json:"messageParams,omitempty"`
	WarningCode   string         `json:"warningCode,omitempty"`
	WarningParams map[string]any `json:"warningParams,omitempty"`
}

func codesOf(message, warning flowText) flowCodes {
	return flowCodes{MessageCode: message.Code, MessageParams: message.Params, WarningCode: warning.Code, WarningParams: warning.Params}
}
