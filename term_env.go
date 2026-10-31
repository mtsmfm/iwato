package main

import (
	"context"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"

	"iwato/internal/openshell/client"
)

// The terminals' environment beyond what the sandbox was created with.
// OpenShell fixes a sandbox's environment when it creates it (no update,
// and a provider's values are placeholders), so Iwato keeps a file in the
// sandbox user's home, ~/.config/iwato/env, which the standard image's
// shell init reads (sandboximage/etc/env.sh, from /etc/profile.d and
// /etc/bash.bashrc, before the owner's ~/.profile and ~/.bashrc).
//
// Each line is conditional on a provider's variable, which OpenShell's
// supervisor puts in the environment of the main process and of every exec
// once the provider is attached (and takes away once it is detached): the
// main terminal sees the providers of each start, the extra terminals
// those of the supervisor's last sync. A line that depends on the kind of
// provider, not only on its variable, is in the file only while such a
// provider is attached: Iwato writes the file on every start and after
// every attach or detach on a running sandbox (when it differs). A value
// set before (the sandbox's environment, the parent shell) is kept, and the
// owner's rc files, read later, still win. The one gap is the first start
// of a new home volume: the main shell may start before Iwato's first write.

// termEnvPath is the file, relative to the home.
const termEnvPath = ".config/iwato/env"

// termEnvContent is the file: grok's GROK_AUTH_PROVIDER_COMMAND when the
// xAI subscription provider gives XAI_OAUTH_TOKEN, Codex's auth.json with
// the Codex sign-in provider (codexAuthLines), with a Claude sign-in
// provider (claudeSignIn) its scopes, as both Claude providers give
// CLAUDE_CODE_OAUTH_TOKEN, and TZ for a zone other than UTC.
func termEnvContent(claudeSignIn bool, zone string) string {
	s := "# Written by Iwato; edits here are lost.\n" +
		"# Set your own values in ~/.bashrc (read later).\n" +
		termEnvIf(grokOAuthCredentialKey, "GROK_AUTH_PROVIDER_COMMAND", grokAuthCommand) +
		codexAuthLines +
		// Claude Code scrolls 3 lines per wheel report only in the terminals it
		// knows (VS Code, Windows Terminal); 1 elsewhere.
		termEnvDefault("CLAUDE_CODE_SCROLL_SPEED", "3")
	if zone != "" && zone != utcZone {
		s += termEnvDefault("TZ", zone)
	}
	if claudeSignIn {
		s += termEnvIf(claudeOAuthCredentialKey, "CLAUDE_CODE_OAUTH_SCOPES", claudeCodeScopes)
	}
	return s
}

// claudeCodeScopes tells Claude Code what the sign-in token may do: it
// takes a token from CLAUDE_CODE_OAUTH_TOKEN as inference-only otherwise
// and hides /usage (user:profile). user:mcp_servers is left out: claude.ai
// connectors go through mcp-proxy.anthropic.com, which the profile does
// not allow.
var claudeCodeScopes = strings.Join(slices.DeleteFunc(strings.Fields(claudeScopes), func(s string) bool { return s == "user:mcp_servers" }), " ")

// hasClaudeSignIn tells whether a provider of the Claude sign-in profile
// is among the attached ones.
func hasClaudeSignIn(list []ProviderInfo, attached []string) bool {
	for _, p := range list {
		if slices.Contains(attached, p.Name) && providerBase(p) == claudeSubscriptionProfileID {
			return true
		}
	}
	return false
}

// termEnvIf is a line that exports name=value when the variable cond is
// set and name is not.
func termEnvIf(cond, name, value string) string {
	return `if [ -n "${` + cond + `-}" ] && [ -z "${` + name + `-}" ]; then export ` + name + "=" + shellQuote(value) + "; fi\n"
}

// termEnvDefault is a line that exports name=value when name is not set.
func termEnvDefault(name, value string) string {
	return `if [ -z "${` + name + `-}" ]; then export ` + name + "=" + shellQuote(value) + "; fi\n"
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// termEnvScript writes the file ($1) as the sandbox user when it differs,
// without going through a link (the directories are the user's own, made
// when missing; the file is replaced by a rename).
const termEnvScript = exportHome + `set -u
umask 077
h=${HOME:-}
case $h in /*) ;; *) exit 0 ;; esac
[ -d "$h" ] && [ ! -L "$h" ] && [ -O "$h" ] && [ -w "$h" ] || exit 0
f=$h/` + termEnvPath + `
own() {
  [ -L "$h/$1" ] && return 1
  [ -e "$h/$1" ] || mkdir -m 700 "$h/$1" 2>/dev/null || return 1
  [ -d "$h/$1" ] && [ ! -L "$h/$1" ] && [ -O "$h/$1" ] && [ -w "$h/$1" ]
}
own .config && own .config/iwato || { echo "~/.config/iwato is not a directory of the user's own" >&2; exit 1; }
t=$h/.config/iwato/.env.$$
rm -f "$t"
set -C
printf '%s' "$1" >"$t" || exit 1
if [ -f "$f" ] && [ ! -L "$f" ] && cmp -s "$t" "$f" 2>/dev/null; then rm -f "$t"; else mv -f "$t" "$f"; fi
`

// writeTermEnv writes the file in a started sandbox for its providers. It
// only logs what goes wrong: a sandbox without a shell or a home still
// starts.
func (o *OpenShell) writeTermEnv(ctx context.Context, name string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, conn, err := o.clients(ctx)
	if err != nil {
		log.Printf("terminal env %s: %v", name, english(err))
		return
	}
	if conn == nil { // a test's fake sandbox API
		return
	}
	// Without the list the file still gets the lines that need no kind.
	list, attached, err := o.ListProviders(ctx)
	if err != nil {
		log.Printf("terminal env %s: %v", name, english(err))
	}
	o.tzMu.Lock()
	tz := o.timezone
	o.tzMu.Unlock()
	content := termEnvContent(hasClaudeSignIn(list, attached[name]), sandboxTimezone(tz))
	p, err := client.Start(ctx, conn, openshellWorkspace, name, []string{"/bin/sh", "-c", termEnvScript, "iwato-term-env", content}, client.Options{Env: termEnv})
	if err != nil {
		log.Printf("terminal env %s: %v", name, english(err))
		return
	}
	defer p.Close()
	p.CloseWrite()
	out, _ := io.ReadAll(p)
	code, err := p.Wait()
	if s := strings.TrimSpace(string(out)); s != "" || code != 0 || err != nil {
		log.Printf("terminal env %s: exit %d %v %s", name, code, english(err), s)
	}
}

// SetTimezone changes the sandboxes' time zone setting (Config.Timezone).
func (o *OpenShell) SetTimezone(setting string) {
	o.tzMu.Lock()
	o.timezone = setting
	o.tzMu.Unlock()
}

// RewriteTermEnv writes the file again in those of names that run.
func (o *OpenShell) RewriteTermEnv(ctx context.Context, names []string) {
	api, err := o.sandboxes(ctx)
	if err != nil {
		log.Printf("terminal env: %v", english(err))
		return
	}
	for _, name := range names {
		if sb, err := api.Get(ctx, openshellWorkspace, name); err == nil && sb.Status.Phase == v1.SandboxReady {
			o.writeTermEnv(ctx, name)
		}
	}
}
