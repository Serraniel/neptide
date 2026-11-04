// SPDX-License-Identifier: AGPL-3.0-or-later

// Package snapshot copies a stopped container's writable data (volumes and
// bind mounts) into snapshot volumes and restores it, so a rollback brings
// back the data that belongs to the old version: no half-migrated databases.
//
// It errs on the side of not touching data: read-only mounts, system paths,
// and data another running container uses are skipped (and reported), and a
// snapshot that would leave less than 10 % free space is refused.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
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

// HelperImage copies the data. GNU coreutils: cp -a keeps xattrs, ACLs and
// hardlinks, --sparse keeps sparse files sparse, rm/find stay on one file
// system. Pinned by digest, as everything that touches data.
const HelperImage = "debian:13-slim@sha256:f6b08b09a05725f9431502d3b5714f5c4576e82c1a84fa1debe0c24a7e0c3ec4"

// exit codes of the helper scripts
const (
	exitFile    = 10 // the source was a single file
	exitNoSpace = 42
)

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
	Containers(ctx context.Context, labels ...string) ([]engine.ContainerSummary, error)
	Inspect(ctx context.Context, id string) (*engine.Container, error)
}

// Snapshotter takes and restores snapshots.
type Snapshotter struct {
	Engine Engine
	// Keep is how many snapshots per container survive pruning (default 3).
	Keep int
	// DockerRoot is never snapshotted (default /var/lib/docker).
	DockerRoot string
	// Now is the clock (tests).
	Now func() time.Time
}

// Item is one copied mount.
type Item struct {
	Mount  engine.Mount
	Volume string
	File   bool // a single-file bind mount
}

// Snapshot is a set of copied mounts of one container.
type Snapshot struct {
	Container string
	Items     []Item
	// Skipped lists mounts left out and why.
	Skipped []string
	// Took is how long copying took; restoring gets at least three times that.
	Took time.Duration
}

var unsafe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// Prepare pulls the helper image. Call it before stopping the container, so
// pulling never adds downtime and an offline host fails before anything stops.
func (s *Snapshotter) Prepare(ctx context.Context) error {
	if err := s.Engine.Pull(ctx, HelperImage, nil); err != nil {
		return fmt.Errorf("snapshot helper image: %w", err)
	}
	return nil
}

// systemPaths are never snapshotted or restored, whatever a container mounts.
var systemPaths = []string{"/", "/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var/run"}

// Select decides which mounts of ct are snapshotted. inUse holds sources
// (volume names and host paths) that other running containers mount.
func (s *Snapshotter) Select(ct *engine.Container, exclude []string, inUse map[string]string) (copyMounts []engine.Mount, skipped []string) {
	root := s.DockerRoot
	if root == "" {
		root = "/var/lib/docker"
	}
	for _, m := range ct.Mounts {
		src := source(m)
		switch {
		case m.Type != "volume" && m.Type != "bind":
			continue // tmpfs, npipe: nothing persistent
		case slices.Contains(exclude, m.Destination):
			skipped = append(skipped, m.Destination+": excluded")
		case !m.RW:
			skipped = append(skipped, m.Destination+": read-only, not data this container changes")
		case m.Type == "bind" && isSystemPath(src, root):
			skipped = append(skipped, m.Destination+": system path "+src+", never snapshotted")
		case inUse[src] != "":
			skipped = append(skipped, m.Destination+": also used by running container "+inUse[src]+", restoring it would pull data from under it")
		default:
			copyMounts = append(copyMounts, m)
		}
	}
	return copyMounts, skipped
}

func isSystemPath(p, dockerRoot string) bool {
	p = path.Clean(p)
	if p == "/var/run/docker.sock" || p == "/run/docker.sock" {
		return true
	}
	for _, sp := range systemPaths {
		if p == sp || (sp != "/" && strings.HasPrefix(p, sp+"/")) {
			return true
		}
	}
	dockerRoot = path.Clean(dockerRoot)
	return p == dockerRoot || strings.HasPrefix(p, dockerRoot+"/") || strings.HasPrefix(dockerRoot, p+"/")
}

