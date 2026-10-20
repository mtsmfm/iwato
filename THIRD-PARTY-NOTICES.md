# Third-party notices

Go modules (sources and checksums: `go.mod`, `go.sum`):

- Go standard library/runtime, BSD-3-Clause. Copy in `GO-LICENSE.txt`
- github.com/NVIDIA/OpenShell/sdk/go, Apache-2.0
- github.com/gorilla/websocket, BSD-2-Clause
- github.com/pkg/sftp, BSD-2-Clause; github.com/kr/fs, BSD-3-Clause
- go.yaml.in/yaml/v3, MIT and Apache-2.0
- golang.org/x/crypto, golang.org/x/sys, golang.org/x/net, golang.org/x/text, golang.org/x/sync, golang.org/x/oauth2, BSD-3-Clause
- google.golang.org/grpc, google.golang.org/genproto, Apache-2.0
- google.golang.org/protobuf, BSD-3-Clause
- rsc.io/qr, BSD-3-Clause
- tailscale.com (tsnet, the port exposure), BSD-3-Clause, and what it brings: github.com/tailscale/wireguard-go and golang.zx2c4.com/wireguard/windows, golang.zx2c4.com/wintun, MIT; gvisor.dev/gvisor, Apache-2.0; github.com/klauspost/compress, Apache-2.0 / BSD-3-Clause / MIT; github.com/coder/websocket, ISC; github.com/fxamacker/cbor/v2, MIT; github.com/x448/float16, MIT; github.com/gaissmai/bart, MIT; github.com/go-json-experiment/json, BSD-3-Clause; github.com/google/btree, github.com/golang/groupcache, Apache-2.0; github.com/hdevalence/ed25519consensus, BSD-3-Clause; filippo.io/edwards25519, BSD-3-Clause; github.com/huin/goupnp, BSD-2-Clause; github.com/mitchellh/go-ps, MIT; github.com/pires/go-proxyproto, Apache-2.0; github.com/tailscale/hujson, BSD-3-Clause; github.com/tailscale/certstore, MIT; github.com/tailscale/go-winio, MIT; github.com/tailscale/web-client-prebuilt, BSD-3-Clause; github.com/alexbrainman/sspi, BSD-3-Clause; github.com/dblohm7/wingoes, BSD-3-Clause; github.com/creachadair/msync, BSD-3-Clause; go4.org/mem and go4.org/netipx, Apache-2.0 / BSD-3-Clause; golang.org/x/exp, golang.org/x/term, golang.org/x/time, BSD-3-Clause (`go version -m iwato.exe` lists them)

From [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) v0.1.4, Copyright (c) 2025-2026 NVIDIA CORPORATION & AFFILIATES, Apache-2.0 (full text in `LICENSES/Apache-2.0.txt`). OpenShell v0.1.4 ships no NOTICE file. Files below that Iwato changed say so in their headers or in the notes here:

- the compute driver and extension protos in `internal/openshell/proto/` (unmodified) and the Go code generated from them (`internal/openshell/computev1/`, `internal/openshell/extensionv1/`)
- the Policy Advisor guide in `sandboximage/openshell-skills/`: `policy-advisor/SKILL.md` unmodified; `policy_advisor.md` modified (the section "Iwato additions" at its end is Iwato's own). Each has a header comment naming its source; the image ships the license next to them (`LICENSE-Apache-2.0.txt`)
- `profiles/iwato-github.yaml` and `profiles/iwato-openrouter.yaml` are adapted from `providers/github.yaml` and `providers/openrouter.yaml` (the changes are listed in their headers); the fake's `openai` profile (`fake_providers.go`) is `providers/openai.yaml`
- ports to Go of OpenShell code: the WSLc compute driver `internal/openshell/wslc/` follows the Docker driver (`crates/openshell-driver-docker`), and `endpoint_ambiguity.go` the endpoint ambiguity check (`crates/openshell-policy/src/ambiguity.rs`)
- the patch and scripts in `docs/dev/upstream/` modify or drive OpenShell

Web assets:

- Moralerspace 2.0.0 (Neon HW Regular/Bold, terminal font), SIL Open Font License 1.1. License in `web/vendor/LICENSE-moralerspace.txt`
- CodeMirror 6 and Lezer (the editor tab, `web/vendor/codemirror.js`; with crelt, style-mod, w3c-keyname and @marijn/find-cluster-break), MIT. Versions in `web/vendor/manifest.json`, notices in `web/vendor/LICENSE-codemirror.txt`
- marked (the editor's Markdown preview, `web/vendor/markdown.js`), MIT. Version in `web/vendor/manifest.json`, notice in `web/vendor/LICENSE-codemirror.txt`

The font release archive SHA-256 and the SHA-256 of each vendored file are recorded in `web/vendor/manifest.json`.

Sandbox image (`sandboximage/`):

- the bash setup in `sandboximage/etc/shell/` is adapted from [Omarchy](https://github.com/basecamp/omarchy) (commit a85e29a; `inputrc` and `starship.toml` are copied unchanged; see its `README.md`), under the MIT License. The license is also in `sandboximage/etc/shell/LICENSE`, which the image carries next to the files:

  > Copyright (c) David Heinemeier Hansson
  >
  > Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:
  >
  > The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.
  >
  > THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

Not bundled: Iwato builds or pulls these when it runs. The Ubuntu base image of the sandbox image, the OpenShell gateway, supervisor and sandbox release binaries and images, mise and starship (checked by SHA-256 in `sandboximage/install-tools.sh`) and the tools users install with mise (agent CLIs included). Consult their licenses and update policies before deployment.
