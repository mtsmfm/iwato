package wslc

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// resolveIdentity ports the Docker driver's
// resolve_docker_identity_from_accounts: the requested user (else the
// image's USER) and group (else the image's USER group, else the user's
// primary group), looked up in the image's passwd and group. No user at all
// means the synthetic 1000:1000. Supplementary groups are always empty:
// launch-capability-free clears them (setgroups(0)) and the sandbox requires
// its actual groups to equal the identity's.
func resolveIdentity(reqUser, reqGroup, imageUser, imageID string, passwdBytes, groupBytes []byte) (identity, error) {
	reqUser, reqGroup = strings.TrimSpace(reqUser), strings.TrimSpace(reqGroup)
	imageName, imageGroup, _ := strings.Cut(imageUser, ":")
	userSel := reqUser
	if userSel == "" {
		userSel = strings.TrimSpace(imageName)
	}
	if userSel == "" {
		// No USER in the image and none requested.
		return identity{UID: 1000, GID: 1000, SupplementaryGIDs: []uint32{}, Source: "default", ResourceDigest: imageID}, nil
	}
	passwd, err := parsePasswd(passwdBytes)
	if err != nil {
		return identity{}, err
	}
	groups, err := parseGroup(groupBytes)
	if err != nil {
		return identity{}, err
	}
	var uid uint32
	var entry *passwdEntry
	if n, err := strconv.ParseUint(userSel, 10, 32); err == nil {
		uid = uint32(n)
		for i := range passwd {
			if passwd[i].uid == uid {
				entry = &passwd[i]
				break
			}
		}
	} else {
		for i := range passwd {
			if passwd[i].name == userSel {
				entry = &passwd[i]
				break
			}
		}
		if entry == nil {
			return identity{}, fmt.Errorf("workload user %q does not exist in the pinned image", userSel)
		}
		uid = entry.uid
	}
	groupSel := reqGroup
	if groupSel == "" {
		groupSel = strings.TrimSpace(imageGroup)
	}
	var gid uint32
	switch {
	case groupSel == "" && entry == nil:
		return identity{}, fmt.Errorf("numeric workload UID %d has no passwd entry; configure process.run_as_group", uid)
	case groupSel == "":
		gid = entry.gid
	default:
		if n, err := strconv.ParseUint(groupSel, 10, 32); err == nil {
			gid = uint32(n)
		} else {
			found := false
			for _, g := range groups {
				if g.name == groupSel {
					gid, found = g.gid, true
					break
				}
			}
			if !found {
				return identity{}, fmt.Errorf("workload group %q does not exist in the pinned image", groupSel)
			}
		}
	}
	if uid == 0 || gid == 0 {
		return identity{}, errors.New("workload identity must not contain UID or GID zero")
	}
	source := "image"
	if reqUser != "" || reqGroup != "" {
		source = "policy"
	}
	return identity{UID: uid, GID: gid, SupplementaryGIDs: []uint32{}, Source: source, ResourceDigest: imageID}, nil
}

type passwdEntry struct {
	name     string
	uid, gid uint32
}

type groupEntry struct {
	name string
	gid  uint32
}

// accountLines yields the meaningful lines of passwd or group with their
// numbers, failing on non-UTF-8 content (parse_docker_passwd/group).
func accountLines(b []byte, file string) ([][]string, []int, error) {
	if !utf8.Valid(b) {
		return nil, nil, fmt.Errorf("image %s is not UTF-8", file)
	}
	var lines [][]string
	var numbers []int
	for i, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) < 4 || f[0] == "" {
			return nil, nil, fmt.Errorf("image %s line %d is malformed", file, i+1)
		}
		lines, numbers = append(lines, f), append(numbers, i+1)
	}
	return lines, numbers, nil
}

func parsePasswd(b []byte) ([]passwdEntry, error) {
	lines, numbers, err := accountLines(b, "/etc/passwd")
	if err != nil {
		return nil, err
	}
	out := make([]passwdEntry, 0, len(lines))
	for i, f := range lines {
		uid, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("image /etc/passwd line %d has an invalid UID", numbers[i])
		}
		gid, err := strconv.ParseUint(f[3], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("image /etc/passwd line %d has an invalid GID", numbers[i])
		}
		out = append(out, passwdEntry{name: f[0], uid: uint32(uid), gid: uint32(gid)})
	}
	return out, nil
}

func parseGroup(b []byte) ([]groupEntry, error) {
	lines, numbers, err := accountLines(b, "/etc/group")
	if err != nil {
		return nil, err
	}
	out := make([]groupEntry, 0, len(lines))
	for i, f := range lines {
		gid, err := strconv.ParseUint(f[2], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("image /etc/group line %d has an invalid GID", numbers[i])
		}
		out = append(out, groupEntry{name: f[0], gid: uint32(gid)})
	}
	return out, nil
}

// tarEntry is one file or directory of an archive copied into a container;
// the archive carries ownership and modes.
type tarEntry struct {
	name     string // relative to the extraction directory
	dir      bool
	mode     int64
	uid, gid int
	data     []byte
}

func buildTar(entries []tarEntry) ([]byte, error) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	now := time.Now()
	for _, e := range entries {
		h := &tar.Header{Name: path.Clean(e.name), Mode: e.mode, Uid: e.uid, Gid: e.gid, ModTime: now, Format: tar.FormatPAX}
		if e.dir {
			h.Typeflag, h.Name = tar.TypeDir, h.Name+"/"
		} else {
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.data))
		}
		if err := w.WriteHeader(h); err != nil {
			return nil, err
		}
		if !e.dir {
			if _, err := w.Write(e.data); err != nil {
				return nil, err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
