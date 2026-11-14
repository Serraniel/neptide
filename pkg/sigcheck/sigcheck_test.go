// SPDX-License-Identifier: AGPL-3.0-or-later

package sigcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/Serraniel/neptide/pkg/registry"
)

// fakeRegistry serves manifests by tag or digest, and blobs.
type fakeRegistry struct {
	manifests map[string]string
	blobs     map[string]string
}

func (f fakeRegistry) Manifest(_ context.Context, r registry.Ref, _ string) ([]byte, error) {
	key := r.Digest
	if key == "" {
		key = r.Tag
	}
	if m, ok := f.manifests[key]; ok {
		return []byte(m), nil
	}
	return nil, registry.ErrNotFound
}

func (f fakeRegistry) Blob(_ context.Context, _ registry.Ref, digest string, _ int64) ([]byte, error) {
	if b, ok := f.blobs[digest]; ok {
		return []byte(b), nil
	}
	return nil, registry.ErrNotFound
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestVerifyOffline(t *testing.T) {
	image := digestOf("image")
	tag := strings.Replace(image, ":", "-", 1)
	ref, _ := registry.ParseRef("ghcr.io/acme/app:1.0")
	ref = ref.WithDigest(image)
	p := Policy{Identity: `^https://github\.com/acme/app/`}
	// Trusted material that never fetches: these cases fail before using it.
	c := func(f fakeRegistry) *Checker { return &Checker{Registry: f, Trusted: &root.TrustedRoot{}} }

	if err := c(fakeRegistry{}).Verify(t.Context(), ref, p); !errors.Is(err, ErrUnsigned) {
		t.Errorf("no referrers: %v", err)
	}
	sbomOnly := fakeRegistry{manifests: map[string]string{tag: `{"manifests":[{"digest":"sha256:aa","artifactType":"application/vnd.dev.sigstore.bundle.v0.3+json","annotations":{"dev.sigstore.bundle.predicateType":"https://cyclonedx.org/bom"}}]}`}}
	if err := c(sbomOnly).Verify(t.Context(), ref, p); !errors.Is(err, ErrUnsigned) || !strings.Contains(err.Error(), "no Sigstore signature") {
		t.Errorf("an SBOM attestation is no signature: %v", err)
	}
	bad := digestOf("not a bundle")
	broken := fakeRegistry{
		manifests: map[string]string{
			tag:          `{"manifests":[{"digest":"sha256:sig","artifactType":"application/vnd.dev.sigstore.bundle.v0.3+json","annotations":{"dev.sigstore.bundle.predicateType":"https://sigstore.dev/cosign/sign/v1"}}]}`,
			"sha256:sig": `{"layers":[{"digest":"` + bad + `"}]}`,
		},
		blobs: map[string]string{bad: "not a bundle"},
	}
	if err := c(broken).Verify(t.Context(), ref, p); !errors.Is(err, ErrUnsigned) {
		t.Errorf("a broken signature: %v", err)
	}
	if err := c(fakeRegistry{}).Verify(t.Context(), ref.WithDigest(""), p); err == nil {
		t.Error("no digest")
	}
}
