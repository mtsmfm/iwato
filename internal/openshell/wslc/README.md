# OpenShell compute driver for WSLc

An external [OpenShell](https://github.com/NVIDIA/OpenShell) compute driver
that runs sandboxes as WSLc containers through `wslc.exe`, targeting
OpenShell v0.1.6. It ports the contract of OpenShell's Docker driver:

- the workload container runs `openshell-sandbox launch-capability-free`
  as `0:0` with `--network none` (WSLc cannot drop capabilities at create;
  the sandbox drops them, sets no_new_privs and switches to the image's
  non-root identity itself);
- the supervisor container runs from the official supervisor image on the
  WSLc network it shares with the gateway (WSLc rejects `--network host`)
  and reaches the gateway by name;
- boundary files are copied in as tar archives carrying ownership
  (`wslc container cp -`); the launch credentials are split as the Docker
  driver does (the supervisor bundle never reaches the workload);
- each start is a new generation, to which the bootstrap, the descriptor
  and their provisioning evidence are bound again. A sandbox created before
  v0.1.5 keeps its pinned runtime, which reads the evidence as `outer_fence`.

Beyond that it handles `driver_config` mounts in the Docker driver's schema
(named volumes and tmpfs; binds, image mounts and CDI devices are refused;
as with Docker, the sandbox policy must allow the target, and a new volume is
owned by root; unlike Docker, a writable volume may be the workspace itself,
as Iwato's home is in images whose `WORKDIR` is the home: the standard image
before the image convention, `docs/adr/0005-sandbox-image-convention.md`),
CPU and memory limits, the image pull policy, a start of a running
workload (a fresh instance after its main process ended) as a new session,
since the runtime stays bound to its first supervisor, stop/start with a new session
(fresh TLS material and launch credentials copied into the stopped
workload), and cleanup of leftovers at startup. The Policy Advisor guide
(`/etc/openshell/skills`), which the v0.1.4 supervisor means to install but
writes into its own container (NVIDIA/OpenShell#3698; checked with the Docker
driver by `docs/dev/upstream/skill-check-docker.sh`), and Iwato's daemon come
with the image (the image convention); the driver copied them into the
workloads it created before that, which keep them. For those sandboxes only
(their main process runs the daemon from `/usr/local/lib/iwato/iwato-term`),
`-legacy-term` gives the current daemon on each start, until they are
recreated. With `-guest-tls-ca` it gives
the supervisors the gateway's CA for a TLS gateway (they authenticate with
their sandbox's bearer token, so no client certificate, as the Docker driver).

The gateway reaches the driver over a Unix socket only. On Windows the
gateway container relays that socket with socat to `host.wslc.internal`,
and the driver listens with `-listen tls://127.0.0.1:17671`, accepting only
the relay's client certificate (`openshell-driver-wslc certs <dir>` creates
the CA, the server certificate and the relay's), since every WSLc container
can reach Windows' loopback.

`../gateway/` builds a gateway image from the official release binaries and
`../gateway/try.ps1` creates the certificates, starts the driver and an mTLS
gateway, and runs sandboxes.

`e2e.sh` runs the released gateway, CLI, sandbox and supervisor
against this driver with `fakewslc` in place of `wslc.exe`
(`MODE=tls` for the mTLS setup, which also runs iwato's Go e2e tests
against it: `TestOpenShell*E2E`, built once into one test binary before
the run moves `HOME`, and OpenShell's lint of `profiles/`; `ONLY=iwato`
skips the driver checks and runs just those). It prints each section's
time (`TIME`), waits for states rather than fixed times, and removes its
work directory at the end (`KEEP=1` keeps it after a failure, for the
logs). Sandboxes here share the host's network, so each new one waits only
until no earlier sandbox still binds the DNS relay's port 53; closed
connections left in TIME_WAIT do not keep the next one from binding it.
`go test` covers the rest. `fakewslc` follows the real CLI where it is known (WSL's
argument definitions and what WSL 3.0.1 did: refused options and
`--network host`, `stop --time`, exit codes, logs on stderr, exec as the
container's user, images only once pulled). It cannot reproduce mounts,
network isolation or a first process that is PID 1: on WSLc the v0.1.2
sandbox does not exit after SIGTERM (v0.1.6 keeps the code that hangs), which
the e2e does not show.

GPUs: a `resources.gpu` request (no count, or 1) becomes `--gpus all`, the
`openshell.gpu` resource claim in the bootstrap and descriptor (the supervisor
then lets the workload open `/dev/dxg` and read `/usr/lib/wsl`) and the
`iwato.gpu=true` label. WSLc offers `all` only, so a count above 1 and CDI
devices are refused. `fakewslc` records the flag (`FAKE_WSLC_GPU=0` makes it fail like a session without GPU support); a real GPU needs Windows
(`docs/dev/gpu.md`).

Every sandbox, with or without a GPU, gets `--shm-size 1g` (WSLc's 64 MB
/dev/shm crashes browsers, e.g. the Selkies desktop of the standard image): a
cap on the tmpfs, not an allocation.

Not supported: PID limits and resource admission. WSLc has no `--cap-drop` or no-new-privileges, so the workload
relies on `launch-capability-free` dropping them.

Regenerate `../computev1` and `../extensionv1` from `../proto` (OpenShell v0.1.6; the other protos come from its Go SDK, so mapped with `M` options) with protoc
29.6, protoc-gen-go 1.36.11 and protoc-gen-go-grpc 1.6.2.
