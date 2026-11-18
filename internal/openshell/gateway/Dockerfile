# The OpenShell gateway (official release binaries). For WSLc the compute
# driver runs on Windows; socat relays the gateway's driver socket to it
# over mutual TLS through host.wslc.internal (Windows' loopback, as WSLc
# relays it). On a Docker-compatible engine (COMPUTE_DRIVER=docker) the
# gateway runs upstream's Docker driver itself (gateway-docker.toml).
#
# OPENSHELL_TAG picks the release: v0.1.9, or dev for OpenShell's rolling
# build of main. For dev the sandbox and supervisor images are built here
# too (--target sandbox / supervisor), as the official images of main are
# not tagged.
#
# OPENSHELL_PATCH picks the supervisor: release (the release's binary), or
# patched (v0.1.9's source with openshell.patch, built here with Rust; only
# for v0.1.9). The gateway image carries the same supervisor at
# /openshell-supervisor, so it serves as the sandboxes' supervisor image too
# (gateway.PatchedSupervisor).
ARG OPENSHELL_TAG=v0.1.9
ARG OPENSHELL_PATCH=patched

FROM ubuntu:24.04@sha256:73b999393d07f2bf65a40373778a44dc9a6d54ad7718d29247d8ff9413c4d6a8 AS base
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl socat openssh-client \
    && rm -rf /var/lib/apt/lists/*

FROM base AS fetch
ARG OPENSHELL_TAG
# The release for the machine building (amd64 or arm64; also under emulation).
# Each tarball's SHA-256 is pinned here per release and architecture (from the
# release's *-checksums-sha256.txt, docs/dev/development.md#bumping-pinned-versions)
# and checked before it is extracted: the gateway built from them holds every
# provider's credential. A release with no pins fails the build. dev is a
# rolling build with nothing to pin: it is checked only against the checksum
# files published with it (a broken download, not a replaced release).
RUN set -e; base=https://github.com/NVIDIA/OpenShell/releases/download/${OPENSHELL_TAG}; mkdir /out /dl; cd /dl; a=$(uname -m); \
    files="openshell-gateway-$a-unknown-linux-gnu openshell-$a-unknown-linux-musl openshell-sandbox-$a-unknown-linux-musl openshell-supervisor-$a-unknown-linux-gnu"; \
    case "$OPENSHELL_TAG" in \
      v0.1.9) printf '%s\n' \
        'c32085d64ccef037121c0e87bee1a57d6e886d77f77bb8b260cb08f0a6cce6a3  openshell-gateway-x86_64-unknown-linux-gnu.tar.gz' \
        'a41701c2dc7ac1aba13b0d48678dcb95090055aa42a2103b2ae712fb79338b1a  openshell-gateway-aarch64-unknown-linux-gnu.tar.gz' \
        '3856ac7b6b2e33d6c8ab72a1036444156516e7303da85a9b13f661a2aa63bd2d  openshell-x86_64-unknown-linux-musl.tar.gz' \
        'cb923d08703b966f7e0e6b1483ed4ba6ed77bc10812c5502bcc1fb6ee0f6ad59  openshell-aarch64-unknown-linux-musl.tar.gz' \
        '01d4a311929a9a2a4a0c149340d0ac570ee49aeee9fcf56699735db8d9c52128  openshell-sandbox-x86_64-unknown-linux-musl.tar.gz' \
        '903c5bf487295ddcd6a48af389496fc274d4abfe4f54e44d82b121c8f766737e  openshell-sandbox-aarch64-unknown-linux-musl.tar.gz' \
        '5b47695814cc3897e278f29f8d570c5238484ef27e13d7eecb05efe17e7c1baf  openshell-supervisor-x86_64-unknown-linux-gnu.tar.gz' \
        'f7eec69dbd6f8f4e72a9b4b7c2507606160451071875aaf07cea2fb77954eeed  openshell-supervisor-aarch64-unknown-linux-gnu.tar.gz' \
        > pinned.sha256 ;; \
      dev) echo "WARNING: OpenShell dev is not pinned; checking it against the checksums published with it"; \
        for p in openshell openshell-gateway openshell-sandbox openshell-supervisor; do curl -fsSL "$base/$p-checksums-sha256.txt"; done > pinned.sha256 ;; \
      *) echo "no SHA-256 pinned for OpenShell $OPENSHELL_TAG: add them to internal/openshell/gateway/Dockerfile" >&2; exit 1 ;; \
    esac; \
    for f in $files; do \
      want=$(awk -v f="$f.tar.gz" '$2 == f || $2 == "*" f { print $1; exit }' pinned.sha256); \
      [ -n "$want" ] || { echo "no SHA-256 for $f.tar.gz ($OPENSHELL_TAG)" >&2; exit 1; }; \
      curl -fsSL -o "$f.tar.gz" "$base/$f.tar.gz"; \
      echo "$want  $f.tar.gz" | sha256sum --check --strict - || { echo "$f.tar.gz does not match the pinned SHA-256 $want" >&2; exit 1; }; \
      tar xzf "$f.tar.gz" -C /out; done; \
    cd /; rm -rf /dl; /out/openshell-gateway --version; /out/openshell --version

FROM fetch AS supervisor-release

# v0.1.9's source (the tag's commit) with openshell.patch applied, built as
# the release builds openshell-supervisor (crates' version set to the
# release's, default features, glibc) with the repository's toolchain, 1.95.0.
# rustup-init is pinned by SHA-256 per architecture; rustup checks the
# toolchain itself.
# openshell.patch is the diff of the iwato/v0.1.9 branch of
# github.com/mtsmfm/openshell (f95a239cb51f) from v0.1.9, regenerated with
#   git diff v0.1.9 f95a239cb51fe76858195291a7f4b5dbdf460170
# (HTTP/2 for opted-in endpoints, a 64 MiB gRPC receive limit in the
# supervisor's client, relays kept open across policy reloads).
FROM base AS supervisor-patched
ARG OPENSHELL_TAG
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential clang cmake pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN set -e; [ "$OPENSHELL_TAG" = v0.1.9 ] || { echo "openshell.patch is for OpenShell v0.1.9, not $OPENSHELL_TAG" >&2; exit 1; }; \
    a=$(uname -m); case "$a" in \
      x86_64) want=5be191e8c70a5007074254cb74d78dbfdd25cd5a61c27de7b8e5410122ec24f4 ;; \
      aarch64) want=a9c168dbea81a84671c6b7a5c1795cb0eb699020110d107ddfca427bd797e697 ;; \
      *) echo "unsupported architecture $a" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/rustup-init "https://static.rust-lang.org/rustup/archive/1.29.2/$a-unknown-linux-gnu/rustup-init"; \
    echo "$want  /tmp/rustup-init" | sha256sum --check --strict -; \
    chmod +x /tmp/rustup-init; /tmp/rustup-init -y --no-modify-path --profile minimal --default-toolchain 1.95.0; rm /tmp/rustup-init
ENV PATH=/root/.cargo/bin:$PATH RUSTUP_TOOLCHAIN=1.95.0
WORKDIR /src
# The version is set in Cargo.lock too (only the workspace's crates are at
# 0.0.0), so --locked holds.
RUN git init -q . && git fetch -q --depth 1 https://github.com/NVIDIA/OpenShell.git ce58cf8e9b953281352d98a637e41616295b1318 \
    && git checkout -q FETCH_HEAD \
    && sed -i 's/^version = "0\.0\.0"$/version = "0.1.9"/' Cargo.toml Cargo.lock \
    && cargo fetch --locked
# The unpatched build, in a layer the patch does not invalidate: a change to
# the patch rebuilds only the crates it touches and those depending on them.
RUN cargo build --release --locked --offline -p openshell-supervisor --bin openshell-supervisor
COPY openshell.patch /tmp/
# OPENSHELL_HTTP2_ENDPOINTS: the destinations offered HTTP/2, iwato-modal's
# endpoints (TestHTTP2EndpointsAreModals).
RUN git apply /tmp/openshell.patch \
    && OPENSHELL_HTTP2_ENDPOINTS=api.modal.com:443,api.modal2.com:443 \
       cargo build --release --locked --offline -p openshell-supervisor --bin openshell-supervisor \
    && mkdir /out && cp target/release/openshell-supervisor /out/ \
    && rm -rf target && /out/openshell-supervisor --version

FROM supervisor-${OPENSHELL_PATCH} AS supervisor-bin

# As deploy/docker/Dockerfile.sandbox and Dockerfile.supervisor upstream.
FROM scratch AS sandbox
COPY --from=fetch --chmod=0555 /out/openshell-sandbox /openshell-sandbox
USER 65532:65532
ENTRYPOINT ["/openshell-sandbox"]

FROM ubuntu:24.04@sha256:73b999393d07f2bf65a40373778a44dc9a6d54ad7718d29247d8ff9413c4d6a8 AS supervisor
COPY --from=supervisor-bin --chmod=0555 /out/openshell-supervisor /openshell-supervisor
ENTRYPOINT ["/openshell-supervisor"]

FROM base AS gateway
COPY --from=fetch /out/openshell-gateway /out/openshell /usr/local/bin/
COPY --from=supervisor-bin --chmod=0555 /out/openshell-supervisor /openshell-supervisor
COPY gateway.toml gateway-docker.toml entrypoint.sh /etc/openshell/
RUN chmod 0755 /etc/openshell/entrypoint.sh
# The CLI inside this container (wslc container exec ... openshell ...)
# reaches the gateway with the client certificate the entrypoint installs.
ENV OPENSHELL_GATEWAY=iwato OPENSHELL_GATEWAY_ENDPOINT=https://127.0.0.1:17670 OPENSHELL_TELEMETRY_ENABLED=false
ENTRYPOINT ["/etc/openshell/entrypoint.sh"]
