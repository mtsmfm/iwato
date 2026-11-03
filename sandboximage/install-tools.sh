#!/bin/sh
# Installs mise and starship, verified by SHA-256. Change versions and
# checksums together. mise lives in a directory dev owns, so `mise
# self-update` works without sudo (until the sandbox is created again).
set -eu
MISE_VERSION=2026.10.20
STARSHIP_VERSION=1.27.0
case "$(uname -m)" in
  x86_64) mt=x64; ms=bfeb49cc86189d259e4eadbde30841e6bb7e9b4d669588fc2cc746e31d1cf632
          st=x86_64; ss=10c8eb4cb3b761d9bf59ea828444c2dab36c900b7d69a69e08814b286969df89 ;;
  aarch64) mt=arm64; ms=bc9c9eeed54f4a26fc88b4acc9a61804cbab4cd95c7d4d29cd47b6e3f1ea52c8
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
