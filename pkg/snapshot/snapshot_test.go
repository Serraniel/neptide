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
	// Default: volumes only.
	if copyM, skipped := (&Snapshotter{}).Select(ct, []string{"/cache"}, map[string]string{"volume:shared": "app-cron"}); len(copyM) != 1 || copyM[0].Destination != "/data" || !strings.Contains(strings.Join(skipped, "\n"), "/config.yml: bind mount; set neptide.snapshot.binds=true") {
		t.Errorf("default selection: copy=%v skipped=%v", copyM, skipped)
	}
	s := &Snapshotter{Binds: true}
	copyM, skipped := s.Select(ct, []string{"/cache"}, map[string]string{"volume:shared": "app-cron"})
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

func TestUsedBy(t *testing.T) {
	inUse := map[string]string{"volume:db": "app-cron", "bind:/srv/app": "backup"}
	for m, want := range map[engine.Mount]string{
		{Type: "volume", Name: "db"}:            "app-cron",
		{Type: "volume", Name: "other"}:         "",
		{Type: "bind", Source: "/srv/app"}:      "backup",
		{Type: "bind", Source: "/srv/app/data"}: "backup", // child of a used path
		{Type: "bind", Source: "/srv"}:          "backup", // parent of a used path
		{Type: "bind", Source: "/srv/apple"}:    "",
	} {
		if got := usedBy(inUse, m); got != want {
			t.Errorf("usedBy(%+v) = %q, want %q", m, got, want)
		}
	}
}

func TestSystemPaths(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/etc": true, "/etc/ssl": true, "/proc/1": true, "/var/lib/docker": true,
		"/var/lib": true, "/tmp": true, "/tmp/x": true, "/var/log/app": true, "/home": true, "/srv": true, "/mnt": true,
		"/srv/app": false, "/home/me/data": false, "/var/lib/dockerish": false, "/opt/app": false, "/mnt/nas/photos": false,
	} {
		if got := isSystemPath(p, "/var/lib/docker"); got != want {
			t.Errorf("isSystemPath(%q) = %v, want %v", p, got, want)
		}
	}
}
