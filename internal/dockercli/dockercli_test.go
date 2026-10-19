package dockercli

import (
	"strings"
	"testing"
)

func TestArgsAndSocket(t *testing.T) {
	for in, want := range map[string]string{
		"volume inspect --format json a b": "volume inspect a b",
		"container create --format json x": "container create --format json x",
		"image inspect img":                "image inspect img",
	} {
		if got := strings.Join(Args(strings.Fields(in)), " "); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	for in, want := range map[string]string{
		"unix:///run/user/1000/docker.sock": "/run/user/1000/docker.sock",
		"tcp://127.0.0.1:2375":              DefaultSocket,
		"":                                  DefaultSocket,
	} {
		if got := socketPath(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