// inUse maps the sources other running containers mount to their name.
func (s *Snapshotter) inUse(ctx context.Context, self string) (map[string]string, error) {
	list, err := s.Engine.Containers(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, c := range list {
		if c.ID == self || c.Labels["neptide.helper"] != "" {
			continue
		}
		ct, err := s.Engine.Inspect(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, m := range ct.Mounts {
			out[source(m)] = strings.TrimPrefix(ct.Name, "/")
		}
	}
	return out, nil
}

// Take copies the selected mounts of ct, which must be stopped. Mounts that
// are skipped are listed in the result. On error nothing is kept.
func (s *Snapshotter) Take(ctx context.Context, ct *engine.Container, exclude []string) (*Snapshot, error) {
	started := time.Now()
	name := strings.TrimPrefix(ct.Name, "/")
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	ts := now().UTC().Format("20060102T150405.000Z")
	inUse, err := s.inUse(ctx, ct.ID)
	if err != nil {
		return nil, fmt.Errorf("checking which data other containers use: %w", err)
	}
	mounts, skipped := s.Select(ct, exclude, inUse)
	snap := &Snapshot{Container: name, Skipped: skipped}
	for i, m := range mounts {
		vol := fmt.Sprintf("neptide-snap-%s-%s-%d", unsafe.ReplaceAllString(name, "_"), ts, i)
		labels := map[string]string{LabelOf: name, LabelMount: m.Destination, LabelTime: ts}
		if err := s.Engine.CreateVolume(ctx, vol, labels); err != nil {
			s.discard(ctx, snap)
			return nil, err
		}
		item := Item{Mount: m, Volume: vol}
		snap.Items = append(snap.Items, item)
		// Refuse when the copy would leave less than 10 % of the space free.
		script := `need=$(du -sk /src | cut -f1); free=$(df -Pk /dst | awk 'NR==2{print $4}')
if [ "$need" -ge "$((free * 9 / 10))" ]; then echo "need ${need}K, ${free}K free" >&2; exit 42; fi
if [ -d /src ]; then cp -a --sparse=always /src/. /dst/; else cp -a --sparse=always /src /dst/file && exit 10; fi`
		code, err := s.run(ctx, m, vol, true, script)
		switch {
		case err != nil:
			s.discard(ctx, snap)
			return nil, fmt.Errorf("snapshot of %s: %w", m.Destination, err)
		case code == exitFile:
			snap.Items[len(snap.Items)-1].File = true
		case code == exitNoSpace:
			s.discard(ctx, snap)
			return nil, fmt.Errorf("snapshot of %s: not enough free disk space", m.Destination)
		case code != 0:
			s.discard(ctx, snap)
			return nil, fmt.Errorf("snapshot of %s: copy exited with code %d", m.Destination, code)
		}
	}
	snap.Took = time.Since(started)
	s.prune(ctx, name)
	return snap, nil
}

// Restore puts the snapshot's data back into the original mounts, replacing
// what is there now. The container using them must be stopped.
func (s *Snapshotter) Restore(ctx context.Context, snap *Snapshot) error {
	var errs []error
	for _, it := range snap.Items {
		// Directories: empty without crossing into other file systems, then copy.
		// Single files: overwrite in place, keeping the bind-mounted inode.
		script := "find /dst -xdev -mindepth 1 -maxdepth 1 -exec rm -rf --one-file-system -- {} + && cp -a --sparse=always /src/. /dst/"
		if it.File {
			script = "cp --preserve=all --sparse=always /src/file /dst"
		}
		code, err := s.run(ctx, it.Mount, it.Volume, false, script)
		if err == nil && code != 0 {
			err = fmt.Errorf("copy exited with code %d", code)
		}
		if err != nil {
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

// run executes script in a throwaway helper with the data mount and the
// snapshot volume. take=true: data at /src read-only, snapshot at /dst;
// otherwise the other way round. Binds are non-recursive, so nested mounts
// below the data are neither copied nor deleted.
func (s *Snapshotter) run(ctx context.Context, data engine.Mount, vol string, take bool, script string) (int, error) {
	dataTarget, volTarget := "/dst", "/src"
	if take {
		dataTarget, volTarget = "/src", "/dst"
	}
	dataMount := map[string]any{"Type": data.Type, "Source": source(data), "Target": dataTarget, "ReadOnly": take}
	if data.Type == "bind" {
		dataMount["BindOptions"] = map[string]any{"NonRecursive": true}
	}
	mounts := []map[string]any{
		dataMount,
		{"Type": "volume", "Source": vol, "Target": volTarget, "ReadOnly": !take},
	}
	id, err := s.Engine.Create(ctx, "", map[string]any{
		"Image":  HelperImage,
		"Cmd":    []string{"sh", "-c", script},
		"Labels": map[string]string{"neptide.helper": "snapshot"},
	}, map[string]any{"Mounts": mounts, "NetworkMode": "none"}, nil)
	if err != nil {
		return -1, err
	}
	defer func() { _ = s.Engine.Remove(context.WithoutCancel(ctx), id) }()
	if err := s.Engine.Start(ctx, id); err != nil {
		return -1, err
	}
	return s.Engine.Wait(ctx, id)
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

// RestoreTimeout is how long a restore may take: at least ten minutes and
// three times what the snapshot took.
func (s *Snapshot) RestoreTimeout() time.Duration {
	return max(10*time.Minute, 3*s.Took)
}

// String summarises a snapshot.
func (s *Snapshot) String() string {
	return fmt.Sprintf("%s: %d mount(s), %d skipped", s.Container, len(s.Items), len(s.Skipped))
}
