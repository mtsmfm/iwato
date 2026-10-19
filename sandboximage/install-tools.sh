#!/bin/sh
# Installs mise and starship, verified by SHA-256. Change versions and
# checksums together. mise lives in a directory dev owns, so `mise
# self-update` works without sudo (until the sandbox is created again from the image).
set -eu
MISE_VERSION=2026.9.14
STARSHIP_VERSION=1.26.0
case "$(uname -m)" in
  x86_64) mt=x64; ms=ac7c8d4ac0317a1c9d82091551640a447fc25998562a32745d48c3b6e4322d9b
          st=x86_64; ss=b7c232b0e8249d8e55a40beb79c5c43a7d370f3f9408bd215deb0170daeaadf3 ;;
  aarch64) mt=arm64; ms=f0df26181a3c1289f4cd6ba2fcfdaf5bd784c4032bf8f4efe3d0986b1eb88b9e
          st=aarch64; ss=dc30189378d2f2e287384e8a692d3f95ad1df64cf0e8c36aa9201516028aed6b ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac
install -d -o dev -g dev /usr/local/lib/mise
curl -fsSL -o /usr/local/lib/mise/mise "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${mt}-musl"
echo "${ms}  /usr/local/lib/mise/mise" | sha256sum -c -
chown dev:dev /usr/local/lib/mise/mise
chmod 755 /usr/local/lib/mise/mise
ln -s /usr/local/lib/mise/mise /usr/local/bin/mise
curl -fsSL -o /tmp/starship.tgz "https://github.com/starship/starship/releases/download/v${STARSHIP_VERSION}/starship-${st}-unknown-linux-musl.tar.gz"
echo "${ss}  /tmp/starship.tgz" | sha256sum -c -
tar -xzf /tmp/starship.tgz -C /usr/local/bin starship
chmod 755 /usr/local/bin/starship
rm -f /tmp/starship.tgz
