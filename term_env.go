package main

import (
	"context"
	"io"
	"log"
	"strings"
	"time"

	"iwato/internal/openshell/client"
)

// The terminals' environment beyond what the sandbox was created with.
// OpenShell fixes a sandbox's environment when it creates it (no update,
// and a provider's values are placeholders), so Iwato keeps a file in the
// sandbox user's home, ~/.config/iwato/env, which the standard image's
// shell init reads (sandboximage/etc/env.sh, from /etc/profile.d and
// /etc/bash.bashrc, before the owner's ~/.profile and ~/.bashrc).
//
// Its content does not depend on the providers attached: each line is
// conditional on a provider's variable, which OpenShell's supervisor puts
// in the environment of the main process and of every exec once the
// provider is attached (and takes away once it is detached). So the file
// only has to exist: the main terminal sees the providers of each start,
// the extra terminals those of the supervisor's last sync. A value set
// before (the sandbox's environment, the parent shell) is kept, and the
// owner's rc files, read later, still win. Iwato writes the file on every
// start (when it differs). The one gap is the first start of a new home
// volume: the main shell may start before Iwato's first write.

// termEnvPath is the file, relative to the home.
const termEnvPath = ".config/iwato/env"

// termEnvContent is the file: grok's GROK_AUTH_PROVIDER_COMMAND when the
// xAI subscription provider gives XAI_OAUTH_TOKEN.
var termEnvContent = "# Written by Iwato when the sandbox starts; edits here are lost.\n" +
	"# Set your own values in ~/.bashrc (read later).\n" +
	termEnvIf(grokOAuthCredentialKey, "GROK_AUTH_PROVIDER_COMMAND", grokAuthCommand)

// termEnvIf is a line that exports name=value when the variable when is
// set and name is not.
func termEnvIf(when, name, value string) string {
	return `if [ -n "${` + when + `-}" ] && [ -z "${` + name + `-}" ]; then export ` + name + "=" + shellQuote(value) + "; fi\n"
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

// writeTermEnv writes the file in a started sandbox. It only logs what
// goes wrong: a sandbox without a shell or a home still starts.
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
	p, err := client.Start(ctx, conn, openshellWorkspace, name, []string{"/bin/sh", "-c", termEnvScript, "iwato-term-env", termEnvContent}, client.Options{Env: termEnv})
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
