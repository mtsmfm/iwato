package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"iwato/internal/dav"
	"iwato/internal/openshell/client"
	"iwato/internal/termd"
)

// The pool takes OpenShell's packet size from the transport.
var _ dav.ClientOptioner = client.SFTP{}

// DialFiles opens the sandbox's files: the daemon's SFTP server, rooted at
// the home, when its daemon has one (the image convention); otherwise
// OpenShell's, rooted at the workspace, through the sandbox's SSH
// connection (the same relay as the main terminal). A sandbox that does
// not run is dav.ErrStopped.
func (o *OpenShell) DialFiles(ctx context.Context, w Sandbox) (dav.Transport, error) {
	if o.Status(ctx, w).State != "running" {
		return nil, dav.ErrStopped
	}
	sdk, conn, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	d, err := o.daemon(ctx, w)
	if err != nil {
		return nil, err
	}
	if d.has(termd.CapSFTP) {
		return &client.DaemonSFTP{Conn: conn, Workspace: openshellWorkspace, Sandbox: w.Name, Bin: d.bin, Root: homeDir}, nil
	}
	// The relay lives as long as the transport (the pool closes it), not
	// the request that opened it.
	c, err := client.DialSSH(context.WithoutCancel(ctx), sdk, openshellWorkspace, w.Name)
	if err != nil {
		if o.Status(ctx, w).State != "running" {
			return nil, dav.ErrStopped
		}
		return nil, err
	}
	return client.SFTP{Client: c}, nil
}

// workdirScript prints the workspace's absolute path once the workload can
// read $1 under it. An exec without a workdir starts in the sandbox's
// workdir, which is also SFTP's root (both take OpenShell's base workdir);
// SFTP itself cannot tell it (its realpath answers "/" for the root).
const workdirScript = `cd -P . && test -r "$1" && pwd`

// FilesRoot is where DialFiles's files are in the sandbox: the home with
// the daemon's SFTP server, else the workspace (OpenShell's SFTP root).
func (o *OpenShell) FilesRoot(ctx context.Context, w Sandbox) (FilesRoot, error) {
	if s := o.Status(ctx, w).State; s != "running" {
		return FilesRoot{}, dav.ErrStopped
	}
	home, err := o.filesAtHome(ctx, w)
	if err != nil {
		return FilesRoot{}, err
	}
	if home {
		return FilesRoot{Path: homeDir, Home: true}, nil
	}
	dir, err := o.Workdir(ctx, w, ".")
	if err != nil {
		return FilesRoot{}, err
	}
	return FilesRoot{Path: dir, Home: dir == homeDir}, nil
}

// filesAtHome tells whether the sandbox's files are its daemon's (rooted
// at the home).
func (o *OpenShell) filesAtHome(ctx context.Context, w Sandbox) (bool, error) {
	d, err := o.daemon(ctx, w)
	return d.has(termd.CapSFTP), err
}

// Workdir is the absolute path of the sandbox's workspace (OpenShell's
// SFTP root), as the workload sees it, after checking that the workload
// can read rel there.
func (o *OpenShell) Workdir(ctx context.Context, w Sandbox, rel string) (string, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return "", err
	}
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, []string{"/bin/sh", "-c", workdirScript, "iwato-workdir", rel}, client.Options{})
	if err != nil {
		return "", err
	}
	defer p.Close()
	p.CloseWrite()
	out, _ := io.ReadAll(p)
	code, err := p.Wait()
	dir := strings.TrimSpace(string(out))
	if err == nil && code != 0 {
		err = fmt.Errorf("the sandbox does not see %s (exit %d): %s", rel, code, dir)
	}
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(dir, "/") || strings.ContainsAny(dir, "\n\x00") {
		return "", fmt.Errorf("the sandbox's workdir: %q", dir)
	}
	return dir, nil
}

// listFilesScript lists the files under $2 (relative to $1, the files'
// root: the home, or "." for the workspace, the exec's directory) with
// ripgrep, NUL-separated: what .gitignore leaves, hidden files too, not the
// globs after $2 (-g). Exit 127: no rg; 3: no such folder.
const listFilesScript = `cd -- "$1" 2>/dev/null && cd -- "$2" 2>/dev/null || exit 3
shift 2
command -v rg >/dev/null 2>&1 || exit 127
exec rg --files --hidden --no-messages --no-config -0 "$@" 2>/dev/null`

// ListFiles lists the files under dir with ripgrep as the sandbox user, at
// most maxEditorFiles of them (and maxEditorList bytes of output).
func (o *OpenShell) ListFiles(ctx context.Context, w Sandbox, dir string) ([]string, bool, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return nil, false, err
	}
	if conn == nil { // a test's fake sandbox API
		return nil, false, errNoRipgrep
	}
	root := "."
	if home, err := o.filesAtHome(ctx, w); err != nil {
		return nil, false, err
	} else if home {
		root = homeDir
	}
	p, err := client.Start(ctx, conn, openshellWorkspace, w.Name, append([]string{"/bin/sh", "-c", listFilesScript, "iwato-list-files", root, dir}, editorSkipGlobs()...), client.Options{})
	if err != nil {
		return nil, false, err
	}
	defer p.Close()
	p.CloseWrite()
	out, err := io.ReadAll(io.LimitReader(p, maxEditorList))
	if err != nil && ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	truncated := len(out) >= maxEditorList
	if truncated {
		// The last name may be cut: drop it, and stop rg.
		if i := strings.LastIndexByte(string(out), 0); i >= 0 {
			out = out[:i+1]
		}
		p.Close()
	} else {
		code, err := p.Wait()
		switch {
		case err != nil:
			return nil, false, err
		case code == 127:
			return nil, false, errNoRipgrep
		case code == 3:
			return nil, false, errEditorNoDir
		case code != 0 && code != 1 && code != 2: // 1: no files, 2: some unreadable
			return nil, false, fmt.Errorf("rg exited %d", code)
		}
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" {
			continue
		}
		if len(files) >= maxEditorFiles {
			truncated = true
			break
		}
		files = append(files, strings.TrimPrefix(f, "./"))
	}
	return files, truncated, nil
}
