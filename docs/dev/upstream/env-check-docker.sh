#!/bin/bash
# What HOME (and the rest of an image's ENV) does an OpenShell sandbox get
# with the official Docker driver? Builds a small image with ENV HOME and
# ENV FOO, then prints the environment of the main process and of an exec.
# Run in a WSL distro (or any Linux) with Docker:
#   bash env-check-docker.sh            # v0.1.2
#   VERSION=0.1.3-pre.5 bash env-check-docker.sh
# The summary stays in ~/openshell-envcheck/summary.txt.
set -u
VERSION=${VERSION:-0.1.2}
DIR=$HOME/openshell-envcheck
GW=openshell-envcheck-gateway
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
docker build -q -t openshell-envcheck:1 - >/dev/null <<'DOCKERFILE' || exit 1
FROM ubuntu:24.04
RUN mkdir -p /custom-home && chown 1000:1000 /custom-home
ENV HOME=/custom-home FOO=from-image
DOCKERFILE
show='env | grep -E "^(HOME|USER|SHELL|FOO|PWD)=" | sort'
run() {
  name=$1; shift
  os sandbox create --name "$name" --from openshell-envcheck:1 --detach "$@" -- sh -c "$show > /tmp/main-env; sleep infinity" >/dev/null || { echo "create $name failed"; docker logs "$GW" 2>&1 | tail -30; exit 1; }
  ready "$name" >/dev/null || { echo "sandbox $name did not become ready"; docker logs "$GW" 2>&1 | tail -30; exit 1; }
  {
    echo "== $name ${*:-(no --env)}"
    echo "-- main process"; os sandbox exec --name "$name" -- cat /tmp/main-env
    echo "-- exec"; os sandbox exec --name "$name" --no-login-shell -- sh -c "$show"
    echo "-- exec with --workdir /tmp"; os sandbox exec --name "$name" --no-login-shell --workdir /tmp -- sh -c "$show"
  } | tee -a summary.txt
  os sandbox delete "$name" >/dev/null
}
: > summary.txt
echo "OpenShell $VERSION, $(docker version --format 'docker {{.Server.Version}}')" | tee -a summary.txt
run image-env
run sandbox-env --env HOME=/from-create
echo
echo "summary: $DIR/summary.txt"
