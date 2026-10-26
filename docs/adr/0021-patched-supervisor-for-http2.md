# 0021. A patched supervisor for HTTP/2 on opted-in endpoints

- Status: Accepted
- Date: 2026-10-26

## Context

OpenShell v0.1.5's L7 proxy terminates TLS with ALPN `http/1.1` only, so clients that speak only HTTP/2 (gRPC: Modal's `modal` CLI and Python client) cannot use an inspected endpoint, and without inspection a provider's credential is not substituted. Upstream tracks HTTP/2 in NVIDIA/OpenShell#2426, not yet released.

Alternatives: wait for upstream (no Modal until then); `tls: skip` (no credential substitution, and an uninspected credentialed path); offering h2 wherever the prototype could enforce it (every credentialed REST endpoint, GitHub and Anthropic included, would change protocol at once); an `http2` field in the profile and policy schema (the gateway is the unpatched release, and protobuf drops fields it does not know, so the field would never reach the supervisor without patching and building the gateway too).

## Decision

- The gateway image builds `openshell-supervisor` from v0.1.5's source with `internal/openshell/gateway/h2-l7.patch` and serves as the supervisor image for both drivers. The gateway, CLI and `openshell-sandbox` stay the release's
- The patch offers `h2` only to the `host:port` destinations named in `OPENSHELL_HTTP2_ENDPOINTS` when the supervisor is built, and only where every endpoint of the route is inspected REST without SigV4 or body rewriting, the tunnel carries credentials and no middleware applies. Each stream is admitted like an HTTP/1 request
- Only `iwato-modal`'s endpoints are listed (a test keeps the list equal to the profile). Everything else keeps `http/1.1` only

## Consequences

- Modal works with credential substitution; other endpoints behave as upstream
- The gateway image build compiles Rust (the dependencies in a layer the patch does not invalidate) and the patch must be re-applied and re-checked on each OpenShell bump, until upstream ships HTTP/2 and this ADR is superseded
- Adding an HTTP/2 endpoint means a new supervisor build, not a profile change
- A sandbox with the Modal provider can run code on Modal, which has open internet access and the workspace's Modal Secrets: the profile's note says so
