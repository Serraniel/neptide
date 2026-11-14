// SPDX-License-Identifier: AGPL-3.0-or-later

package sigcheck

import (
	"errors"
	"os"
	"testing"

	"github.com/Serraniel/neptide/pkg/registry"
)

// Against a real signed image and Sigstore's public good instance: run with
// NEPTIDE_TEST_NETWORK=1.
func TestVerifyLive(t *testing.T) {
	if os.Getenv("NEPTIDE_TEST_NETWORK") == "" {
		t.Skip("set NEPTIDE_TEST_NETWORK to verify against ghcr.io and Sigstore")
	}
	reg := registry.New()
	ref, err := registry.ParseRef("ghcr.io/serraniel/nepfleet:0.3.2")
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Digest(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	c := &Checker{Registry: reg}
	good := Policy{Identity: `^https://github\.com/Serraniel/nepfleet/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9.]+)$`}
	if err := c.Verify(t.Context(), ref.WithDigest(d), good); err != nil {
		t.Fatalf("the release's signature: %v", err)
	}
	other := Policy{Identity: `^https://github\.com/someone-else/`}
	if err := c.Verify(t.Context(), ref.WithDigest(d), other); !errors.Is(err, ErrUnsigned) {
		t.Errorf("another identity: %v", err)
	}
	// A digest nobody signed: the tag points at the index; its config isn't signed.
	unsigned := ref.WithDigest("sha256:" + "00000000000000000000000000000000000000000000000000000000000000ab")
	if err := c.Verify(t.Context(), unsigned, good); !errors.Is(err, ErrUnsigned) {
		t.Errorf("unsigned digest: %v", err)
	}
}
