package wslc

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// A named volume starts as an empty directory owned by root, and the
// capability-free launch hands only the workspace to the sandbox user. A
// volume mounted at a path the image does not have (a persisted /sandbox/work
// or ~/.claude) would therefore stay read-only for the sandbox user. The
// Docker driver leaves this to Docker's copy-up from the image, which cannot
// help for such a path; Kubernetes sets fsGroup. Here the root of each
// writable volume that root still owns is handed to the sandbox user after the
// workload starts and before the supervisor starts the main process (the
// workload runs nothing of the user's until then).

// writableVolumeTargets returns the normalized targets of the volume mounts
// that are not read-only.
func writableVolumeTargets(cfg driverConfig) []string {
	var targets []string
	for _, m := range cfg.Mounts {
		if m.Type != "volume" || m.ReadOnly == nil || *m.ReadOnly {
			continue
		}
		if t, err := validTarget(m.Target); err == nil {
			targets = append(targets, t)
		}
	}
	return targets
}

// ownVolumesScript chowns each target (arguments after the uid and gid) when
// it is a real directory that root owns. The mount point itself only, not its
// contents: what the sandbox user or an earlier owner put there stays as it
// is, and a directory the user has since given away or locked is not touched.
const ownVolumesScript = `u=$1 g=$2; shift 2
for d in "$@"; do
  [ -d "$d" ] && [ ! -L "$d" ] && [ "$(stat -c %u "$d")" = 0 ] && chown "$u:$g" "$d"
done
exit 0`

// ownVolumesCommand is the exec for the workload container.
func ownVolumesCommand(uid, gid string, targets []string) []string {
	return append([]string{"/bin/sh", "-c", ownVolumesScript, "own-volumes", uid, gid}, targets...)
}

// ownVolumes runs after the workload started. A failure (an image without
// stat or chown, a state from before this existed) is logged, not fatal: the
// sandbox still works, only the persisted path may not be writable.
func (d *driver) ownVolumes(ctx context.Context, sandboxID, cid string) {
	st, err := d.readState(sandboxID, "identity")
	if err != nil {
		return
	}
	targets := d.volumeTargets(sandboxID)
	if len(targets) == 0 {
		return
	}
	uid, gid, ok := strings.Cut(strings.TrimSpace(string(st["identity"])), ":")
	if !ok {
		return
	}
	if err := d.w.exec(ctx, cid, ownVolumesCommand(uid, gid, targets)...); err != nil {
		msg := fmt.Sprintf("could not hand the persisted paths to the sandbox user: %v", err)
		log.Printf("sandbox %s: %s", sandboxID, msg)
		d.platformEvent(sandboxID, "Warning", "VolumeOwnership", msg)
	}
}

// volumeTargets reads the writable volume targets saved at creation. Absent
// for a sandbox created before they were saved.
func (d *driver) volumeTargets(sandboxID string) []string {
	st, err := d.readState(sandboxID, "volume-targets")
	if err != nil {
		return nil
	}
	return strings.Fields(string(st["volume-targets"]))
}
