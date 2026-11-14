// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"context"
	"strings"
	"testing"

	"github.com/Serraniel/neptide/pkg/engine"
)

// pullOnly is an engine that can inspect and pull; anything that would
// change the container panics, which fails the test.
type pullOnly struct {
	Engine
	pulled engine.Image
}

func (p *pullOnly) Inspect(context.Context, string) (*engine.Container, error) {
	return &engine.Container{ID: "c1", Name: "/app", Image: "sha256:old"}, nil
}

func (p *pullOnly) InspectImage(_ context.Context, ref string) (*engine.Image, error) {
	if ref == "sha256:old" {
		return &engine.Image{ID: "sha256:old", Config: map[string]any{}}, nil
	}
	return &p.pulled, nil
}

func (p *pullOnly) Pull(context.Context, string, *engine.RegistryAuth) error { return nil }

// A tag moved between the signature check and the pull: nothing changes.
func TestRecreateRefusesAnotherDigest(t *testing.T) {
	e := &pullOnly{pulled: engine.Image{ID: "sha256:new", RepoDigests: []string{"ghcr.io/acme/app@sha256:unchecked"}}}
	err := RecreateContainer(t.Context(), e, "c1", "ghcr.io/acme/app:1.2", Recreate{WantDigest: "sha256:checked"})
	if err == nil || !strings.Contains(err.Error(), "not the checked sha256:checked") {
		t.Fatalf("got %v", err)
	}
}
