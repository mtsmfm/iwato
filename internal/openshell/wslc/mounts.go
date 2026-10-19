package wslc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computev1 "iwato/internal/openshell/computev1"
)

// driverConfig is template.driver_config, in the Docker driver's schema.
// WSLc can mount named volumes and tmpfs; binds, image mounts and CDI
// devices are refused.
type driverConfig struct {
	CDIDevices []string      `json:"cdi_devices"`
	Mounts     []driverMount `json:"mounts"`
}

type driverMount struct {
	Type      string   `json:"type"`
	Source    string   `json:"source"`
	Target    string   `json:"target"`
	ReadOnly  *bool    `json:"read_only"`
	Subpath   *string  `json:"subpath"`
	Options   []string `json:"options"`
	SizeBytes *float64 `json:"size_bytes"`
	Mode      *float64 `json:"mode"`
	// Accepted for schema compatibility; binds are refused anyway.
	SelinuxLabel *string `json:"selinux_label"`
}

// Paths OpenShell reserves inside sandboxes (container_paths.rs).
var (
	controlRoots    = []string{"/opt/openshell", "/etc/openshell", "/etc/openshell-tls", "/run/openshell", "/run/openshell-sidecar", "/run/netns", "/var/run/netns"}
	ociRuntimeRoots = []string{"/proc", "/sys", "/dev"}
)

