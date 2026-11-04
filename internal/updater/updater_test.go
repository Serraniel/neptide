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
