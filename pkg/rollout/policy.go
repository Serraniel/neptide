// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rollout updates Compose containers and Swarm services to a new
// image with a health gate, and rolls them back when the gate fails.
package rollout

import (
	"fmt"
	"strconv"
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
	if v, ok := labels[LabelHealthTimeout]; ok {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return p, fmt.Errorf("%s=%q: want a positive duration like 2m", LabelHealthTimeout, v)
		}
		p.HealthTimeout = d
	}
	return p, nil
}
