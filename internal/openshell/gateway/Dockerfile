# The OpenShell gateway (official release binaries). For WSLc the compute
# driver runs on Windows; socat relays the gateway's driver socket to it
# over mutual TLS through host.wslc.internal (Windows' loopback, as WSLc
# relays it). On a Docker-compatible engine (COMPUTE_DRIVER=docker) the
# gateway runs upstream's Docker driver itself (gateway-docker.toml).
#
# OPENSHELL_TAG picks the release: v0.1.7, or dev for OpenShell's rolling
# build of main. For dev the sandbox and supervisor images are built here
# too (--target sandbox / supervisor), as the official images of main are
# not tagged.
#
# OPENSHELL_PATCH picks the supervisor: release (the release's binary), or
# patched (v0.1.7's source with openshell.patch, built here with Rust; only
# for v0.1.7). The gateway image carries the same supervisor at
# /openshell-supervisor, so it serves as the sandboxes' supervisor image too
# (gateway.PatchedSupervisor).
ARG OPENSHELL_TAG=v0.1.7
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
      v0.1.7) printf '%s\n' \
        'e1714dcfd85c949e2f53260bcfbf04b203f6f5cbd3a61e61c11c38cbc91cb102  openshell-gateway-x86_64-unknown-linux-gnu.tar.gz' \
        'eb0ba91efebbcc61e4cfae5fb18934c5fe308ece1a09ba16c999d00c452109dc  openshell-gateway-aarch64-unknown-linux-gnu.tar.gz' \
        '5c1adfcae797eac9b2e8b9280c66c1f75e4e84d9e31ce6938c5b994314ef6098  openshell-x86_64-unknown-linux-musl.tar.gz' \
        '696b523c263895b69eaa7c7e6afbb19b8af6c65cd79c7ffac7b685b6e3aa23da  openshell-aarch64-unknown-linux-musl.tar.gz' \
        'c1b3166d1c232948293ff84b0fac2d0419a24e4b694d354ba6aeb100efa2f039  openshell-sandbox-x86_64-unknown-linux-musl.tar.gz' \
        'ed60eab7612f372a0f669186ac2637fe3223fe90a1c20ad51d747e1a87ca5c6d  openshell-sandbox-aarch64-unknown-linux-musl.tar.gz' \
        'a33903b42a8333de73f4a0f051b0477559c632cf31188caeed0064b4c2048601  openshell-supervisor-x86_64-unknown-linux-gnu.tar.gz' \
        '095d561df9ea40e0af55d8d3645cd13ee6ef9f92d2ccd2b78d76faaec4dec9f3  openshell-supervisor-aarch64-unknown-linux-gnu.tar.gz' \
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

# v0.1.7's source (the tag's commit) with openshell.patch applied, built as
# the release builds openshell-supervisor (crates' version set to the
# release's, default features, glibc) with the repository's toolchain, 1.95.0.
# rustup-init is pinned by SHA-256 per architecture; rustup checks the
# toolchain itself.
# openshell.patch is the iwato/v0.1.6 branch of github.com/mtsmfm/openshell
# (8a75a2694242) rebased onto v0.1.7 (l7/tls.rs: try_server_builder), as
#   git diff v0.1.7 <the rebased commit>
# (HTTP/2 for opted-in endpoints, a 64 MiB gRPC receive limit in the
# supervisor's client, relays kept open across policy reloads).
FROM base AS supervisor-patched
ARG OPENSHELL_TAG
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential clang cmake pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN set -e; [ "$OPENSHELL_TAG" = v0.1.7 ] || { echo "openshell.patch is for OpenShell v0.1.7, not $OPENSHELL_TAG" >&2; exit 1; }; \
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
RUN git init -q . && git fetch -q --depth 1 https://github.com/NVIDIA/OpenShell.git 5d25211b25a8fc887e8d5c606d57d6ab22f1d76c \
    && git checkout -q FETCH_HEAD \
    && sed -i 's/^version = "0\.0\.0"$/version = "0.1.7"/' Cargo.toml Cargo.lock \
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
