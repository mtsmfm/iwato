package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"iwato/internal/openshell/wslc"
)

const version = "0.1.0"
const configVersion = 1

// ownerKey is the label that marks an engine resource as this
// configuration's.
const ownerKey = "iwato.owner"

var safeID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var ownerID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Config struct {
	Version  int    `json:"version"`
	Instance string `json:"instance"`
	// Namespace names the engine's resources (iwato-<namespace>-…), so
	// that several configurations can share one engine (a WSLc session).
	Namespace string `json:"namespace"`
	// WSLC is the wslc executable (Windows). Without it the engine is a
	// Docker-compatible one through the CLI Docker names (default "docker").
	WSLC    string `json:"wslc,omitempty"`
	Session string `json:"session,omitempty"`
	Docker  string `json:"docker,omitempty"`
	// DockerSocket overrides where the engine's API socket is on the
	// engine's side, for the gateway's Docker driver (dockercli.Socket).
	DockerSocket string `json:"dockerSocket,omitempty"`
	// Ingress exposes the sandboxes' ports (ingress_config.go).
	Ingress *IngressConfig `json:"ingress,omitempty"`
	// TermTrace records what every terminal outputs, and its sizes, in the
	// sandbox's ~/.local/state/iwato/term-trace, to investigate a broken
	// screen later (internal/hostterm/trace.go, traceScript). The files
	// hold whatever the terminals showed.
	TermTrace bool `json:"termTrace,omitempty"`
	// Timezone is the sandboxes' time zone (timezone.go): "" the host's.
	Timezone string `json:"timezone,omitempty"`
	// ProviderScopes are the access scopes of the providers with their own
	// profile, by provider name (providers_scope.go).
	ProviderScopes map[string]ProviderScope `json:"providerScopes,omitempty"`
	Sandboxes      []Sandbox                `json:"sandboxes"`
}

// Sandbox is one OpenShell sandbox.
type Sandbox struct {
	// Name is the OpenShell sandbox name.
	Name string `json:"name"`
	// Image is any image reference the engine can use, as `openshell
	// sandbox create --from`; empty means Iwato's sandbox image.
	Image string `json:"image,omitempty"`
	// LegacyPersist are paths outside the home that a sandbox keeps on
	// volumes of their own. Under the image convention (docs/adr/0005)
	// only the home persists: a new sandbox has none, and the owner's
	// recreate clears them (the new sandbox does not mount those volumes,
	// which stay in the volumes screen). Until then the sandbox mounts them
	// (also when Start makes a failed one again), the volumes show as used
	// by it, and a delete with its volumes removes them too.
	LegacyPersist []string `json:"persist,omitempty"`
	// Presets are the network presets (presets.go) the sandbox has: those
	// it was created with (their rules are in its first policy) and those
	// put into its policy later. A recreate creates it with them all.
	Presets []PresetRef `json:"presets,omitempty"`
	// Providers are the OpenShell providers attached to the sandbox.
	// OpenShell keeps them once it has the sandbox; this list is what
	// it is created with (at the first start, or again after a failure).
	Providers []string `json:"providers,omitempty"`
	// GPU asks OpenShell for a GPU (SandboxSpec resources.gpu); the WSLc
	// driver passes the machine's GPU through (--gpus all). It applies when
	// OpenShell creates the sandbox.
	GPU bool `json:"gpu,omitempty"`
	// ProvidersChangedAt is when a provider was attached or detached
	// while the sandbox ran: its processes from before (the main
	// terminal's included) lack the change until it is restarted.
	// Starting the sandbox clears it.
	ProvidersChangedAt time.Time `json:"providersChangedAt,omitzero"`
	// ProviderApplying are the provider changes the running sandbox has
	// not applied yet (OpenShell's receipts), and those it could not.
	// Kept here so that a restarted Iwato still follows them; stopping or
	// starting the sandbox clears them.
	ProviderApplying []ProviderApply `json:"providerApplying,omitempty"`
	// Ingress overrides the port exposure for this sandbox.
	Ingress *SandboxIngress `json:"ingress,omitempty"`
	// Carry is the network policy a recreate (recreate.go) carries over,
	// saved before the old sandbox is deleted and cleared once the new
	// one has it: a recreate or start after a failure applies it.
	Carry *CarriedPolicy `json:"carry,omitempty"`
}

