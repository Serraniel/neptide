// SPDX-License-Identifier: AGPL-3.0-or-later

// Package snapshot copies a stopped container's volumes and bind mounts
// into snapshot volumes and restores them, so a rollback brings back the
// data that belongs to the old version (no half-migrated databases).
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
)

// Labels on snapshot volumes.
const (
	LabelOf    = "neptide.snapshot.of"
	LabelMount = "neptide.snapshot.mount"
	LabelTime  = "neptide.snapshot.time"
)

// HelperImage copies the data; pinned by digest, as everything that touches data.
const HelperImage = "alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63"

// Engine is what snapshots need from Docker.
type Engine interface {
	Pull(ctx context.Context, ref string, auth *engine.RegistryAuth) error
	Create(ctx context.Context, name string, config, hostConfig map[string]any, networks map[string]map[string]any) (string, error)
	Start(ctx context.Context, id string) error
	Wait(ctx context.Context, id string) (int, error)
	Remove(ctx context.Context, id string) error
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	Volumes(ctx context.Context, labels ...string) ([]engine.Volume, error)
	RemoveVolume(ctx context.Context, name string) error
}

// Snapshotter takes and restores snapshots.
type Snapshotter struct {
	Engine Engine
	// Keep is how many snapshots per container survive pruning (default 3).
	Keep int
	// Now is the clock (tests).
	Now func() time.Time
}

// Item is one copied mount.
type Item struct {
	Mount  engine.Mount
	Volume string
}

// Snapshot is a set of copied mounts of one container.
type Snapshot struct {
	Container string
	Items     []Item
}

var unsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// Take copies every volume and bind mount of ct (which must be stopped)
// except destinations in exclude. On error, partial copies are removed.
func (s *Snapshotter) Take(ctx context.Context, ct *engine.Container, exclude []string) (*Snapshot, error) {
	name := strings.TrimPrefix(ct.Name, "/")
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ts := now().UTC().Format("20060102T150405.000Z")
	snap := &Snapshot{Container: name}
	if err := s.Engine.Pull(ctx, HelperImage, nil); err != nil {
		return nil, fmt.Errorf("snapshot helper image: %w", err)
	}
	for i, m := range ct.Mounts {
		if (m.Type != "volume" && m.Type != "bind") || slices.Contains(exclude, m.Destination) {
			continue
		}
		vol := fmt.Sprintf("neptide-snap-%s-%s-%d", unsafe.ReplaceAllString(name, "_"), ts, i)
		labels := map[string]string{LabelOf: name, LabelMount: m.Destination, LabelTime: ts}
		if err := s.Engine.CreateVolume(ctx, vol, labels); err != nil {
			s.discard(ctx, snap)
			return nil, err
		}
		snap.Items = append(snap.Items, Item{Mount: m, Volume: vol})
		if err := s.run(ctx, []string{source(m) + ":/src:ro", vol + ":/dst"}, "cp -a /src/. /dst/"); err != nil {
			s.discard(ctx, snap)
			return nil, fmt.Errorf("snapshot of %s: %w", m.Destination, err)
		}
	}
	s.prune(ctx, name)
	return snap, nil
}

// Restore puts the snapshot's data back into the original mounts, replacing
// what is there now. The container using them must be stopped.
func (s *Snapshotter) Restore(ctx context.Context, snap *Snapshot) error {
	var errs []error
	for _, it := range snap.Items {
		script := "find /dst -mindepth 1 -maxdepth 1 -exec rm -rf {} + && cp -a /src/. /dst/"
		if err := s.run(ctx, []string{it.Volume + ":/src:ro", source(it.Mount) + ":/dst"}, script); err != nil {
			errs = append(errs, fmt.Errorf("restore %s from %s: %w", it.Mount.Destination, it.Volume, err))
		}
	}
	return errors.Join(errs...)
}

func source(m engine.Mount) string {
	if m.Type == "volume" {
		return m.Name
	}
	return m.Source
}

// run executes script in a throwaway helper container with binds.
func (s *Snapshotter) run(ctx context.Context, binds []string, script string) error {
	id, err := s.Engine.Create(ctx, "", map[string]any{
		"Image":  HelperImage,
		"Cmd":    []string{"sh", "-c", script},
		"Labels": map[string]string{"neptide.helper": "snapshot"},
	}, map[string]any{"Binds": binds, "NetworkMode": "none"}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = s.Engine.Remove(context.WithoutCancel(ctx), id) }()
	if err := s.Engine.Start(ctx, id); err != nil {
		return err
	}
	code, err := s.Engine.Wait(ctx, id)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("copy exited with code %d", code)
	}
	return nil
}

func (s *Snapshotter) discard(ctx context.Context, snap *Snapshot) {
	for _, it := range snap.Items {
		_ = s.Engine.RemoveVolume(context.WithoutCancel(ctx), it.Volume)
	}
}

// prune keeps the newest Keep snapshots (by time label) of a container.
func (s *Snapshotter) prune(ctx context.Context, name string) {
	keep := s.Keep
	if keep <= 0 {
		keep = 3
	}
	vols, err := s.Engine.Volumes(ctx, LabelOf+"="+name)
	if err != nil {
		return
	}
	times := map[string]bool{}
	for _, v := range vols {
		times[v.Labels[LabelTime]] = true
	}
	var sorted []string
	for t := range times {
		sorted = append(sorted, t)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(sorted)))
	if len(sorted) <= keep {
		return
	}
	drop := map[string]bool{}
	for _, t := range sorted[keep:] {
		drop[t] = true
	}
	for _, v := range vols {
		if drop[v.Labels[LabelTime]] {
			_ = s.Engine.RemoveVolume(ctx, v.Name)
		}
	}
}

// Excludes parses the neptide.snapshot.exclude label ("/media, /cache").
func Excludes(label string) []string {
	var out []string
	for _, p := range strings.Split(label, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// String summarises a snapshot.
func (s *Snapshot) String() string {
	return s.Container + ": " + strconv.Itoa(len(s.Items)) + " mount(s)"
}
