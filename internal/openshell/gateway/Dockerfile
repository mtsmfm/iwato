# The OpenShell gateway (official release binaries). For WSLc the compute
# driver runs on Windows; socat relays the gateway's driver socket to it
# over mutual TLS through host.wslc.internal (Windows' loopback, as WSLc
# relays it). On a Docker-compatible engine (COMPUTE_DRIVER=docker) the
# gateway runs upstream's Docker driver itself (gateway-docker.toml).
#
# OPENSHELL_TAG picks the release: v0.1.6, or dev for OpenShell's rolling
# build of main. For dev the sandbox and supervisor images are built here
# too (--target sandbox / supervisor), as the official images of main are
# not tagged.
#
# OPENSHELL_PATCH picks the supervisor: release (the release's binary), or
# patched (v0.1.6's source with openshell.patch, built here with Rust; only
# for v0.1.6). The gateway image carries the same supervisor at
# /openshell-supervisor, so it serves as the sandboxes' supervisor image too
# (gateway.PatchedSupervisor).
ARG OPENSHELL_TAG=v0.1.6
ARG OPENSHELL_PATCH=patched

FROM ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac AS base
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
      v0.1.6) printf '%s\n' \
        '811265a55993beb333905a5b560764a40aa9177250171f711dce8ba820f7eff8  openshell-gateway-x86_64-unknown-linux-gnu.tar.gz' \
        'de7342457746829138cecbc21cc90a2d5d4c0ea22a362b976831ac3bbb0109d6  openshell-gateway-aarch64-unknown-linux-gnu.tar.gz' \
        '2951c67366f3e3584ed9cec8d49b9f0b0eefc8cacea8b0582ac164590492f136  openshell-x86_64-unknown-linux-musl.tar.gz' \
        '259452a6065c4f76e71884b84e8696cc0fec2f8a832891d333f268ca7bb55a21  openshell-aarch64-unknown-linux-musl.tar.gz' \
        '789c42ea8783f66574bc49d09f32136224a182f5ef1a46b84b71ef2f31de818f  openshell-sandbox-x86_64-unknown-linux-musl.tar.gz' \
        '93092ff5669cd312f915d704243365c688933232c931cae43d492669f4a1673e  openshell-sandbox-aarch64-unknown-linux-musl.tar.gz' \
        'c33be6fc1e3bebba60bd1dc29a56b274052578d724e5eca46fedd4311a0cd323  openshell-supervisor-x86_64-unknown-linux-gnu.tar.gz' \
        '01a9734368ac32c399b664ad943feca1d777d36ae4fb4e073422d09c5e9a4961  openshell-supervisor-aarch64-unknown-linux-gnu.tar.gz' \
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

# v0.1.6's source (the tag's commit) with openshell.patch applied, built as
# the release builds openshell-supervisor (crates' version set to the
# release's, default features, glibc) with the repository's toolchain, 1.95.0.
# rustup-init is pinned by SHA-256 per architecture; rustup checks the
# toolchain itself.
# openshell.patch is the diff of the iwato/v0.1.6 branch of
# github.com/mtsmfm/openshell (ae198bdea236) from v0.1.6, regenerated with
#   git diff v0.1.6 ae198bdea236dab96475d56151a29cf13c8b5543
# (HTTP/2 for opted-in endpoints, a 64 MiB gRPC receive limit in the
# supervisor's client, relays kept open across policy reloads).
FROM base AS supervisor-patched
ARG OPENSHELL_TAG
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential clang cmake pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN set -e; [ "$OPENSHELL_TAG" = v0.1.6 ] || { echo "openshell.patch is for OpenShell v0.1.6, not $OPENSHELL_TAG" >&2; exit 1; }; \
    a=$(uname -m); case "$a" in \
      x86_64) want=dda7234360b7f578ca8b0ddcb80145646fa61a67c1720a5abc7051b35c9fcb71 ;; \
      aarch64) want=15f6e4ce9f583b929c996c91562bad6d4454f3281de858b02cdfdef615fac433 ;; \
      *) echo "unsupported architecture $a" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/rustup-init "https://static.rust-lang.org/rustup/archive/1.29.1/$a-unknown-linux-gnu/rustup-init"; \
    echo "$want  /tmp/rustup-init" | sha256sum --check --strict -; \
    chmod +x /tmp/rustup-init; /tmp/rustup-init -y --no-modify-path --profile minimal --default-toolchain 1.95.0; rm /tmp/rustup-init
ENV PATH=/root/.cargo/bin:$PATH RUSTUP_TOOLCHAIN=1.95.0
WORKDIR /src
# The version is set in Cargo.lock too (only the workspace's crates are at
# 0.0.0), so --locked holds.
RUN git init -q . && git fetch -q --depth 1 https://github.com/NVIDIA/OpenShell.git ac7f615cd12d5cc49cb16eb1f5f10ffeb1e90f47 \
    && git checkout -q FETCH_HEAD \
    && sed -i 's/^version = "0\.0\.0"$/version = "0.1.6"/' Cargo.toml Cargo.lock \
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

FROM ubuntu:24.04@sha256:68ee22004a9109b44973313b02ff7b1de250f1de0f5ecb628d73f579aaff33ac AS supervisor
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