// ProviderApply is a provider change a running sandbox applies on its own
// (on its next settings poll).
type ProviderApply struct {
	Provider string    `json:"provider"`
	Attach   bool      `json:"attach"`
	Receipt  string    `json:"receipt"`
	At       time.Time `json:"at"`
	// Scope: the change is of the provider's scope (its profile), not an
	// attach or detach; running processes need no restart for it.
	Scope bool `json:"scope,omitempty"`
	// State is "" while pending, else withheld or failed (with Reason).
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// usesDocker: the engine is a Docker-compatible one, as the configuration
// names no wslc (never on Windows: Validate refuses it).
func (c Config) usesDocker() bool { return c.WSLC == "" }

// dockerCLI is the docker executable to run.
func (c Config) dockerCLI() string {
	if c.Docker != "" {
		return c.Docker
	}
	return "docker"
}

// defaultConfig is a new configuration without sandboxes: the owner creates
// each one explicitly.
func defaultConfig() Config {
	c := Config{Version: configVersion, Instance: randomHex(16), Namespace: "personal", Sandboxes: []Sandbox{}}
	if runtime.GOOS == "windows" {
		c.WSLC = "wslc.exe"
	}
	return c
}

func newSandbox(name string) Sandbox { return Sandbox{Name: name} }

func readConfig(path string) (Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Config{}, e
	}
	if len(b) > 1<<20 {
		return Config{}, errors.New("config exceeds 1 MiB")
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return c, errors.New("trailing JSON data")
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Version != configVersion {
		return fmt.Errorf("unsupported config version (expected %d)", configVersion)
	}
	if !ownerID.MatchString(c.Instance) {
		return errors.New("instance must be 32 lowercase hex digits")
	}
	if !safeID.MatchString(c.Namespace) {
		return errors.New("invalid namespace")
	}
	// Windows always uses WSLc (docs/adr/0002).
	if (runtime.GOOS == "windows" && strings.TrimSpace(c.WSLC) == "") || strings.ContainsAny(c.WSLC, "\x00\r\n") {
		return errors.New("invalid wslc executable")
	}
	if strings.ContainsAny(c.Docker, "\x00\r\n") || (c.DockerSocket != "" && (!strings.HasPrefix(c.DockerSocket, "/") || strings.ContainsAny(c.DockerSocket, "\x00\r\n,="))) {
		return errors.New("invalid docker executable")
	}
	if strings.ContainsAny(c.Session, "\x00\r\n") {
		return errors.New("invalid WSLc session")
	}
	if e := c.Ingress.Validate(); e != nil {
		return fmt.Errorf("ingress: %w", e)
	}
	if _, ok := normalTimezone(c.Timezone); c.Timezone != "" && !ok {
		return errors.New("invalid timezone")
	}
	if e := validScopes(c.ProviderScopes); e != nil {
		return e
	}
	if len(c.Sandboxes) > 16 {
		return errors.New("at most 16 sandboxes")
	}
	seen := map[string]bool{}
	for _, w := range c.Sandboxes {
		if !safeID.MatchString(w.Name) || seen[w.Name] {
			return fmt.Errorf("invalid or duplicate sandbox name %q", w.Name)
		}
		seen[w.Name] = true
		if w.Image != "" && !validImage(w.Image) {
			return fmt.Errorf("invalid sandbox image for %s", w.Name)
		}
		if e := validPersist(w.LegacyPersist); e != nil {
			return fmt.Errorf("persist for %s: %w", w.Name, e)
		}
		for _, a := range w.ProviderApplying {
			if !providerNamePattern.MatchString(a.Provider) || len(a.Receipt) > 200 || len(a.State) > 64 || len(a.Reason) > 200 {
				return fmt.Errorf("provider change of %s: invalid entry", w.Name)
			}
		}
		if e := validProviderList(w.Providers); e != nil {
			return fmt.Errorf("providers for %s: %w", w.Name, e)
		}
		if e := w.Ingress.Validate(); e != nil {
			return fmt.Errorf("ingress for %s: %w", w.Name, e)
		}
		if w.Carry != nil && !json.Valid(w.Carry.Rules) {
			return fmt.Errorf("carried policy of %s: invalid JSON", w.Name)
		}
	}
	return nil
}

// homeDir is the sandbox user's home. Iwato sets HOME to it and keeps it on
// a volume: only what is under it outlives the sandbox (the image
// convention, docs/adr/0005). The workspace (the image's WORKDIR, /sandbox)
// is outside it and is not kept; terminals start in workDir under the home,
// and the files tab, the editor and image paste see the home through the
// daemon's SFTP server.
const homeDir = "/home/dev"

// workDir is where work happens, under the home: terminals start there
// (made when missing).
const workDir = "work"

// volumePaths are the paths the sandbox keeps on volumes: the home, then
// the legacy ones (LegacyPersist) of a sandbox made before the convention.
// A new sandbox has none of those, and the explicit recreate clears them
// first, so only Start's automatic re-creation of a failed sandbox that the
// owner has not recreated mounts them again (and keeps them writable):
// volumes outside the home are dropped only when the owner chooses to.
func (w Sandbox) volumePaths() []string { return append([]string{homeDir}, w.LegacyPersist...) }

// maxPersist bounds a sandbox's persisted paths.
const maxPersist = 8

// validPersist accepts distinct, non-overlapping paths the WSLc driver can
// mount a volume on (a configuration's LegacyPersist).
func validPersist(paths []string) error {
	if len(paths) > maxPersist {
		return fmt.Errorf("at most %d paths", maxPersist)
	}
	for i, p := range paths {
		n, err := wslc.CheckMountTarget(p)
		if err != nil {
			return err
		}
		if n != p {
			return fmt.Errorf("%q must be written as %q", p, n)
		}
		if p == homeDir || strings.HasPrefix(p, homeDir+"/") || strings.HasPrefix(homeDir, p+"/") {
			return fmt.Errorf("%q is in the home %s, which is always kept", p, homeDir)
		}
		for _, q := range paths[:i] {
			if p == q || strings.HasPrefix(p, q+"/") || strings.HasPrefix(q, p+"/") {
				return fmt.Errorf("%q overlaps %q", p, q)
			}
		}
	}
	return nil
}

func validImage(s string) bool {
	return s != "" && s[0] != '-' && len(s) < 300 && !strings.ContainsAny(s, "\x00 \t\r\n\\\"'")
}

func (c Config) Sandbox(name string) (Sandbox, bool) {
	for _, w := range c.Sandboxes {
		if w.Name == name {
			return w, true
		}
	}
	return Sandbox{}, false
}

// prefix starts the names of this configuration's engine resources.
func (c Config) prefix() string { return "iwato-" + c.Namespace }

// resourceName names one of a sandbox's engine resources.
func (c Config) resourceName(w Sandbox, kind string) string {
	return c.prefix() + "-" + w.Name + "-" + kind
}
