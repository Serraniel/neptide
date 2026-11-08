// SPDX-License-Identifier: AGPL-3.0-or-later

package snapshot

import (
	"context"
	"errors"
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

// listEngine lists containers, some of which are gone by the time they're inspected.
type listEngine struct {
	Engine
	list []engine.ContainerSummary
	byID map[string]*engine.Container
	fail error
}

func (e listEngine) Containers(context.Context, ...string) ([]engine.ContainerSummary, error) {
	return e.list, nil
}

func (e listEngine) Inspect(_ context.Context, id string) (*engine.Container, error) {
	if c, ok := e.byID[id]; ok {
		return c, nil
	}
	if e.fail != nil {
		return nil, e.fail
	}
	return nil, engine.NotFoundError{What: "GET /containers/" + id + "/json"}
}

func TestInUseSkipsRemovedContainers(t *testing.T) {
	cron := &engine.Container{ID: "cron", Name: "/app-cron", Mounts: []engine.Mount{{Type: "volume", Name: "db"}}}
	e := listEngine{list: []engine.ContainerSummary{{ID: "self"}, {ID: "cron"}, {ID: "gone"}}, byID: map[string]*engine.Container{"cron": cron}}
	got, err := (&Snapshotter{Engine: e}).inUse(context.Background(), "self")
	if err != nil {
		t.Fatalf("a container removed meanwhile uses nothing: %v", err)
	}
	if len(got) != 1 || got["volume:db"] != "app-cron" {
		t.Errorf("%v", got)
	}
	e.fail = errors.New("daemon hiccup")
	if _, err := (&Snapshotter{Engine: e}).inUse(context.Background(), "self"); err == nil {
		t.Error("any other error is still unsafe to guess past")
	}
}
