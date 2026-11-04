// SPDX-License-Identifier: AGPL-3.0-or-later

package snapshot

import (
	"strings"
	"testing"

	"github.com/Serraniel/neptide/pkg/engine"
)

func TestSelect(t *testing.T) {
	ct := &engine.Container{Name: "/app", Mounts: []engine.Mount{
		{Type: "volume", Name: "app_data", Destination: "/data", RW: true},
		{Type: "bind", Source: "/srv/app/config.yml", Destination: "/config.yml", RW: true},
		{Type: "bind", Source: "/etc", Destination: "/host-etc", RW: true},
		{Type: "bind", Source: "/", Destination: "/rootfs", RW: false},
		{Type: "bind", Source: "/srv/media", Destination: "/media", RW: false},
		{Type: "bind", Source: "/var/lib/docker/volumes", Destination: "/v", RW: true},
		{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true},
		{Type: "volume", Name: "shared", Destination: "/shared", RW: true},
		{Type: "volume", Name: "cache", Destination: "/cache", RW: true},
		{Type: "tmpfs", Destination: "/tmp", RW: true},
	}}
	s := &Snapshotter{}
	copyM, skipped := s.Select(ct, []string{"/cache"}, map[string]string{"shared": "app-cron"})
	var dests []string
	for _, m := range copyM {
		dests = append(dests, m.Destination)
	}
	if strings.Join(dests, " ") != "/data /config.yml" {
		t.Errorf("copied %v", dests)
	}
	all := strings.Join(skipped, "\n")
	for _, want := range []string{"/host-etc: system path", "/rootfs: read-only", "/media: read-only", "/v: system path", "/var/run/docker.sock: system path", "/shared: also used by running container app-cron", "/cache: excluded"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing skip %q in:\n%s", want, all)
		}
	}
}

func TestSystemPaths(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/etc": true, "/etc/ssl": true, "/proc/1": true, "/var/lib/docker": true,
		"/var/lib": true, // contains the docker root
		"/srv/app": false, "/home/me/data": false, "/var/lib/dockerish": false, "/opt/app": false,
	} {
		if got := isSystemPath(p, "/var/lib/docker"); got != want {
			t.Errorf("isSystemPath(%q) = %v, want %v", p, got, want)
		}
	}
}