func parseDriverConfig(tmpl *computev1.DriverSandboxTemplate) (driverConfig, error) {
	var cfg driverConfig
	if tmpl.GetDriverConfig() == nil || len(tmpl.GetDriverConfig().GetFields()) == 0 {
		return cfg, nil
	}
	raw, err := tmpl.GetDriverConfig().MarshalJSON()
	if err != nil {
		return cfg, status.Error(codes.InvalidArgument, err.Error())
	}
	// As the Docker driver's serde types: deny unknown fields at the top
	// and per mount type (one struct per type upstream).
	var top struct {
		CDIDevices *[]string         `json:"cdi_devices"`
		Mounts     []json.RawMessage `json:"mounts"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&top); err != nil {
		return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: %v", err)
	}
	if top.CDIDevices != nil {
		if len(*top.CDIDevices) == 0 {
			return cfg, status.Error(codes.InvalidArgument, "invalid wslc driver_config: cdi_devices must be a non-empty list of strings")
		}
		return cfg, status.Error(codes.FailedPrecondition, "CDI devices are not supported by the wslc driver")
	}
	for i, rawMount := range top.Mounts {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawMount, &fields); err != nil {
			return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: mounts[%d]: %v", i, err)
		}
		var typ string
		_ = json.Unmarshal(fields["type"], &typ)
		allowed, required := mountFields[typ], mountRequired[typ]
		if allowed == nil {
			return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: mounts[%d]: unknown mount type %q", i, typ)
		}
		for k := range fields {
			if !slices.Contains(allowed, k) {
				return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: mounts[%d]: unknown field %q for a %s mount", i, k, typ)
			}
		}
		for _, k := range required {
			if _, ok := fields[k]; !ok {
				return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: mounts[%d]: missing field %q", i, k)
			}
		}
		var m driverMount
		if err := json.Unmarshal(rawMount, &m); err != nil {
			return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: mounts[%d]: %v", i, err)
		}
		cfg.Mounts = append(cfg.Mounts, m)
	}
	seen := map[string]bool{}
	for _, m := range cfg.Mounts {
		switch m.Type {
		case "volume":
			if err := validSource(m.Source); err != nil {
				return cfg, err
			}
			if m.Subpath != nil {
				return cfg, status.Error(codes.FailedPrecondition, "volume subpath is not supported by the wslc driver")
			}
		case "tmpfs":
			if err := integral(m.SizeBytes, "tmpfs size_bytes", 1); err != nil {
				return cfg, err
			}
			if err := integral(m.Mode, "tmpfs mode", 0); err != nil {
				return cfg, err
			}
			for _, o := range m.Options {
				if _, err := tmpfsOption(o); err != nil {
					return cfg, err
				}
			}
		case "bind":
			return cfg, status.Error(codes.FailedPrecondition, "bind mounts are not supported by the wslc driver; use a named volume")
		case "image":
			return cfg, status.Error(codes.FailedPrecondition, "image mounts are not supported by the wslc driver")
		default:
			return cfg, status.Errorf(codes.InvalidArgument, "invalid wslc driver_config: unknown mount type %q", m.Type)
		}
		target, err := validTarget(m.Target)
		if err != nil {
			return cfg, err
		}
		if seen[target] {
			return cfg, status.Errorf(codes.FailedPrecondition, "duplicate mount target %q", m.Target)
		}
		seen[target] = true
	}
	return cfg, nil
}

// mountFields and mountRequired are each mount type's fields, as the
// Docker driver's DockerDriverMountConfig variants (deny_unknown_fields).
var (
	mountFields = map[string][]string{
		"bind":   {"type", "source", "target", "read_only", "selinux_label"},
		"volume": {"type", "source", "target", "read_only", "subpath"},
		"tmpfs":  {"type", "target", "options", "size_bytes", "mode"},
		"image":  {"type", "source", "target", "read_only", "subpath"},
	}
	mountRequired = map[string][]string{"bind": {"source", "target"}, "volume": {"source", "target"}, "tmpfs": {"target"}, "image": {"source", "target"}}
)

// integral checks an optional JSON number as validate_optional_integral_i64
// does: finite, whole, an int64, and at least min.
func integral(v *float64, field string, min int64) error {
	if v == nil {
		return nil
	}
	if math.IsInf(*v, 0) || math.IsNaN(*v) || *v != math.Trunc(*v) {
		return status.Errorf(codes.FailedPrecondition, "%s must be an integer", field)
	}
	if *v >= math.MaxInt64 || *v < math.MinInt64 {
		return status.Errorf(codes.FailedPrecondition, "%s must be representable as an i64", field)
	}
	if int64(*v) < min {
		if min > 0 {
			return status.Errorf(codes.FailedPrecondition, "%s must be positive", field)
		}
		return status.Errorf(codes.FailedPrecondition, "%s must be zero or greater", field)
	}
	return nil
}

// tmpfsOption trims one option as docker_tmpfs_option does; a comma would
// split wslc's --tmpfs value, so it is refused too.
func tmpfsOption(o string) (string, error) {
	o = strings.TrimSpace(o)
	if o == "" {
		return "", status.Error(codes.FailedPrecondition, "tmpfs options must not contain empty values")
	}
	if strings.Contains(o, ",") {
		return "", status.Errorf(codes.FailedPrecondition, "invalid tmpfs option %q", o)
	}
	if k, v, ok := strings.Cut(o, "="); ok {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" || v == "" {
			return "", status.Error(codes.FailedPrecondition, "tmpfs key=value options must include both key and value")
		}
		return k + "=" + v, nil
	}
	return o, nil
}

func validSource(s string) error {
	switch {
	case s == "":
		return status.Error(codes.FailedPrecondition, "volume source must not be empty")
	case s != strings.TrimSpace(s) || strings.ContainsAny(s, "\x00,="):
		return status.Errorf(codes.FailedPrecondition, "invalid volume source %q", s)
	}
	return nil
}

// validTarget normalizes a container path and rejects OpenShell's reserved
// roots (validate_container_mount_target).
func validTarget(t string) (string, error) {
	n, err := normalizePath(t, "mount target")
	if err != nil {
		return "", status.Error(codes.FailedPrecondition, err.Error())
	}
	if strings.ContainsAny(n, ",=") {
		return "", status.Errorf(codes.FailedPrecondition, "mount target %q must not contain ',' or '='", t)
	}
	for _, r := range controlRoots {
		if overlap(n, r) {
			return "", status.Errorf(codes.FailedPrecondition, "mount target %q conflicts with reserved OpenShell path %q", t, r)
		}
	}
	return n, nil
}

func normalizePath(v, field string) (string, error) {
	switch {
	case v == "":
		return "", fmt.Errorf("%s must not be empty", field)
	case v != strings.TrimSpace(v) || strings.IndexFunc(v, unicode.IsControl) >= 0:
		return "", fmt.Errorf("%s must not contain whitespace at the ends or control characters", field)
	case !strings.HasPrefix(v, "/"):
		return "", fmt.Errorf("%s must be an absolute container path", field)
	}
	segs := strings.Split(v, "/")[1:]
	for i, s := range segs {
		if (s == "" && i < len(segs)-1) || s == "." || s == ".." {
			return "", fmt.Errorf("%s must be normalized without empty, '.', or '..' segments", field)
		}
	}
	n := strings.TrimRight(v, "/")
	if n == "" {
		return "", fmt.Errorf("%s must not be the container root", field)
	}
	return n, nil
}

func under(p, parent string) bool { return p == parent || strings.HasPrefix(p, parent+"/") }
func overlap(a, b string) bool    { return under(a, b) || under(b, a) }

// resolveWorkspace follows resolve_oci_workspace_root.
func resolveWorkspace(workdir string) (string, error) {
	if workdir == "" || workdir == "/" {
		return defaultWorkspace, nil
	}
	ws, err := normalizePath(workdir, "OCI WorkingDir")
	if err != nil {
		return "", err
	}
	// validate_workspace_control_path: not in the OpenShell channel.
	if overlap(ws, path.Dir(channelMount)) {
		return "", fmt.Errorf("OCI WorkingDir %q conflicts with the OpenShell channel", workdir)
	}
	for _, r := range append(append([]string{}, ociRuntimeRoots...), controlRoots...) {
		if overlap(ws, r) {
			return "", fmt.Errorf("OCI WorkingDir %q conflicts with reserved path %q", workdir, r)
		}
	}
	return ws, nil
}

// mountFlags checks the mounts against the workspace and the channel, and
// that each volume exists, then renders them as wslc flags.
func (d *driver) mountFlags(ctx context.Context, cfg driverConfig, workspace string, imageVolumes []string) ([]string, error) {
	var flags []string
	// As the Docker driver does: an image volume may sit below the
	// workspace, but not mask it, the OpenShell channel or a reserved path.
	for _, v := range imageVolumes {
		target, err := validTarget(v)
		if err != nil {
			return nil, fmt.Errorf("invalid image-declared volume %q: %w", v, err)
		}
		if under(workspace, target) {
			return nil, fmt.Errorf("image-declared volume %q masks the workspace %q", v, workspace)
		}
		if overlap(target, channelMount) || overlap(target, path.Dir(sandboxBinaryPath)) {
			return nil, fmt.Errorf("image-declared volume %q conflicts with the OpenShell channel", v)
		}
	}
	for _, m := range cfg.Mounts {
		target, _ := validTarget(m.Target)
		// Unlike the Docker driver, a writable volume may be the workspace
		// itself: Iwato's home in an image whose WORKDIR is the home (the
		// standard image before the image convention, docs/adr/0005, and
		// the sandboxes made from it, which a start or a failed sandbox's
		// re-creation must keep running). An image of the convention has
		// its WORKDIR outside the home and does not need it. It still may
		// not contain the workspace.
		if under(workspace, target) && !(target == workspace && m.Type == "volume" && m.ReadOnly != nil && !*m.ReadOnly) {
			return nil, fmt.Errorf("mount target %q is reserved for the OpenShell workspace", m.Target)
		}
		if overlap(target, channelMount) || overlap(target, path.Dir(sandboxBinaryPath)) {
			return nil, fmt.Errorf("mount target %q conflicts with the OpenShell channel", m.Target)
		}
		switch m.Type {
		case "volume":
			if err := d.volumeExists(ctx, m.Source); err != nil {
				return nil, err
			}
			spec := "type=volume,source=" + m.Source + ",target=" + target
			if m.ReadOnly == nil || *m.ReadOnly {
				spec += ",readonly"
			}
			flags = append(flags, "--mount", spec)
		case "tmpfs":
			var opts []string
			for _, o := range m.Options {
				t, _ := tmpfsOption(o)
				opts = append(opts, t)
			}
			if m.SizeBytes != nil {
				opts = append(opts, fmt.Sprintf("size=%d", int64(*m.SizeBytes)))
			}
			if m.Mode != nil {
				opts = append(opts, fmt.Sprintf("mode=%o", int64(*m.Mode)))
			}
			spec := target
			if len(opts) > 0 {
				spec += ":" + strings.Join(opts, ",")
			}
			flags = append(flags, "--tmpfs", spec)
		}
	}
	return flags, nil
}

// volumeExists checks a driver_config volume (the Docker driver checks at
// validation too).
func (d *driver) volumeExists(ctx context.Context, name string) error {
	var v map[string]any
	if err := d.w.inspect(ctx, "volume", name, &v); err != nil {
		if errors.Is(err, errNotFound) {
			return fmt.Errorf("volume %q does not exist; create it with wslc volume create", name)
		}
		return err
	}
	return nil
}

// cpuLimit turns a Kubernetes quantity ("500m", "2", "1.5") into wslc's
// --cpus, accepting what the Docker driver's parse_cpu_limit accepts.
func cpuLimit(q string) (string, error) {
	q = strings.TrimSpace(q)
	if m, ok := strings.CutSuffix(q, "m"); ok {
		n, err := strconv.ParseInt(m, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid cpu limit %q; expected an integer or millicore quantity", q)
		}
		if n <= 0 {
			return "", errors.New("cpu limit must be greater than zero")
		}
		return strconv.FormatFloat(float64(n)/1000, 'f', -1, 64), nil
	}
	n, err := strconv.ParseFloat(q, 64)
	if err != nil {
		return "", fmt.Errorf("invalid cpu limit %q; expected an integer or millicore quantity", q)
	}
	if math.IsInf(n, 0) || math.IsNaN(n) || n <= 0 {
		return "", errors.New("cpu limit must be greater than zero")
	}
	return strconv.FormatFloat(n, 'f', -1, 64), nil
}

// memoryLimit turns a Kubernetes quantity ("512Mi", "4G", "1048576") into
// bytes for wslc's --memory, as the Docker driver's parse_memory_limit does.
func memoryLimit(q string) (string, error) {
	q = strings.TrimSpace(q)
	end := strings.IndexFunc(q, func(r rune) bool { return !(r >= '0' && r <= '9' || r == '.') })
	if end < 0 {
		end = len(q)
	}
	number, suffix := q[:end], q[end:]
	n, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return "", fmt.Errorf("invalid memory limit %q; expected a Kubernetes-style quantity", q)
	}
	if math.IsInf(n, 0) || n <= 0 {
		return "", errors.New("memory limit must be greater than zero")
	}
	mult := map[string]float64{"": 1, "Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60,
		"K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18}
	m, ok := mult[suffix]
	if !ok {
		return "", fmt.Errorf("invalid memory limit suffix %q", suffix)
	}
	// Rust's `as i64` saturates; Go's conversion of a too large float is
	// undefined, so clamp first.
	bytes := math.Round(n * m)
	if bytes >= math.MaxInt64 {
		return strconv.FormatInt(math.MaxInt64, 10), nil
	}
	return strconv.FormatInt(int64(bytes), 10), nil
}

// CheckMountTarget normalizes a mount target as the driver does and rejects
// the default workspace (/sandbox), its parents and OpenShell's paths.
// Another image WORKDIR is checked when the sandbox is created.
func CheckMountTarget(t string) (string, error) {
	n, err := validTarget(t)
	if err != nil {
		return "", errors.New(status.Convert(err).Message())
	}
	if under(defaultWorkspace, n) {
		return "", fmt.Errorf("mount target %q is reserved for the OpenShell workspace", t)
	}
	if overlap(n, channelMount) || overlap(n, path.Dir(sandboxBinaryPath)) {
		return "", fmt.Errorf("mount target %q conflicts with the OpenShell channel", t)
	}
	return n, nil
}
