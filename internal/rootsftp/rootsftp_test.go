package rootsftp

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
)

// client serves dir and returns a client of it.
func client(t *testing.T, dir string) *sftp.Client {
	t.Helper()
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(dir, toServer, fromServer) }()
	c, err := sftp.NewClientPipe(toClient, fromClient)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		if err := <-done; err != nil {
			t.Error("serve:", err)
		}
	})
	return c
}

// Nothing outside the root is reached: not through `..`, not through a
// link (absolute or relative, as the last component or a folder on the
// way), not by renaming or linking there. What is inside works, links
// included.
func TestConfinedToTheRoot(t *testing.T) {
	base := t.TempDir()
	home, outside := filepath.Join(base, "home"), filepath.Join(base, "outside")
	for _, d := range []string{home, outside, filepath.Join(home, "work")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "work", "a.txt"), []byte("yes"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"abs": outside, "rel": "../outside", "secret": filepath.Join(outside, "secret"),
		"inside": "work/a.txt", "workdir": "work",
	} {
		if err := os.Symlink(target, filepath.Join(home, link)); err != nil {
			t.Fatal(err)
		}
	}
	c := client(t, home)

	read := func(p string) (string, error) {
		f, err := c.Open(p)
		if err != nil {
			return "", err
		}
		defer f.Close()
		b, err := io.ReadAll(f)
		return string(b), err
	}
	for _, p := range []string{"/work/a.txt", "work/a.txt", "/inside", "/workdir/a.txt", "/work/../work/a.txt"} {
		if got, err := read(p); err != nil || got != "yes" {
			t.Errorf("read %s: %q %v", p, got, err)
		}
	}
	if fi, err := c.Stat("/"); err != nil || !fi.IsDir() {
		t.Errorf("stat /: %v %v", fi, err)
	}
	for _, p := range []string{"/../outside/secret", "../outside/secret", "/secret", "/abs/secret", "/rel/secret"} {
		if got, err := read(p); err == nil {
			t.Errorf("read %s outside the root: %q", p, got)
		}
		if _, err := c.Stat(p); err == nil {
			t.Errorf("stat %s outside the root succeeded", p)
		}
	}
	if _, err := c.ReadDir("/abs"); err == nil {
		t.Error("listed a folder outside the root")
	}
	for _, p := range []string{"/abs/new", "/rel/new", "/../outside/new"} {
		if f, err := c.Create(p); err == nil {
			f.Close()
			t.Errorf("created %s", p)
		}
		if err := c.Mkdir(p + "dir"); err == nil {
			t.Errorf("made the folder %sdir", p)
		}
	}
	if err := c.Rename("/work/a.txt", "/abs/moved"); err == nil {
		t.Error("renamed into a folder outside the root")
	}
	if err := c.Link("/abs/secret", "/hard"); err == nil {
		t.Error("hard-linked a file outside the root")
	}
	if err := c.Remove("/abs/secret"); err == nil {
		t.Error("removed a file outside the root")
	}
	if err := c.Chmod("/secret", 0o600); err == nil {
		t.Error("changed the mode through a link outside the root")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the outside folder changed: %v %v", entries, err)
	}
	if fi, _ := os.Stat(filepath.Join(outside, "secret")); fi.Mode().Perm() != 0o644 {
		t.Error("the outside file's mode changed")
	}

	// The links themselves are seen as links.
	if fi, err := c.Lstat("/abs"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("lstat /abs: %v %v", fi, err)
	}
	if got, err := c.ReadLink("/rel"); err != nil || got != "../outside" {
		t.Errorf("readlink /rel: %q %v", got, err)
	}
	// Writing, replacing and removing inside.
	f, err := c.OpenFile("/work/b.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := c.Rename("/work/b.txt", "/work/a.txt"); err == nil {
		t.Error("rename replaced an existing file (SFTP v3 refuses)")
	}
	if err := c.PosixRename("/work/b.txt", "/work/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got, _ := read("/work/a.txt"); got != "new" {
		t.Errorf("after posix-rename: %q", got)
	}
	if err := c.Remove("/work"); err == nil {
		t.Error("removed a folder that is not empty")
	}
	if err := c.Remove("/work/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove("/work"); err != nil {
		t.Fatal("remove an empty folder:", err)
	}
}
