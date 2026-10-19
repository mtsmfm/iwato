#!/bin/bash
# Does openshell-sandbox (OpenShell v0.1.2, official binaries) hang after
# SIGTERM with the official Docker driver too? Run in a WSL distro (or any
# Linux) with Docker, Docker Desktop included:
#   bash sigterm-repro-docker.sh [RUNS]
# The gateway (Docker driver, plaintext) runs as a host-network container on
# the same engine, since the driver's supervisors use the engine host's
# 127.0.0.1. Each run stops two new sandboxes: one with `docker stop -t 10`
# on its workload container (supervisor still connected), one with
# `openshell sandbox stop`.
# A sandbox that does not exit after SIGTERM shows up as about 10 s and
# exit 137. Logs and the summary stay in ~/openshell-repro.
set -u
VERSION=0.1.2
RUNS=${1:-5}
DIR=$HOME/openshell-repro
GW=openshell-repro-gateway
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
summary=()
# Creates a sandbox, waits until it is ready, prints its workload container.
fresh() {
  if ! out=$(os sandbox create --name "$1" --detach -- sleep infinity); then
    echo "create failed:" >&2; echo "$out" >&2; docker logs "$GW" 2>&1 | tail -30 >&2; exit 1
  fi
  ready "$1" >/dev/null || { echo "sandbox $1 did not become ready" >&2; docker logs "$GW" 2>&1 | tail -30 >&2; exit 1; }
  workload "$1"
}
for i in $(seq "$RUNS"); do
  echo "== run $i (the first one pulls the sandbox images, which takes a while)"
  # 1. docker stop on the workload, supervisor still connected.
  id=$(fresh "dock$i") || exit 1
  began=$(date +%s.%N)
  docker stop -t 10 "$id" >/dev/null
  t1=$(elapsed "$began")
  code1=$(docker inspect -f '{{.State.ExitCode}}' "$id")
  docker logs "$id" > "sandbox-$i-docker-stop.log" 2>&1
  os sandbox delete "dock$i" >/dev/null
  # 2. OpenShell's own stop on another new sandbox.
  id=$(fresh "os$i") || exit 1
  began=$(date +%s.%N)
  os sandbox stop "os$i" >/dev/null
  while [ "$(docker inspect -f '{{.State.Running}}' "$id" 2>/dev/null)" = true ]; do sleep 0.05; done
  t2=$(elapsed "$began")
  code2=$(docker inspect -f '{{.State.ExitCode}}' "$id" 2>/dev/null)
  docker logs "$id" > "sandbox-$i-openshell-stop.log" 2>&1
  os sandbox delete "os$i" >/dev/null
  line="run $i: docker stop ${t1}s (exit $code1), openshell sandbox stop ${t2}s (exit $code2)"
  echo "   $line"
  summary+=("$line")
done
echo
echo "== summary (about 10 s or exit 137 = the sandbox did not exit after SIGTERM)"
printf '%s\n' "${summary[@]}" | tee summary.txt
echo "logs: $DIR"
