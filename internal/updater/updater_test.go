// SPDX-License-Identifier: AGPL-3.0-or-later

package updater

import (
	"slices"
	"testing"

	"github.com/Serraniel/neptide/pkg/engine"
)

func TestDependents(t *testing.T) {
	c := func(name, mode string) engine.ContainerSummary {
		s := engine.ContainerSummary{ID: name + "-id", Names: []string{"/" + name}}
		s.HostConfig.NetworkMode = mode
		return s
	}
	got := dependents([]engine.ContainerSummary{
		c("vpn", "bridge"), c("torrent", "container:vpn-id"), c("indexer", "container:vpn-id"),
		c("web", "app_default"), c("host", "host"),
	})
	var names []string
	for _, d := range got["vpn-id"] {
		names = append(names, d.Names[0])
	}
	if len(got) != 1 || !slices.Equal(names, []string{"/torrent", "/indexer"}) {
		t.Errorf("dependents = %v", got)
	}
}

func TestSkip(t *testing.T) {
	for _, c := range []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{"com.docker.compose.oneoff": "True", "neptide.enable": "true"}, true},
		{map[string]string{"com.docker.compose.oneoff": "False", "neptide.enable": "true"}, false},
		{map[string]string{"com.docker.swarm.service.id": "x"}, true},
		{map[string]string{"neptide.self": "true"}, true},
		{map[string]string{"neptide.enable": "true"}, false},
	} {
		if got := skip(c.labels); got != c.want {
			t.Errorf("skip(%v) = %v", c.labels, got)
		}
	}
}
