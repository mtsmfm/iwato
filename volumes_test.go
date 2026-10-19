package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// A new volume carries the sandbox and the path; WSLc splits a label at
// its first "=", so the path's slashes are kept as they are.
func TestVolumeLabels(t *testing.T) {
	e := &Engine{config: testConfig()}
	var created []string
	exists := false
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		switch a[1] {
		case "create":
			created = a
			exists = true
			return nil, nil
		case "inspect":
			if exists {
				return []byte(`[{"Name":"v","Driver":"guest","Labels":{"iwato.owner":"` + e.config.Instance + `"}}]`), nil
			}
			return []byte("[]"), exitStatus1(t)
		}
		return nil, errors.New("unexpected " + strings.Join(a, " "))
	}
	if err := e.volume(context.Background(), "v", map[string]string{sandboxKey: "dev", pathKey: "/sandbox/work"}); err != nil {
		t.Fatal(err)
	}
	want := "volume create --label iwato.owner=" + e.config.Instance + " --label iwato.path=/sandbox/work --label iwato.sandbox=dev v"
	if got := strings.Join(created, " "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func exitStatus1(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("no exit status")
	}
	return err
}

// The list is this instance's volumes (by label), inspected at once; one
// removed meanwhile (status 1) leaves the others, and another owner's is
// never shown.
func TestListVolumes(t *testing.T) {
	c := testConfig()
	e := &Engine{config: c}
	var calls []string
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		calls = append(calls, strings.Join(a, " "))
		if a[1] == "list" {
			return []byte("a\nb\nforeign\ngone\n"), nil
		}
		return []byte(`[
{"Name":"a","Driver":"guest","CreatedAt":"2026-10-01T02:03:04Z","Labels":{"iwato.owner":"` + c.Instance + `","iwato.sandbox":"dev","iwato.path":"/sandbox/work"}},
{"Name":"b","Driver":"guest","CreatedAt":"2026-09-01T00:00:00Z","Labels":{"iwato.owner":"` + c.Instance + `"}},
{"Name":"foreign","Driver":"guest","Labels":{"iwato.owner":"someone-else"}}]`), exitStatus1(t)
	}
	list, err := e.volumes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls[0] != "volume list --quiet --filter label=iwato.owner="+c.Instance || calls[1] != "volume inspect --format json a b foreign gone" {
		t.Fatal(calls)
	}
	if len(list) != 2 || list[0] != (Volume{Name: "a", Sandbox: "dev", Path: "/sandbox/work", CreatedAt: "2026-10-01T02:03:04Z"}) || list[1] != (Volume{Name: "b", CreatedAt: "2026-09-01T00:00:00Z"}) {
		t.Fatalf("%+v", list)
	}
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		if a[1] == "list" {
			return []byte("a\n"), nil
		}
		return nil, errors.New("wslc volume inspect: access denied")
	}
	if _, err := e.volumes(context.Background()); err == nil {
		t.Fatal("a failed inspect was taken as no volumes")
	}
	e.run = func(context.Context, ...string) ([]byte, error) { return nil, nil }
	if list, err := e.volumes(context.Background()); err != nil || list == nil || len(list) != 0 {
		t.Fatal(list, err)
	}
}

// Removing refuses a volume this instance does not own.
func TestRemoveVolumeChecksOwner(t *testing.T) {
	e := &Engine{config: testConfig()}
	removed := false
	e.run = func(_ context.Context, a ...string) ([]byte, error) {
		if a[1] == "remove" {
			removed = true
			return nil, nil
		}
		return []byte(`[{"Name":"x","Driver":"guest","Labels":{"iwato.owner":"other"}}]`), nil
	}
	if err := e.removeVolume(context.Background(), "x"); err == nil || removed {
		t.Fatal("removed another owner's volume", err)
	}
}

// describeVolumes tells who uses each volume, and recovers the sandbox from
// the name and the path from a configured one when the labels lack them.
func TestDescribeVolumes(t *testing.T) {
	c := testConfig()
	dev := Sandbox{Name: "dev", LegacyPersist: []string{"/sandbox/work", "/sandbox/data"}}
	sandboxes := []Sandbox{dev}
	old := Sandbox{Name: "old"}
	list := describeVolumes(c, sandboxes, []Volume{
		{Name: persistVolume(c, old, "/x"), Sandbox: "old", Path: "/x"},
		{Name: persistVolume(c, old, "/sandbox/work")},
		{Name: persistVolume(c, old, "/never")},
		{Name: persistVolume(c, dev, "/sandbox/data")},
		{Name: persistVolume(c, dev, "/sandbox/work"), Sandbox: "dev", Path: "/sandbox/work"},
		{Name: "made-by-hand"},
	})
	want := []Volume{
		{Name: "made-by-hand"},
		{Name: persistVolume(c, dev, "/sandbox/data"), Sandbox: "dev", Path: "/sandbox/data", UsedBy: "dev"},
		{Name: persistVolume(c, dev, "/sandbox/work"), Sandbox: "dev", Path: "/sandbox/work", UsedBy: "dev"},
		{Name: persistVolume(c, old, "/never"), Sandbox: "old"},
		{Name: persistVolume(c, old, "/sandbox/work"), Sandbox: "old", Path: "/sandbox/work"},
		{Name: persistVolume(c, old, "/x"), Sandbox: "old", Path: "/x"},
	}
	if len(list) != len(want) {
		t.Fatalf("%+v", list)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("%d: got %+v want %+v", i, list[i], want[i])
		}
	}
}
