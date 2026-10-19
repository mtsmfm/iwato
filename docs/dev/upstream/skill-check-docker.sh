#!/bin/bash
# Does the Policy Advisor guide reach the sandbox with OpenShell's own
# Docker driver? (NVIDIA/OpenShell#3698: since NVIDIA/OpenShell#2942 the supervisor writes
# it into its own container.) Run in a WSL distro (or any Linux) with
# Docker, Docker Desktop included:
#   bash skill-check-docker.sh            # v0.1.2
#   VERSION=0.1.3-pre.5 bash skill-check-docker.sh
# Checks two sandboxes: one created while agent_policy_proposals_enabled is
# already true, one where it turns true while the sandbox runs. For each it
# lists the guide files inside the workload, asks policy.local for the
# current policy (the API itself should work), and keeps the supervisor's
# log lines about the skill. The summary and logs stay in
# ~/openshell-skillcheck.
set -u
VERSION=${VERSION:-0.1.2}
DIR=$HOME/openshell-skillcheck
GW=openshell-skillcheck-gateway
mkdir -p "$DIR" && cd "$DIR" || exit 1
for f in openshell-gateway-x86_64-unknown-linux-gnu openshell-x86_64-unknown-linux-musl; do
  [ -f "$f.tar.gz" ] || curl -fsSL -o "$f.tar.gz" "https://github.com/NVIDIA/OpenShell/releases/download/v$VERSION/$f.tar.gz" || exit 1
  tar xzf "$f.tar.gz"
done
docker version --format 'docker {{.Server.Version}}' || { echo "docker is not reachable"; exit 1; }

rm -rf tls && ./openshell-gateway generate-certs --output-dir tls >/dev/null || exit 1
cat > gateway.toml <<'EOF'
[openshell]
version = 2

[openshell.gateway]
name           = "repro"
bind_address   = "127.0.0.1:17670"
compute_driver = "docker"
disable_tls    = true

[openshell.gateway.gateway_jwt]
signing_key_path = "/repro/tls/jwt/signing.pem"
public_key_path  = "/repro/tls/jwt/public.pem"
kid_path         = "/repro/tls/jwt/kid"
gateway_id       = "repro"

[openshell.gateway.auth]
allow_unauthenticated_users = true

[openshell.drivers.docker]
socket_path       = "/var/run/docker.sock"
sandbox_label     = "repro"
grpc_endpoint     = "http://127.0.0.1:17670"
image_pull_policy = "if_not_present"
EOF

echo "== gateway container ($GW, host network)"
docker rm -f "$GW" >/dev/null 2>&1
docker create --name "$GW" --network host -v /var/run/docker.sock:/var/run/docker.sock \
  -e OPENSHELL_DB_URL=sqlite:/repro/openshell.db -e OPENSHELL_TELEMETRY_ENABLED=false \
  ubuntu:24.04 /repro/openshell-gateway --config /repro/gateway.toml >/dev/null || exit 1
docker cp "$DIR/." "$GW:/repro" >/dev/null || exit 1
docker start "$GW" >/dev/null || exit 1
trap 'docker logs "$GW" > "$DIR/gateway.log" 2>&1; docker rm -f "$GW" >/dev/null' EXIT
sleep 5
[ "$(docker inspect -f '{{.State.Running}}' "$GW")" = true ] || { echo "gateway did not start:"; docker logs "$GW" 2>&1 | tail -20; exit 1; }

os() { timeout 300 docker exec -e OPENSHELL_GATEWAY_ENDPOINT=http://127.0.0.1:17670 -e OPENSHELL_TELEMETRY_ENABLED=false "$GW" /repro/openshell "$@" </dev/null 2>&1; }
ready() { for _ in $(seq 180); do os sandbox get "$1" | grep -q -i "phase.*ready" && return 0; sleep 1; done; os sandbox get "$1"; return 1; }
workload() { docker ps -a --quiet --no-trunc --filter "label=openshell.ai/sandbox-name=$1" --filter label=openshell.ai/isolation-role=sandbox | head -1; }
elapsed() { awk "BEGIN{printf \"%.2f\", $(date +%s.%N) - $1}"; }
setting() { os settings set --global --key agent_policy_proposals_enabled --value "$1" --yes; }
supervisor() { docker ps -a --quiet --no-trunc --filter "label=openshell.ai/sandbox-name=$1" --filter label=openshell.ai/isolation-role=supervisor | head -1; }
probe='for f in /etc/openshell/skills/policy_advisor.md /etc/openshell/skills/policy-advisor/SKILL.md /AGENTS.md; do if [ -f "$f" ]; then echo "present $f"; else echo "MISSING $f"; fi; done
exec 3<>/dev/tcp/policy.local/80 && printf "GET /v1/policy/current HTTP/1.1\r\nHost: policy.local\r\nConnection: close\r\n\r\n" >&3 && head -1 <&3 | sed "s/^/policy.local: /" || echo "policy.local: unreachable"'
check() {
  {
    echo "== $1 ($2)"
    os sandbox exec --name "$1" -- bash -c "$probe"
    echo "-- supervisor log lines about the skill"
    docker logs "$(supervisor "$1")" 2>&1 | grep -i -E "skill|agent-driven policy proposals" | sed 's/\x1b\[[0-9;]*m//g' | tail -5
  } | tee -a summary.txt
}
create() {
  os sandbox create --name "$1" --detach -- sleep infinity >/dev/null || { echo "create $1 failed"; docker logs "$GW" 2>&1 | tail -30; exit 1; }
  ready "$1" >/dev/null || { echo "sandbox $1 did not become ready"; docker logs "$GW" 2>&1 | tail -30; exit 1; }
}
: > summary.txt
echo "OpenShell $VERSION, $(docker version --format 'docker {{.Server.Version}}')" | tee -a summary.txt

echo "== 1. created while the setting is true (the first sandbox pulls images, which takes a while)"
setting true >/dev/null || exit 1
create on-at-create
check on-at-create "created with agent_policy_proposals_enabled=true"

echo "== 2. setting turned on while the sandbox runs"
setting false >/dev/null || exit 1
create on-later
setting true >/dev/null || exit 1
echo "   waiting 20 s for the supervisor's settings poll"
sleep 20
check on-later "agent_policy_proposals_enabled false -> true while running"

os sandbox delete on-at-create >/dev/null
os sandbox delete on-later >/dev/null
echo
echo "summary: $DIR/summary.txt (MISSING = the guide did not reach the workload)"
