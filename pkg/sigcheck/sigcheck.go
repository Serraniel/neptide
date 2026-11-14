// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sigcheck tells whether an image digest carries a valid Sigstore
// signature from an expected identity, as cosign sign stores it: a Sigstore
// bundle attached to the digest as an OCI referrer (the referrers API, or
// its tag fallback sha256-<hex>).
package sigcheck

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"

	"github.com/Serraniel/neptide/pkg/registry"
)

// GitHubActions is the OIDC issuer of GitHub Actions' signing identities.
const GitHubActions = "https://token.actions.githubusercontent.com"

// bundleType is the artifact type of a Sigstore bundle referrer.
const bundleType = "application/vnd.dev.sigstore.bundle.v0.3+json"

// cosignSign is the predicate type of what cosign sign makes (in-toto),
// as opposed to attestations such as an SBOM, which aren't signatures.
const cosignSign = "https://sigstore.dev/cosign/sign/v1"

// ErrUnsigned means no signature from the expected identity was found.
var ErrUnsigned = errors.New("no valid signature from the expected identity")

// Policy is who must have signed: a regular expression for the signing
// certificate's identity (for GitHub Actions, the workflow's URL), and the
// OIDC issuer that vouched for it.
type Policy struct {
	Identity string
	Issuer   string
}

// Registry is what the checker needs of a registry client.
type Registry interface {
	Manifest(ctx context.Context, r registry.Ref, accept string) ([]byte, error)
	Blob(ctx context.Context, r registry.Ref, digest string, limit int64) ([]byte, error)
}

// Checker verifies signatures against Sigstore's public trusted root, which
// it fetches over TUF the first time it is needed and refreshes daily.
type Checker struct {
	Registry Registry
	// Trusted, if set, replaces Sigstore's public trusted root (tests).
	Trusted root.TrustedMaterial

	once    sync.Once
	rootErr error
}

func (c *Checker) trusted() (root.TrustedMaterial, error) {
	c.once.Do(func() {
		if c.Trusted != nil {
			return
		}
		// neptide's file system is read-only: no TUF cache on disk.
		live, err := root.NewLiveTrustedRoot(tuf.DefaultOptions().WithDisableLocalCache())
		if err != nil {
			c.rootErr = fmt.Errorf("fetching Sigstore's trusted root: %w", err)
			return
		}
		c.Trusted = live
	})
	return c.Trusted, c.rootErr
}

type descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       string            `json:"digest"`
	ArtifactType string            `json:"artifactType"`
	Annotations  map[string]string `json:"annotations"`
}

type index struct {
	Manifests []descriptor `json:"manifests"`
}

type manifest struct {
	ArtifactType string       `json:"artifactType"`
	Layers       []descriptor `json:"layers"`
}

// referrers lists what refers to ref's digest: the OCI referrers API, else
// the tag fallback sha256-<hex> that registries without the API get.
func (c *Checker) referrers(ctx context.Context, ref registry.Ref) ([]descriptor, error) {
	const accept = "application/vnd.oci.image.index.v1+json"
	tag := strings.Replace(ref.Digest, ":", "-", 1)
	raw, err := c.Registry.Manifest(ctx, ref.WithDigest("").WithTag(tag), accept)
	if errors.Is(err, registry.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var idx index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("the referrers of %s: %w", ref.String(), err)
	}
	return idx.Manifests, nil
}

// Verify checks that ref (with a digest) has a signature by p's identity.
func (c *Checker) Verify(ctx context.Context, ref registry.Ref, p Policy) error {
	sum, ok := strings.CutPrefix(ref.Digest, "sha256:")
	if !ok {
		return fmt.Errorf("%s: a sha256 digest is needed to check its signature", ref.String())
	}
	digest, err := hex.DecodeString(sum)
	if err != nil {
		return fmt.Errorf("%s: %w", ref.String(), err)
	}
	issuer := p.Issuer
	if issuer == "" {
		issuer = GitHubActions
	}
	identity, err := verify.NewShortCertificateIdentity(issuer, "", "", p.Identity)
	if err != nil {
		return err
	}
	refs, err := c.referrers(ctx, ref)
	if err != nil {
		return err
	}
	var sigs []descriptor
	for _, d := range refs {
		if d.ArtifactType != bundleType {
			continue
		}
		if pt := d.Annotations["dev.sigstore.bundle.predicateType"]; pt != "" && pt != cosignSign {
			continue // an attestation (SBOM, provenance), not a signature
		}
		sigs = append(sigs, d)
	}
	if len(sigs) == 0 {
		return fmt.Errorf("%s: %w: it has no Sigstore signature", ref.String(), ErrUnsigned)
	}
	trusted, err := c.trusted()
	if err != nil {
		return err
	}
	v, err := verify.NewVerifier(trusted, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	var why []string
	for _, d := range sigs {
		b, err := c.bundle(ctx, ref, d.Digest)
		if err != nil {
			why = append(why, err.Error())
			continue
		}
		pb := verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(identity))
		if _, err := v.Verify(b, pb); err != nil {
			why = append(why, err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("%s: %w (%s)", ref.String(), ErrUnsigned, strings.Join(why, "; "))
}

// bundle reads the Sigstore bundle a referrer manifest carries as its layer.
func (c *Checker) bundle(ctx context.Context, ref registry.Ref, digest string) (*bundle.Bundle, error) {
	raw, err := c.Registry.Manifest(ctx, ref.WithDigest(digest), "application/vnd.oci.image.manifest.v1+json")
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if len(m.Layers) != 1 {
		return nil, fmt.Errorf("signature %s: %d layers, not one bundle", digest, len(m.Layers))
	}
	data, err := c.Registry.Blob(ctx, ref, m.Layers[0].Digest, 1<<20)
	if err != nil {
		return nil, err
	}
	var b bundle.Bundle
	if err := b.UnmarshalJSON(data); err != nil {
		return nil, fmt.Errorf("signature %s: %w", digest, err)
	}
	return &b, nil
}
