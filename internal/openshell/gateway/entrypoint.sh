#!/bin/sh
# `init` creates the gateway PKI and JWT keys in the state volume and exits;
# otherwise the gateway serves mTLS, with the relay to the WSLc driver
# alongside, or (COMPUTE_DRIVER=docker) with upstream's Docker driver.
set -eu
tls=/var/lib/openshell/tls
mkdir -p /var/lib/openshell /run/openshell
# The providers' key-encryption key (gateway.toml), owner only: the
# gateway runs as root.
install -d -m 700 /var/lib/openshell/credentials
# The supervisors reach the gateway by its container name on the WSLc
# network (GATEWAY_HOST); PKI without that name in the server certificate
# (no marker for it) is replaced.
host=${GATEWAY_HOST:-openshell-gateway}
if [ ! -f "$tls/.server-san-$host" ]; then
  rm -rf $tls
  openshell-gateway generate-certs --output-dir $tls --server-san "$host" >/dev/null
  touch "$tls/.server-san-$host"
fi
[ "${1:-}" = init ] && exit 0
mtls=$HOME/.config/openshell/gateways/iwato/mtls
mkdir -p $mtls && cp $tls/ca.crt $mtls/ca.crt && cp $tls/client/tls.crt $tls/client/tls.key $mtls/
config=/etc/openshell/gateway.toml
if [ "${COMPUTE_DRIVER:-wslc}" = docker ]; then
  # Upstream's Docker driver, in the gateway, on the engine's socket.
  config=/run/openshell/gateway.toml
  # The driver keeps each sandbox's state (its runtime descriptor, JWT)
  # under XDG_STATE_HOME: in the state volume, so that it outlives a new
  # gateway container.
  export XDG_STATE_HOME=/var/lib/openshell/state
  case "${SANDBOX_LABEL:-}" in ''|*[!a-z0-9-]*) echo "invalid SANDBOX_LABEL" >&2; exit 1 ;; esac
  sed "s/SANDBOX_LABEL/$SANDBOX_LABEL/" /etc/openshell/gateway-docker.toml >$config
  # The supervisor image, when Iwato names one: this image, whose
  # supervisor is built with a patch (gateway.PatchedSupervisor).
  if [ -n "${SUPERVISOR_IMAGE:-}" ]; then
    case "$SUPERVISOR_IMAGE" in *[!A-Za-z0-9._/:@-]*) echo "invalid SUPERVISOR_IMAGE" >&2; exit 1 ;; esac
    sed -i "s|^supervisor_image .*|supervisor_image      = \"$SUPERVISOR_IMAGE\"|" $config
  fi
else
  d=/etc/openshell/driver-tls
  rm -f /run/openshell/wslc.sock
  socat UNIX-LISTEN:/run/openshell/wslc.sock,fork,mode=600 \
    "OPENSSL:host.wslc.internal:${DRIVER_PORT:-17671},cert=$d/relay.pem,key=$d/relay.key,cafile=$d/ca.pem" &
fi
export OPENSHELL_DB_URL=sqlite:/var/lib/openshell/openshell.db
exec openshell-gateway --config $config \
  --tls-cert $tls/server/tls.crt --tls-key $tls/server/tls.key --tls-client-ca $tls/ca.crt --enable-mtls-auth true
