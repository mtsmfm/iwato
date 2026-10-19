#!/bin/bash
# End-to-end check of openshell-driver-wslc against the released OpenShell
# gateway, CLI, sandbox and supervisor binaries, with test/fakewslc standing
# in for wslc.exe (sandboxes run as local processes: no network isolation).
# Only in a disposable Linux dev container with passwordless sudo: it
# writes /.openshell, /sandbox, /home/dev (the sandbox's home volume, which
# the fake does not mount: made when missing and then removed at the end),
# /run/openshell*, /opt/openshell-test, and what the sandbox image carries
# (the fake's sandboxes see this machine's files): the Policy Advisor guide
# (/etc/openshell, removed at the end), /AGENTS.md (removed at the end when
# this run put it) and Iwato's daemon (/usr/local/lib/iwato/iwato-entrypoint,
# and at its legacy path iwato-term; removed at the end when this run made
# the directory), and
# needs ssh and socat (sudo apt-get install -y openssh-client socat).
#
#   internal/openshell/wslc/e2e.sh          # plaintext gateway
#   MODE=tls internal/openshell/wslc/e2e.sh # mTLS gateway, TLS driver listener behind socat
#   VERSION=dev internal/openshell/wslc/e2e.sh # OpenShell's rolling build of main
#   KEEP=1 ...                              # keep the work directory (logs) after a failure
set -u
# One run at a time per machine (worktrees share the ports, /sandbox and the
# certificates): a second run waits for the first. What runs in the
# background closes the lock (9>&-), so a leftover cannot hold it.
exec 9>/tmp/iwato-e2e.lock
flock 9
VERSION=${VERSION:-0.1.4}
TAG=v$VERSION
[ "$VERSION" = dev ] && TAG=dev
MODE=${MODE:-plain}
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
CACHE=${XDG_CACHE_HOME:-$HOME/.cache}/openshell-e2e/$VERSION
WORK=$(mktemp -d "${TMPDIR:-/tmp}/wslc-e2e.XXXXXX")
PORT=17670
fail=0
out=
# Per-section times: section NAME ends the previous one and prints its time.
t0=$SECONDS sect= sectat=$SECONDS
section() { [ -n "$sect" ] && echo "TIME $sect $((SECONDS - sectat)) s"; sect=${1:-} sectat=$SECONDS; }
# Poll until a command succeeds, every 0.2 s for at most $1 seconds.
waitfor() { local end=$((SECONDS + $1)); shift; until "$@"; do [ $SECONDS -ge $end ] && return 1; sleep 0.2; done; }
# Whether something listens on TCP port $1 (hex in /proc/net/tcp, state 0A).
listening() { awk -v p="$(printf ':%04X' "$1")" 'NR>1 && $4=="0A" && substr($2,length($2)-4)==p{f=1} END{exit !f}' /proc/net/tcp /proc/net/tcp6; }
check() { if eval "$2"; then echo "PASS $1"; else echo "FAIL $1"; echo "$out" | tail -"${TAIL:-8}" | sed 's/^/    /'; fail=1; fi; }
# On success, failure and interrupt: stop what this run started and remove
# its work directory (KEEP=1 keeps it after a failure, for the logs).
# The agents' files Iwato places in the sandbox's home (agent_files.go),
# here this machine's /sandbox: only Iwato's own are removed.
agent_files() {
  [ "$(cat /sandbox/.claude/CLAUDE.md 2>/dev/null)" = @/AGENTS.md ] && rm -f /sandbox/.claude/CLAUDE.md && rmdir /sandbox/.claude 2>/dev/null
  [ "$(readlink /sandbox/.codex/AGENTS.md)" = /AGENTS.md ] && rm -f /sandbox/.codex/AGENTS.md && rmdir /sandbox/.codex 2>/dev/null
  true
}
cleanup() {
  status=$?
  section
  local pids="${gwpid:-} ${drvpid:-} ${relay:-}"
  if [ -n "${pids// }" ]; then kill $pids 2>/dev/null; waitfor 10 eval '! kill -0 $pids 2>/dev/null'; fi
  agent_files
  sudo pkill -KILL -x openshell-sandb; sudo pkill -KILL -x openshell-super
  # Terminal daemons that attach -spawn started outlive the fake's stop
  # (sessions of their own), and their shells.
  sudo pkill -KILL -f '^/usr/local/lib/iwato/iwato-(entrypoint|term) '
  [ -n "${madeterm:-}" ] && sudo rm -rf /usr/local/lib/iwato
  [ -n "${madehome:-}" ] && sudo rm -rf /home/dev
  [ -n "${naptime:-}" ] && sudo pkill -f "^sleep $naptime\$"
  sudo rm -rf /etc/openshell
  [ -n "${madeagents:-}" ] && sudo rm -f /AGENTS.md
  # The work directory holds sandbox files owned by other users: sudo.
  if [ "$status" != 0 ] && [ -n "${KEEP:-}" ]; then echo "logs: $WORK"; else sudo rm -rf "$WORK"; fi
  echo "TIME total $((SECONDS - t0)) s"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

mkdir -p "$CACHE"
for f in openshell-gateway-x86_64-unknown-linux-gnu openshell-x86_64-unknown-linux-musl openshell-sandbox-x86_64-unknown-linux-musl openshell-supervisor-x86_64-unknown-linux-gnu; do
  [ "$VERSION" = dev ] && rm -f "$CACHE/$f.tar.gz"
  [ -f "$CACHE/$f.tar.gz" ] || curl -fsSL -o "$CACHE/$f.tar.gz" "https://github.com/NVIDIA/OpenShell/releases/download/$TAG/$f.tar.gz" || exit 1
  # A release's tarballs are checked against the gateway Dockerfile's pins.
  if [ "$VERSION" != dev ]; then
    want=$(sed -n "/^      $TAG) printf/,/pinned.sha256 ;;/s/.*'\([0-9a-f]\{64\}\)  $f.tar.gz'.*/\1/p" "$ROOT/internal/openshell/gateway/Dockerfile")
    [ -n "$want" ] || { echo "no SHA-256 pinned for $f.tar.gz ($TAG) in internal/openshell/gateway/Dockerfile" >&2; exit 1; }
    echo "$want  $CACHE/$f.tar.gz" | sha256sum --check --strict --quiet - || { rm -f "$CACHE/$f.tar.gz"; exit 1; }
  fi
  tar xzf "$CACHE/$f.tar.gz" -C "$CACHE"
done
# Leftovers of an earlier run would hold the sandbox DNS relay port.
sudo pkill -KILL -x openshell-sandb; sudo pkill -KILL -x openshell-super
# The supervisor runs as nobody: keep it outside private directories.
sudo install -D -m 0755 "$CACHE/openshell-supervisor" /opt/openshell-test/openshell-supervisor
sudo mkdir -p /run/openshell /run/openshell-supervisor-ca/material
sudo chown 65534:65534 /run/openshell
sudo chown 1000:1000 /run/openshell-supervisor-ca/material
# The sandbox's home (Iwato's homeDir) is a volume the fake does not mount:
# this machine's /home/dev stands for it, so that the shells and the agents'
# files find it. Made for this run when missing (an existing one is the
# owner's: the Go e2e refuses to change its agents' files).
# Without the default ACL it may inherit from /home (a volume has none),
# which would override the umask of what the sandbox writes there.
if [ ! -e /home/dev ]; then
  sudo install -d -o 1000 -g 1000 -m 0755 /home/dev && madehome=1
  command -v setfacl >/dev/null && sudo setfacl -b -k /home/dev
fi

# What the sandbox image carries (here on this machine): removed at the end
# when this run puts it.
[ -e /usr/local/lib/iwato ] || madeterm=1
[ -e /AGENTS.md ] || [ -L /AGENTS.md ] || madeagents=1

section build
# Everything Go is built here, with the real HOME (its toolchain and caches):
# nothing runs go after HOME moves below. The e2e tests are one test binary.
GO=$(cd "$ROOT" && go env GOROOT)/bin/go
(cd "$ROOT" && "$GO" build -o "$WORK/" ./cmd/openshell-driver-wslc ./internal/openshell/wslc/fakewslc ./internal/openshell/wslc/e2eprobe &&
  mv "$WORK/openshell-driver-wslc" "$WORK/drv" && mv "$WORK/e2eprobe" "$WORK/probe" &&
  CGO_ENABLED=0 "$GO" build -o "$WORK/iwato-entrypoint" ./cmd/iwato-entrypoint &&
  "$GO" test -c -o "$WORK/iwato.test" .) || exit 1
mkdir -p "$WORK/fake" "$WORK/state" "$WORK/db" "$WORK/home"
# The image's daemon at its path, and at the legacy path, where an older
# driver put it in the sandboxes it made (the driver now places it there on
# their starts: -legacy-term below); the Policy Advisor guide and /AGENTS.md.
sudo install -D -m 0555 "$WORK/iwato-entrypoint" /usr/local/lib/iwato/iwato-entrypoint
sudo install -D -m 0555 "$WORK/iwato-entrypoint" /usr/local/lib/iwato/iwato-term
sudo mkdir -p /etc/openshell/skills && sudo cp -r "$ROOT/sandboximage/openshell-skills/." /etc/openshell/skills/ && sudo chmod -R a+rX /etc/openshell
[ -n "${madeagents:-}" ] && sudo install -m 0444 "$ROOT/sandboximage/AGENTS.md" /AGENTS.md
# iwato's sandbox image names its user (dev), so the driver resolves it;
# its WORKDIR is the default workspace, /sandbox (the image convention).
export FAKE_IMAGES=iwato-sandbox:0.1.0=dev
export FAKE_WSLC_STATE=$WORK/fake FAKE_SANDBOX_BIN=${SANDBOX_BIN:-$CACHE/openshell-sandbox} FAKE_SUPERVISOR_BIN=/opt/openshell-test/openshell-supervisor
export HOME=$WORK/home OPENSHELL_TELEMETRY_ENABLED=false
# The XDG directories would win over HOME (GitHub's runners set
# XDG_CONFIG_HOME, so the CLI would miss the gateway's mTLS files below).
unset XDG_CONFIG_HOME XDG_DATA_HOME XDG_STATE_HOME XDG_CACHE_HOME
"$CACHE/openshell-gateway" generate-certs --output-dir "$WORK/tls" --server-san 127.0.0.1 >/dev/null

cat >"$WORK/gateway.toml" <<EOF
[openshell]
version = 2
[openshell.gateway]
name = "e2e"
bind_address = "127.0.0.1:$PORT"
compute_driver = "wslc"
$([ "$MODE" = plain ] && echo 'disable_tls = true')
[openshell.gateway.gateway_jwt]
signing_key_path = "$WORK/tls/jwt/signing.pem"
public_key_path = "$WORK/tls/jwt/public.pem"
kid_path = "$WORK/tls/jwt/kid"
gateway_id = "e2e"
# As Iwato's gateway.toml (there under the state volume).
[openshell.gateway.credential_storage]
key_encryption_key_path = "$WORK/db/credentials/key-encryption-key.bin"
$([ "$MODE" = plain ] && printf '[openshell.gateway.auth]\nallow_unauthenticated_users = true')
[openshell.drivers.wslc]
socket_path = "$WORK/wslc.sock"
allow_driver_config = true
[openshell.drivers.wslc.resource_admission]
enabled = false
EOF

# DRIVER_FLAGS adds driver options (for example -shell-init).
drv=("$WORK/drv" -wslc "$WORK/fakewslc" -network bridge -state-dir "$WORK/state" -legacy-term "$WORK/iwato-entrypoint" ${DRIVER_FLAGS:-})
gw=("$CACHE/openshell-gateway" --config "$WORK/gateway.toml")
if [ "$MODE" = tls ]; then
  "$WORK/drv" certs "$WORK/drvtls"
  drv+=(-listen tls://127.0.0.1:17671 -tls-cert "$WORK/drvtls/server.pem" -tls-key "$WORK/drvtls/server.key" -tls-client-ca "$WORK/drvtls/ca.pem"
    -gateway-endpoint https://127.0.0.1:$PORT -guest-tls-ca "$WORK/tls/ca.crt")
  gw+=(--tls-cert "$WORK/tls/server/tls.crt" --tls-key "$WORK/tls/server/tls.key" --tls-client-ca "$WORK/tls/ca.crt" --enable-mtls-auth true)
  # The relay a WSLc gateway container runs, here on loopback.
  socat UNIX-LISTEN:"$WORK/wslc.sock",fork,mode=600 OPENSSL:127.0.0.1:17671,cert="$WORK/drvtls/relay.pem",key="$WORK/drvtls/relay.key",cafile="$WORK/drvtls/ca.pem" 9>&- &
  relay=$!
  mkdir -p "$HOME/.config/openshell/gateways/e2e/mtls"
  cp "$WORK/tls/ca.crt" "$HOME/.config/openshell/gateways/e2e/mtls/ca.crt"
  cp "$WORK/tls/client/tls.crt" "$WORK/tls/client/tls.key" "$HOME/.config/openshell/gateways/e2e/mtls/"
  export OPENSHELL_GATEWAY_ENDPOINT=https://127.0.0.1:$PORT OPENSHELL_GATEWAY=e2e
else
  drv+=(-listen "unix://$WORK/wslc.sock" -gateway-endpoint http://127.0.0.1:$PORT)
  export OPENSHELL_GATEWAY_ENDPOINT=http://127.0.0.1:$PORT
fi
section start
"${drv[@]}" >"$WORK/driver.log" 2>&1 9>&- &
drvpid=$!
if [ "$MODE" = tls ]; then waitfor 30 listening 17671; else waitfor 30 test -S "$WORK/wslc.sock"; fi || { echo "the driver did not start"; cat "$WORK/driver.log"; exit 1; }
OPENSHELL_DB_URL=sqlite:$WORK/db/openshell.db "${gw[@]}" >"$WORK/gateway.log" 2>&1 9>&- &
gwpid=$!
agent_files
# </dev/null: exec reads an open stdin to its end before running (OpenShell #3993).
os() { timeout 120 "$CACHE/openshell" "$@" 2>&1 </dev/null; }
waitfor 60 eval 'os sandbox list >/dev/null' || {
  echo "the gateway did not start; the CLI says:"; timeout 20 "$CACHE/openshell" sandbox list 2>&1 </dev/null | tail -20 | sed 's/^/    /'
  echo "the gateway's log:"; tail -20 "$WORK/gateway.log"; exit 1; }
# Wait until the sandbox's phase reads Ready.
ready() { for _ in $(seq 60); do os sandbox get "$1" | grep -q -i "phase.*ready" && return 0; sleep 1; done; os sandbox get "$1"; return 1; }
# Sandboxes share this host's network here, and each binds the DNS relay
# (127.0.0.53:53): wait until no earlier sandbox still listens there. A
# closed connection in TIME_WAIT does not keep it from binding.
dns_bound() { listening 53 || awk 'NR>1 && $2 ~ /:0035$/{f=1} END{exit !f}' /proc/net/udp /proc/net/udp6; }
# A sandbox an earlier failed check left behind is killed, so that one
# failure does not fail every later check.
free_dns() {
  waitfor 30 eval '! dns_bound' && return
  echo "port 53 is still held, killing what is left: $(sudo ss -lntup 'sport = :53' | tail -n +2)"
  sudo pkill -KILL -x openshell-sandb; sudo pkill -KILL -x openshell-super
  waitfor 10 eval '! dns_bound' || echo "port 53 is still held: $(sudo ss -lntup 'sport = :53' | tail -n +2)"
}

# ONLY=hangloop N=30 repeats create / stop (5 s grace) / delete and prints
# how long each stop took: a sandbox that does not exit after SIGTERM shows
# up as about 5 s.
if [ "${ONLY:-}" = hangloop ]; then
  for i in $(seq "${N:-30}"); do
    free_dns
    os sandbox create --name loop --detach -- sleep infinity >/dev/null
    ready loop >/dev/null || { echo "LOOP $i not ready"; os sandbox delete loop >/dev/null; continue; }
    id=$("$WORK/fakewslc" container list --all --quiet --no-trunc --filter label=openshell.ai/sandbox-name=loop --filter label=openshell.ai/isolation-role=sandbox)
    began=$(date +%s.%N)
    "$WORK/fakewslc" container stop --time 5 "$id"
    took=$(echo "$(date +%s.%N) - $began" | bc)
    echo "LOOP $i stop took $took s, $("$WORK/fakewslc" container inspect --format json "$id" | grep -o '"ExitCode":[0-9]*')"
    os sandbox delete loop >/dev/null
    for _ in $(seq 30); do os sandbox get loop >/dev/null 2>&1 || break; sleep 1; done
  done
  exit 0
fi
# ONLY=hang stops a sandbox's workload container directly, with its
# supervisor still connected, and reports how long that took.
if [ "${ONLY:-}" = hang ]; then
  free_dns
  os sandbox create --name hang --detach -- sleep infinity >/dev/null
  ready hang
  id=$("$WORK/fakewslc" container list --all --quiet --no-trunc --filter label=openshell.ai/sandbox-name=hang --filter label=openshell.ai/isolation-role=sandbox)
  # A debug sandbox (SANDBOX_BIN) dumps its threads' stacks on SIGQUIT.
  # (a debug sandbox, SANDBOX_BIN, can dump stacks on SIGQUIT: pkill -QUIT -x openshell-sandb)
  began=$(date +%s.%N)
  "$WORK/fakewslc" container stop --time 30 "$id"
  echo "HANG stop took $(echo "$(date +%s.%N) - $began" | bc) s, exit $("$WORK/fakewslc" container inspect --format json "$id" | grep -o '"ExitCode":[0-9]*')"
  "$WORK/fakewslc" container logs "$id" 2>&1 | tail -6 | sed 's/^/    /'
  exit 0
fi
# gotest NAME runs one of Iwato's Go e2e tests (built above) as go test
# would: in the package directory, with its 10 min default timeout. -test.v:
# the logs some checks print, and passed can tell that the test really ran.
gotest() { out=$(cd "$ROOT" && OPENSHELL_E2E_ADDR=127.0.0.1:$PORT OPENSHELL_E2E_TLS="$WORK/tls" OPENSHELL_E2E_WSLC="$WORK/fakewslc" \
  OPENSHELL_E2E_KEK="$WORK/db/credentials/key-encryption-key.bin" "$WORK/iwato.test" -test.v -test.timeout 10m -test.run "^$1\$" "${@:2}" 2>&1); gorc=$?; }
# passed DESCRIPTION: the check for the gotest just before (out, gorc).
passed() { check "$1" '[ $gorc = 0 ] && [[ $out == *"--- PASS: $sect "* ]]'; }
# ONLY=ingress runs just Iwato's ingress check (MODE=tls), with all its output.
if [ "${ONLY:-}" = ingress ]; then
  free_dns
  gotest TestOpenShellIngressE2E -test.timeout 8m
  echo "$out"
  exit $gorc
fi
# ONLY=iwato skips the driver checks: just Iwato's Go e2e tests and the
# profile lint (MODE=tls).
if [ "${ONLY:-}" != iwato ]; then
section driver
free_dns
out=$(os sandbox create --name once --no-keep -- sh -c 'echo MARK-OK; id -u')
check "a command runs and its output comes back" '[[ $out == *MARK-OK* ]]'
check "the workload runs as a non-root user" '[[ $out == *$'"'"'\n'"'"'1000* ]]'

free_dns
out=$(os sandbox create --name egress --no-keep -- sh -c 'timeout 5 bash -c "echo > /dev/tcp/1.1.1.1/80" && echo OPEN || echo BLOCKED')
check "egress is denied by default" '[[ $out == *BLOCKED* ]]'

free_dns
FAKE_WSLC_STATE=$WORK/fake "$WORK/fakewslc" volume create e2e-data
out=$(os sandbox create --name keep --detach --cpu 500m --memory 256Mi \
  --driver-config-json '{"wslc":{"mounts":[{"type":"volume","source":"e2e-data","target":"/data","read_only":false},{"type":"tmpfs","target":"/scratch","size_bytes":1048576}]}}' -- sleep 3600)
check "a detached sandbox is created" '[[ $out == *keep* ]] && ready keep'
out=$(os sandbox get keep)
check "a user volume and tmpfs reach wslc" 'grep -q "type=volume,source=e2e-data,target=/data --tmpfs /scratch:size=1048576" "$WORK/fake/calls.log"'
check "limits are converted for wslc" 'grep -q -- "--cpus 0.5 --memory 268435456" "$WORK/fake/calls.log"'
out=$(os sandbox exec --name keep -- echo EXEC-ONE)
check "exec reaches a detached sandbox" '[[ $out == *EXEC-ONE* ]]'
if [ "$MODE" = tls ]; then
  out=$(timeout 90 "$WORK/probe" 127.0.0.1:$PORT "$WORK/tls/ca.crt" "$WORK/tls/client/tls.crt" "$WORK/tls/client/tls.key" keep 2>&1)
  check "stdin streams to a command without a terminal" '[[ $out == *STREAM-OK* ]]'
fi
# Terminal sessions run a server that the first attach starts in its own
# session; it must outlive the exec that started it.
# Seen by a later exec, and then by one more. The sandboxes here share the
# host's processes, and it outlives the sandbox: this run's own (the
# cleanup ends it).
naptime=300.$$
os sandbox exec --name keep -- sh -c "setsid sleep $naptime </dev/null >/dev/null 2>&1 &" >/dev/null
sleeping() { out=$(os sandbox exec --name keep -- sh -c "ps -eo args | grep -c '^sleep $naptime\$'"); [[ $out == *1* ]]; }
waitfor 10 sleeping
check "a process started in its own session outlives its exec" 'sleeping'
os sandbox stop keep >/dev/null
# Its containers stop running.
waitfor 30 eval '! "$WORK/fakewslc" container list --quiet --filter label=openshell.ai/sandbox-name=keep | grep -q .'
free_dns
out=$(os sandbox start keep)
ready keep
out=$(os sandbox exec --name keep -- echo EXEC-AFTER-RESTART)
check "a stopped sandbox starts again with a new session" '[[ $out == *EXEC-AFTER-RESTART* ]]'
os sandbox delete keep >/dev/null
deleted() { ! "$WORK/fakewslc" container list --all --quiet --filter label=openshell.ai/sandbox-name=keep | grep -q . && ! "$WORK/fakewslc" volume list --quiet --filter label=openshell.ai/managed-by=openshell | grep -q .; }
waitfor 30 deleted
check "delete removes containers and volumes" deleted

fi

if [ "$MODE" = tls ]; then
  # Iwato's OpenShell runtime: create, terminal, stop/start, delete.
  section TestOpenShellE2E
  free_dns
  gotest TestOpenShellE2E
  passed "iwato runs a sandbox and its terminal on OpenShell"
  # Terminals that the daemon (the sandbox's main process) keeps outlive
  # Iwato: a new one resumes, lists, shows and kills them; a sandbox
  # without it keeps its main process and execs (and OpenShell's SFTP); one
  # made before the image carried it runs it from the legacy path.
  section TestOpenShellTerminalsSurviveRestartE2E
  free_dns
  gotest TestOpenShellTerminalsSurviveRestartE2E
  TAIL=30 passed "iwato's terminals outlive it in the sandbox's terminal daemon"
  # Iwato's files: WebDAV to the daemon's SFTP (the home), and whether the
  # policy's read_only holds for it. -v: the result.
  section TestOpenShellFilesE2E
  free_dns
  gotest TestOpenShellFilesE2E
  echo "$out" | grep -E -A25 "read_only:|owned by" | sed 's/^ */    /'
  TAIL=30 passed "iwato serves a sandbox's files over WebDAV through SFTP"
  # Iwato's ingress: servers in a sandbox through the core and ForwardTcp
  # (127.0.0.1 and ::1, nothing listening, raw TCP, stopped), and servers
  # the workload runs from another exec, listed and then swept after they
  # exit. -v: whether the gateway relays to ::1, and whether those servers
  # could bind (a detached one needs kernel.yama.ptrace_scope=0, see
  # e2e.yml; without it that case is skipped).
  section TestOpenShellIngressE2E
  free_dns
  gotest TestOpenShellIngressE2E
  echo "$out" | grep -E "::1:|inside the sandbox|#4331|NOT VERIFIED|--- (PASS|SKIP|FAIL): TestOpenShellIngressE2E/" | sed 's/^ */    /'
  TAIL=30 passed "iwato's ingress reaches servers in a sandbox through ForwardTcp"
  section lint
  # Iwato's profile files pass OpenShell's own lint (the gateway's
  # LintProviderProfiles, which the CLI calls).
  for f in "$ROOT"/profiles/*.yaml; do
    out=$(os provider profile lint -f "$f")
    check "openshell provider profile lint accepts profiles/${f##*/}" '[[ $out == *"lint passed"* ]]'
  done
  sed 's/access: read-only/access: readonly/' "$ROOT/profiles/iwato-github.yaml" >"$WORK/bad-profile.yaml"
  out=$(os provider profile lint -f "$WORK/bad-profile.yaml")
  check "openshell provider profile lint refuses a broken copy" '[[ $out == *"lint failed"* && $out == *"unknown access value"* ]]'
  # Iwato's provider profiles and providers (the key-encryption key where
  # gateway.toml puts it).
  section TestOpenShellProvidersE2E
  free_dns
  gotest TestOpenShellProvidersE2E
  TAIL=30 passed "iwato imports its profiles, and providers reach a sandbox as placeholders"
  # Provider scopes: OpenShell lints the rendered profiles, a GitHub
  # provider's own profile follows its scope into a running sandbox's
  # effective policy, and goes with the provider.
  section TestOpenShellScopeE2E
  free_dns
  gotest TestOpenShellScopeE2E
  echo "$out" | grep -E "readiness|lint " | sed 's/^ */    /'
  TAIL=30 passed "a provider's scope renders its own profile, which reaches the sandbox"
  # The Atlassian profile and Iwato's OAuth flow against the gateway: a
  # fake Atlassian, and a copy of the profile whose token_url is a loopback
  # endpoint in the test, so the gateway's refresh (and the rotation of the
  # refresh token) really runs.
  section TestOpenShellAtlassianE2E
  free_dns
  gotest TestOpenShellAtlassianE2E
  TAIL=30 passed "iwato connects Atlassian: the gateway refreshes and rotates, the sandbox gets a placeholder"
  # The Grok Build subscription profile and Iwato's device code flow: a
  # fake auth.x.ai, a copy of the profile refreshing there, and grok's
  # GROK_AUTH_PROVIDER_COMMAND in the sandbox printing the placeholder.
  section TestOpenShellGrokSubscriptionE2E
  free_dns
  gotest TestOpenShellGrokSubscriptionE2E
  TAIL=30 passed "iwato signs in to xAI: the gateway refreshes and rotates, grok's auth command in the sandbox prints the placeholder"
  # A rule approved before a provider that would carry its credential
  # uninspected (L4): Iwato refuses the attach, fixes it, and refuses such
  # rules while the provider is attached. -v: the log shows OpenShell's own
  # answers.
  section TestOpenShellCredentialConflictE2E
  free_dns
  gotest TestOpenShellCredentialConflictE2E
  echo "$out" | grep -E "OpenShell's|then the sandbox" | sed 's/^ */    /'
  TAIL=30 passed "iwato refuses an attach over an uninspected rule for the credential, and fixes it"
  # Endpoints whose metadata disagree (an audited rule where the GitHub
  # provider's rule enforces): Iwato's prediction names the same pairs as
  # the gateway's refusal, on an edit and on an attach.
  section TestOpenShellAmbiguityE2E
  free_dns
  gotest TestOpenShellAmbiguityE2E
  echo "$out" | grep -E "OpenShell's" | sed 's/^ */    /'
  TAIL=30 passed "iwato predicts the gateway's endpoint ambiguity refusal, and fixes it"
  # Iwato's network presets: a sandbox created with all of them and the
  # GitHub provider, and a preset put into its policy and taken out.
  section TestOpenShellPresetsE2E
  free_dns
  gotest TestOpenShellPresetsE2E
  TAIL=30 passed "the gateway takes iwato's network presets with the GitHub provider"
  # The policy tab's YAML editor: a whole policy replaced, its fixed parts kept.
  section TestOpenShellPolicyYAMLE2E
  free_dns
  gotest TestOpenShellPolicyYAMLE2E
  TAIL=30 passed "the gateway takes a policy edited as YAML, as iwato checks it"
  # Recreating a sandbox: the same volumes, its provider and its rule come back.
  section TestOpenShellRecreateE2E
  free_dns
  gotest TestOpenShellRecreateE2E
  TAIL=30 passed "iwato recreates a sandbox with its volumes, providers and policy rules"
fi

echo "mode=$MODE"
exit $fail
