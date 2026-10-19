package main

import (
	"context"
	"io"
	"log"
	"strings"
	"time"

	"iwato/internal/openshell/client"
)

// agentFilesScript places the agents' global instruction files in the
// sandbox user's home so that they read /AGENTS.md ($1), which neither Claude
// Code nor Codex reads by itself: ~/.claude/CLAUDE.md importing it, and
// ~/.codex/AGENTS.md linking to it. It runs as the sandbox user (so the
// files are the user's), places a file only when there is none (a
// dangling link counts), never goes through a link to a directory, and
// does nothing when the home or /AGENTS.md does not fit (any image).
const agentFilesScript = exportHome + `set -u
umask 022
a=$1
h=${HOME:-}
case $h in /*) ;; *) exit 0 ;; esac
[ -d "$h" ] && [ ! -L "$h" ] && [ -O "$h" ] && [ -w "$h" ] || exit 0
[ -f "$a" ] && [ -r "$a" ] || exit 0
# own DIR: $h/DIR, a directory of the user's own (made when missing).
own() {
  [ -L "$h/$1" ] && return 1
  [ -e "$h/$1" ] || mkdir -m 700 "$h/$1" 2>/dev/null || return 1
  [ -d "$h/$1" ] && [ ! -L "$h/$1" ] && [ -O "$h/$1" ] && [ -w "$h/$1" ]
}
absent() { [ ! -e "$1" ] && [ ! -L "$1" ]; }
set -C
if own .claude && absent "$h/.claude/CLAUDE.md"; then
  printf '@%s\n' "$a" >"$h/.claude/CLAUDE.md" 2>/dev/null && echo "placed ~/.claude/CLAUDE.md"
fi
if own .codex && absent "$h/.codex/AGENTS.md"; then
  ln -s "$a" "$h/.codex/AGENTS.md" 2>/dev/null && echo "placed ~/.codex/AGENTS.md"
fi
exit 0
`

// placeAgentFiles runs agentFilesScript in a started sandbox. It only logs
// what goes wrong: a sandbox without a shell or a home still starts.
func (o *OpenShell) placeAgentFiles(ctx context.Context, w Sandbox) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, conn, err := o.clients(ctx)
	if err != nil {
		log.Printf("agent files %s: %v", w.Name, english(err))
		return
	}
	if conn == nil { // a test's fake sandbox API
		return
	}
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"/bin/sh", "-c", agentFilesScript, "iwato-agent-files", "/AGENTS.md"}, client.Options{Env: termEnv})
	if err != nil {
		log.Printf("agent files %s: %v", w.Name, english(err))
		return
	}
	defer p.Close()
	p.CloseWrite()
	out, _ := io.ReadAll(p)
	code, err := p.Wait()
	if s := strings.TrimSpace(string(out)); s != "" || code != 0 || err != nil {
		log.Printf("agent files %s: exit %d %v %s", w.Name, code, english(err), s)
	}
}
