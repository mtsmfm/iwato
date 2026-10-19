package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"iwato/internal/termd"
)

func TestSandboxPolicyAllowsNoNetworkAndWritesTheHome(t *testing.T) {
	w := newSandbox("dev")
	p := sandboxPolicy(w)
	if len(p.NetworkPolicies) != 0 {
		t.Fatalf("network allowed: %+v", p.NetworkPolicies)
	}
	// An older sandbox's path outside the home stays writable (Start's
	// automatic re-creation) until the owner recreates it.
	w.LegacyPersist = []string{"/work"}
	if !slices.Contains(sandboxPolicy(w).Filesystem.ReadWrite, "/work") {
		t.Fatalf("an older sandbox's persisted path is not writable: %v", sandboxPolicy(w).Filesystem.ReadWrite)
	}
	for _, dir := range []string{homeDir, "/tmp"} {
		if !strings.Contains(strings.Join(p.Filesystem.ReadWrite, " "), dir) {
			t.Fatalf("%s is not writable: %v", dir, p.Filesystem.ReadWrite)
		}
	}
	if !p.Filesystem.IncludeWorkdir {
		t.Fatal("the workspace is not writable")
	}
}

// The sandbox image gives no sudo and assumes no egress proxy: OpenShell's
// policy is the network boundary.
func TestSandboxImageHasNoSudoOrEgressProxy(t *testing.T) {
	d := t.TempDir()
	if err := writeImageContext(d); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Dockerfile", "AGENTS.md", "etc/shell/envs", "etc/gitconfig", "desktop/iwato-desktop", "desktop/gpu-env.sh"} {
		b, err := imageFiles.ReadFile("sandboximage/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"sudo apt", "NOPASSWD", "egress proxy"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s mentions %q", name, bad)
			}
		}
	}
}

