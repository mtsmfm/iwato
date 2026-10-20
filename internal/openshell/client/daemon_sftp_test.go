package client

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"

	"iwato/internal/rootsftp"
)

// A file larger than one packet reads whole through a large buffer (the
// WebDAV GET's), which pkg/sftp splits into concurrent requests.
func TestDaemonSFTPReadsPastOnePacket(t *testing.T) {
	dir := t.TempDir()
	want := make([]byte, 100000)
	rand.Read(want)
	if err := os.WriteFile(filepath.Join(dir, "f"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	go rootsftp.Serve(dir, toServer, fromServer)
	c, err := sftp.NewClientPipe(toClient, fromClient, (&DaemonSFTP{}).ClientOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	f, err := c.Open("f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := io.ReadAll(bufio.NewReaderSize(f, 1<<20))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %d of %d bytes: %v", len(got), len(want), err)
	}
}
