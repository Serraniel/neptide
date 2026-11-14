// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rollout updates Compose containers and Swarm services to a new
// image with a health gate, and rolls them back when the gate fails.
package rollout

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/version"
)

// Label keys read from containers and services (deploy.labels on swarm).
const (
	LabelEnable        = "neptide.enable"
	LabelTrack         = "neptide.track"
	LabelMode          = "neptide.mode"
	LabelHealthTimeout = "neptide.health-timeout"
	LabelSelf          = "neptide.self"
	// LabelSnapshot=true snapshots volumes before an update and restores
	// them on rollback; LabelSnapshotExclude lists mount paths to skip.
	LabelSnapshot        = "neptide.snapshot"
	LabelSnapshotExclude = "neptide.snapshot.exclude"
	LabelSnapshotBinds   = "neptide.snapshot.binds"
	// LabelVerifyIdentity, a regular expression for the signing certificate's
	// identity (for GitHub Actions, the workflow's URL), makes neptide update
	// only to digests with a valid Sigstore signature by it (cosign sign).
	LabelVerifyIdentity = "neptide.verify.identity"
	// LabelVerifyIssuer is the OIDC issuer of that identity; GitHub Actions'
	// by default.
	LabelVerifyIssuer = "neptide.verify.issuer"
)

// Mode is what happens when an update is found.
type Mode string

const (
	// ModeUpdate applies the update.
	ModeUpdate Mode = "update"
	// ModeNotify only reports it.
	ModeNotify Mode = "notify"
)

// Policy is the update policy of one container or service.
type Policy struct {
	Enabled       bool
	Track         version.Track
	Mode          Mode
	HealthTimeout time.Duration
	Snapshot      bool
	// Window limits when updates are applied; empty means any time.
	Window Windows
	// VerifyIdentity and VerifyIssuer require a signature (see
	// LabelVerifyIdentity); empty means none is checked.
	VerifyIdentity string
	VerifyIssuer   string
}

// PolicyFrom reads labels on top of defaults. Invalid labels are errors, not
// silently ignored: a typo in a policy must not cause an unwanted update.
func PolicyFrom(labels map[string]string, def Policy) (Policy, error) {
	p := def
	if v, ok := labels[LabelEnable]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return p, fmt.Errorf("%s=%q: %w", LabelEnable, v, err)
		}
		p.Enabled = b
	}
	if v, ok := labels[LabelTrack]; ok {
		t, err := version.ParseTrack(v)
		if err != nil {
			return p, err
		}
		p.Track = t
	}
	if v, ok := labels[LabelMode]; ok {
		switch Mode(v) {
		case ModeUpdate, ModeNotify:
			p.Mode = Mode(v)
		default:
			return p, fmt.Errorf("%s=%q: want update or notify", LabelMode, v)
		}
	}
	if v, ok := labels[LabelSnapshot]; ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return p, fmt.Errorf("%s=%q: %w", LabelSnapshot, v, err)
		}
		p.Snapshot = b
	}
	if v, ok := labels[LabelWindow]; ok {
		w, err := ParseWindows(v)
		if err != nil {
			return p, fmt.Errorf("%s: %w", LabelWindow, err)
		}
		p.Window = w // an empty label value lifts a default window
	}
	if v, ok := labels[LabelHealthTimeout]; ok {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return p, fmt.Errorf("%s=%q: want a positive duration like 2m", LabelHealthTimeout, v)
		}
		p.HealthTimeout = d
	}
	if v, ok := labels[LabelVerifyIdentity]; ok {
		// Anchored: an identity "matching" somewhere inside another one's URL
		// would accept a stranger's signature.
		if !strings.HasPrefix(v, "^") || !strings.HasSuffix(v, "$") {
			return p, fmt.Errorf("%s=%q: anchor the expression with ^ and $", LabelVerifyIdentity, v)
		}
		if _, err := regexp.Compile(v); err != nil {
			return p, fmt.Errorf("%s=%q: %w", LabelVerifyIdentity, v, err)
		}
		p.VerifyIdentity = v
	}
	if v, ok := labels[LabelVerifyIssuer]; ok {
		if !strings.HasPrefix(v, "https://") {
			return p, fmt.Errorf("%s=%q: an issuer is an https URL", LabelVerifyIssuer, v)
		}
		p.VerifyIssuer = v
	}
	return p, nil
}
