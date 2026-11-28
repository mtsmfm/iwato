# The OpenShell gateway (official release binaries). For WSLc the compute
# driver runs on Windows; socat relays the gateway's driver socket to it
# over mutual TLS through host.wslc.internal (Windows' loopback, as WSLc
# relays it). On a Docker-compatible engine (COMPUTE_DRIVER=docker) the
# gateway runs upstream's Docker driver itself (gateway-docker.toml).
#
# OPENSHELL_TAG picks the release: v0.1.10, or dev for OpenShell's rolling
# build of main. For dev the sandbox and supervisor images are built here
# too (--target sandbox / supervisor), as the official images of main are
# not tagged.
#
# OPENSHELL_PATCH picks the supervisor: release (the release's binary), or
# patched (v0.1.10's source with openshell.patch, built here with Rust; only
# for v0.1.10). The gateway image carries the same supervisor at
# /openshell-supervisor, so it serves as the sandboxes' supervisor image too
# (gateway.PatchedSupervisor).
ARG OPENSHELL_TAG=v0.1.10
ARG OPENSHELL_PATCH=patched

FROM ubuntu:24.04@sha256:562111b88caa79c71391c0c4d3c2114a7d5c35915c20561fd893cb1e07929e6b AS base
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
RUN set -e; base=https://github.com/ai-openshell/openshell/releases/download/${OPENSHELL_TAG}; mkdir /out /dl; cd /dl; a=$(uname -m); \
    files="openshell-gateway-$a-unknown-linux-gnu openshell-$a-unknown-linux-musl openshell-sandbox-$a-unknown-linux-musl openshell-supervisor-$a-unknown-linux-gnu"; \
    case "$OPENSHELL_TAG" in \
      v0.1.10) printf '%s\n' \
        '2d609a3fa586aacdd29834f66379bb6ec565549a6dc572285115d2c129d078ac  openshell-gateway-x86_64-unknown-linux-gnu.tar.gz' \
        'd8ec5fd2646e482856d64fdac36940c7345314ada1ffd752b67aed3e440b2c5d  openshell-gateway-aarch64-unknown-linux-gnu.tar.gz' \
        '2467a09d839bb9af43bf1d649d72e77e596578cbd8e567b6e55a0c504c222f5f  openshell-x86_64-unknown-linux-musl.tar.gz' \
        'a6ad4833ca22a077d844e8405eae4092b32c9ae1e53b1b2151d77374117ff4c0  openshell-aarch64-unknown-linux-musl.tar.gz' \
        '4ee38fbf38a9d3f383f958f868f1eefabb31f1e095d4e08f44a1394ab0a0b95c  openshell-sandbox-x86_64-unknown-linux-musl.tar.gz' \
        'dcdded7d582ac004e552196958c39dbb8360b802ae3b062e6325c1b90ac83ebe  openshell-sandbox-aarch64-unknown-linux-musl.tar.gz' \
        'd7aa20027d37ec828969fc9179082aa99578ba270d617116ac348c008c7a6ef6  openshell-supervisor-x86_64-unknown-linux-gnu.tar.gz' \
        'ff524d9f2c2ee770523a5c8d021c5de4a14fdc39d265ec550fc72d7ff78ab584  openshell-supervisor-aarch64-unknown-linux-gnu.tar.gz' \
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

# v0.1.10's source (the tag's commit) with openshell.patch applied, built as
# the release builds openshell-supervisor (crates' version set to the
# release's, default features, glibc) with the repository's toolchain, 1.95.0.
# rustup-init is pinned by SHA-256 per architecture; rustup checks the
# toolchain itself.
# openshell.patch is the diff of the iwato/v0.1.10 branch of
# github.com/mtsmfm/openshell (26134607e7ee) from v0.1.10, regenerated with
#   git diff v0.1.10 26134607e7ee93bc1a75dc6a070e793e42fcda88
# (HTTP/2 for opted-in endpoints, a 64 MiB gRPC receive limit in the
# supervisor's client, relays kept open across policy reloads).
FROM base AS supervisor-patched
ARG OPENSHELL_TAG
RUN apt-get update && apt-get install -y --no-install-recommends git build-essential clang cmake pkg-config \
    && rm -rf /var/lib/apt/lists/*
RUN set -e; [ "$OPENSHELL_TAG" = v0.1.10 ] || { echo "openshell.patch is for OpenShell v0.1.10, not $OPENSHELL_TAG" >&2; exit 1; }; \
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
RUN git init -q . && git fetch -q --depth 1 https://github.com/ai-openshell/openshell.git 152c59d3178039e3fbb4a5595f9fff7216a6be92 \
    && git checkout -q FETCH_HEAD \
    && sed -i 's/^version = "0\.0\.0"$/version = "0.1.10"/' Cargo.toml Cargo.lock \
    && cargo fetch --locked
# The unpatched build, in a layer the patch does not invalidate: a change to
# the patch rebuilds only the crates it touches and those depending on them.
RUN cargo build --release --locked --offline -p openshell-supervisor --bin openshell-supervisor
COPY openshell.patch /tmp/
# OPENSHELL_HTTP2_ENDPOINTS: the destinations offered HTTP/2, iwato-modal's
# endpoints (TestHTTP2EndpointsAreModals); hosts may be patterns.
RUN git apply /tmp/openshell.patch \
    && OPENSHELL_HTTP2_ENDPOINTS=api.modal.com:443,api.modal2.com:443,input-plane.*.modal.com:443 \
       cargo build --release --locked --offline -p openshell-supervisor --bin openshell-supervisor \
    && mkdir /out && cp target/release/openshell-supervisor /out/ \
    && rm -rf target && /out/openshell-supervisor --version

FROM supervisor-${OPENSHELL_PATCH} AS supervisor-bin

# As deploy/docker/Dockerfile.sandbox and Dockerfile.supervisor upstream.
FROM scratch AS sandbox
COPY --from=fetch --chmod=0555 /out/openshell-sandbox /openshell-sandbox
USER 65532:65532
ENTRYPOINT ["/openshell-sandbox"]

FROM ubuntu:24.04@sha256:562111b88caa79c71391c0c4d3c2114a7d5c35915c20561fd893cb1e07929e6b AS supervisor
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
