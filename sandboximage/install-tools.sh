#!/bin/sh
# Installs mise and starship, verified by SHA-256. Change versions and
# checksums together. mise lives in a directory dev owns, so `mise
# self-update` works without sudo (until the sandbox is created again).
set -eu
MISE_VERSION=2026.10.17
STARSHIP_VERSION=1.27.0
case "$(uname -m)" in
  x86_64) mt=x64; ms=f6e5e457103252eca6032dbc5c29bff7e0c73f1bc0f915541e7658ba360d4287
          st=x86_64; ss=10c8eb4cb3b761d9bf59ea828444c2dab36c900b7d69a69e08814b286969df89 ;;
  aarch64) mt=arm64; ms=86d4db8bb1277ce164d75c0ba12179830ab172c318598f878f8286d27978e4f2
          st=aarch64; ss=341f5cbf24699fa0b27ec88965ad25f862ea09e15d35a543fd6ffdff1d2aa00d ;;
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