// The image is generic: no agent CLI or agent settings, and its
// environment instructions are /AGENTS.md (which OpenShell then keeps
// instead of installing its own). OpenShell's Policy Advisor guide
// (openshell-skills) is OpenShell's text.
func TestSandboxImageIsAgentNeutral(t *testing.T) {
	err := fs.WalkDir(imageFiles, "sandboximage", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "sandboximage/AGENTS.md" || strings.HasPrefix(p, "sandboximage/openshell-skills/") {
			return err
		}
		b, err := imageFiles.ReadFile(p)
		if err != nil {
			return err
		}
		for _, bad := range []string{"claude", "codex", "grok", "[tools]"} {
			if strings.Contains(strings.ToLower(string(b)), bad) && !(p == "sandboximage/Dockerfile" && bad == "claude") {
				t.Errorf("%s mentions %q", p, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := imageFiles.ReadFile("sandboximage/Dockerfile")
	if !strings.Contains(string(b), "ln -s /etc/iwato/AGENTS.md /AGENTS.md") {
		t.Error("the image does not install /AGENTS.md")
	}
}

// Nothing an agent reads in the sandbox may contain OpenShell's placeholder
// prefix: the agent sends the text with every request to its model's API,
// and OpenShell refuses a body with a placeholder it cannot vouch for, so
// the session stops ("A credential placeholder in the request body cannot
// be forwarded").
func TestSandboxTextsHoldNoPlaceholder(t *testing.T) {
	const prefix = "openshell" + ":resolve:"
	check := func(fsys fs.FS, root string) {
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), prefix) {
				t.Errorf("%s contains %q", p, prefix)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(imageFiles, "sandboximage")
}

// The one image has the desktop, installs it as root only at build time, and
// ends as dev. There is no separate desktop image or base-image argument.
func TestSandboxImageHasTheDesktopAndEndsAsDev(t *testing.T) {
	b, err := imageFiles.ReadFile("sandboximage/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"COPY desktop/iwato-desktop /usr/local/bin/iwato-desktop", "COPY desktop/gpu-env.sh", "Pin-Priority", "selkies-session"} {
		if !strings.Contains(text, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	// The image convention: the workspace is outside the home, and the
	// image carries the daemon (named by its label) and the guide.
	if !strings.HasSuffix(strings.TrimSpace(text), "USER dev\nWORKDIR /sandbox\nCMD [\"/bin/bash\", \"-l\"]") {
		t.Error("the image must end as dev, in /sandbox")
	}
	for _, want := range []string{"LABEL " + daemonLabel + "=" + termd.Path, "/usr/local/lib/iwato/iwato-entrypoint", "COPY openshell-skills/ /etc/openshell/skills/"} {
		if !strings.Contains(text, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
	if strings.Contains(text, "BASE_IMAGE") {
		t.Error("the image has no base image argument")
	}
	// The base image is pinned by digest, and so is the Selkies package.
	if !regexp.MustCompile(`(?m)^FROM ubuntu:24\.04@sha256:[0-9a-f]{64}$`).MatchString(text) {
		t.Error("the base image must be pinned by digest")
	}
	if !regexp.MustCompile(`2\.0\.0-amd64\) want=[0-9a-f]{64} ;;`).MatchString(text) || !regexp.MustCompile(`2\.0\.0-arm64\) want=[0-9a-f]{64} ;;`).MatchString(text) {
		t.Error("the Selkies package's SHA-256 must be pinned per architecture")
	}
	if _, err := imageFiles.ReadFile("sandboximage/Dockerfile.desktop"); err == nil {
		t.Error("there is no separate desktop Dockerfile")
	}
	// The desktop steps fail the build loudly, naming the step.
	for _, want := range []string{"desktop: cannot download Mozilla", "desktop: Mozilla apt key fingerprint", "desktop: cannot download the Selkies package"} {
		if !strings.Contains(text, want) {
			t.Errorf("Dockerfile lacks the message %q", want)
		}
	}
	// The heavy desktop layers come before the layers that change often.
	if strings.Index(text, "selkies.deb") > strings.Index(text, "COPY etc/") {
		t.Error("the desktop layers must come before COPY etc/ for cache reuse")
	}
	// The GPU settings are conditional on /dev/dxg, so no ENV for them.
	if strings.Contains(text, "ENV GALLIUM_DRIVER") || strings.Contains(text, "ENV LIBVA_DRIVER_NAME") {
		t.Error("GALLIUM_DRIVER/LIBVA_DRIVER_NAME must not be set unconditionally")
	}
	// The sandbox AGENTS.md tells agents about the desktop.
	a, _ := imageFiles.ReadFile("sandboximage/AGENTS.md")
	for _, want := range []string{"iwato-desktop", "6080", "「ポート」"} {
		if !strings.Contains(string(a), want) {
			t.Errorf("sandboximage/AGENTS.md lacks %q", want)
		}
	}
}

func runDesktopScript(t *testing.T, env ...string) string {
	t.Helper()
	d := prepareDesktop(t)
	return d + "\n" + runDesktopIn(t, d, env...)
}

// prepareDesktop writes the image context into a temp dir (also HOME, with
// /tmp and /proc stand-ins under it) and a stub selkies-session in bin/.
func prepareDesktop(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if err := writeImageContext(d); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(d, "bin")
	if err := os.MkdirAll(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stub, "selkies-session"), []byte("#!/bin/sh\necho \"selkies-session $*\"\necho \"LD=${LD_LIBRARY_PATH:-} GALLIUM=${GALLIUM_DRIVER:-}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(d, "sandboximage", "desktop", "iwato-desktop")
	// The script sources the installed copy of gpu-env.sh.
	src, _ := os.ReadFile(script)
	patched := strings.ReplaceAll(string(src), ". /etc/iwato/desktop/gpu-env.sh", ". "+filepath.Join(d, "sandboximage", "desktop", "gpu-env.sh"))
	if err := os.WriteFile(script, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d, "tmp", ".X11-unix"), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

// runDesktopIn runs iwato-desktop prepared by prepareDesktop. IWATO_TMP is the
// temp dir's tmp/ (never the real /tmp, which the script cleans).
func runDesktopIn(t *testing.T, d string, env ...string) string {
	t.Helper()
	c := exec.Command("sh", filepath.Join(d, "sandboximage", "desktop", "iwato-desktop"), "run")
	c.Env = append([]string{"PATH=" + filepath.Join(d, "bin") + ":" + os.Getenv("PATH"), "HOME=" + d, "IWATO_TMP=" + filepath.Join(d, "tmp")}, env...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// With no GPU the script skips the NVIDIA part and still starts the session.
func TestDesktopScriptWithoutAGPU(t *testing.T) {
	out := runDesktopScript(t, "IWATO_DXG=/nonexistent/dxg")
	if !strings.Contains(out, "selkies-session --session xfce --port=6080 --enable-basic-auth=false --enable-https=false") {
		t.Errorf("%s", out)
	}
	if strings.Contains(out, "GALLIUM=d3d12") {
		t.Errorf("GALLIUM_DRIVER set without /dev/dxg:\n%s", out)
	}
}

// With /dev/dxg it links the newest driver store's libcuda under the sonames
// and puts the directory first in LD_LIBRARY_PATH.
func TestDesktopScriptLinksTheDriverStore(t *testing.T) {
	root := t.TempDir()
	dxg := filepath.Join(root, "dxg")
	store := filepath.Join(root, "drivers", "nv_dispi.inf_amd64_abc")
	for _, f := range []string{dxg, filepath.Join(store, "libcuda.so.1.1"), filepath.Join(store, "libnvidia-encode.so.1")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nv := filepath.Join(root, "nv")
	out := runDesktopScript(t, "IWATO_DXG="+dxg, "IWATO_WSL_DRIVERS="+filepath.Join(root, "drivers"), "IWATO_NVIDIA_LIBS="+nv, "LD_LIBRARY_PATH=/other")
	for name, want := range map[string]string{"libcuda.so.1": "libcuda.so.1.1", "libcuda.so": "libcuda.so.1.1", "libnvidia-encode.so.1": "libnvidia-encode.so.1"} {
		if got, err := os.Readlink(filepath.Join(nv, name)); err != nil || got != filepath.Join(store, want) {
			t.Errorf("%s -> %q, %v", name, got, err)
		}
	}
	if !strings.Contains(out, "LD="+nv+":/other GALLIUM=d3d12") {
		t.Errorf("%s", out)
	}
}
