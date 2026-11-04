// SPDX-License-Identifier: AGPL-3.0-or-later

// Package updater runs one update cycle: find enabled containers and swarm
// services, check their registries, and update or report them.
package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/registry"
	"github.com/Serraniel/neptide/pkg/rollout"
	"github.com/Serraniel/neptide/pkg/snapshot"
	"github.com/Serraniel/neptide/pkg/version"
)

// EventKind classifies what happened to a target.
type EventKind string

// Event kinds.
const (
	Available  EventKind = "available"    // update found, mode notify
	Beyond     EventKind = "beyond_track" // newer release outside the track
	Updated    EventKind = "updated"
	RolledBack EventKind = "rolled_back"
	Failed     EventKind = "failed"
)

// Event is reported for every target that has something to say.
type Event struct {
	Kind   EventKind
	Target string // container or service name
	Swarm  bool
	From   string
	To     string
	Err    error
}

func (e Event) String() string {
	what := "container"
	if e.Swarm {
		what = "service"
	}
	switch e.Kind {
	case Available:
		return fmt.Sprintf("Update available for %s %s: %s → %s", what, e.Target, e.From, e.To)
	case Beyond:
		return fmt.Sprintf("%s %s: %s is available, outside its update track (now %s)", what, e.Target, e.To, e.From)
	case Updated:
		return fmt.Sprintf("Updated %s %s: %s → %s", what, e.Target, e.From, e.To)
	case RolledBack:
		return fmt.Sprintf("Rolled back %s %s to %s: %v", what, e.Target, e.From, e.Err)
	default:
		return fmt.Sprintf("Updating %s %s failed: %v", what, e.Target, e.Err)
	}
}

// Options configure the updater.
type Options struct {
	// All updates every container unless labelled neptide.enable=false.
	All bool
	// Defaults apply where labels say nothing.
	Defaults rollout.Policy
	Gate     rollout.Gate
	// SkipContainers and SkipServices limit a cycle to one kind of target.
	// In a swarm, run exactly one instance with services enabled.
	SkipContainers, SkipServices bool
	// SnapshotKeep is how many data snapshots per container are kept.
	SnapshotKeep int
	// ForceNotify reports updates without applying them, whatever labels say.
	ForceNotify bool
	// SwarmTimeout bounds one service update.
	SwarmTimeout time.Duration
}

// Updater holds its collaborators.
type Updater struct {
	Engine   *engine.Client
	Registry *registry.Client
	Notify   func(Event)
	Log      *slog.Logger
	Opts     Options
}

// Run performs one cycle and returns the events it produced.
func (u *Updater) Run(ctx context.Context) ([]Event, error) {
	var events []Event
	emit := func(e Event) {
		events = append(events, e)
		level := slog.LevelInfo
		if e.Kind == Failed || e.Kind == RolledBack {
			level = slog.LevelError
		}
		u.Log.Log(ctx, level, e.String())
		if u.Notify != nil {
			u.Notify(e)
		}
	}
	var labels []string
	if !u.Opts.All {
		labels = []string{rollout.LabelEnable + "=true"}
	}

	var containers []engine.ContainerSummary
	if !u.Opts.SkipContainers {
		var err error
		if containers, err = u.Engine.Containers(ctx, labels...); err != nil {
			return nil, fmt.Errorf("list containers: %w", err)
		}
	}
	for _, c := range containers {
		if c.Labels["com.docker.swarm.service.id"] != "" || c.Labels[rollout.LabelSelf] == "true" {
			continue // swarm tasks are updated through their service; never replace ourselves mid-run
		}
		name := strings.TrimPrefix(firstOr(c.Names, c.ID), "/")
		u.container(ctx, c.ID, name, c.Labels, emit)
	}

	if u.Opts.SkipServices {
		return events, nil
	}
	manager, err := u.Engine.SwarmActive(ctx)
	if err != nil {
		return events, fmt.Errorf("swarm info: %w", err)
	}
	if manager {
		services, err := u.Engine.Services(ctx, labels...)
		if err != nil {
			return events, fmt.Errorf("list services: %w", err)
		}
		for i := range services {
			u.service(ctx, &services[i], emit)
		}
	}
	return events, nil
}

func (u *Updater) policy(labels map[string]string) (rollout.Policy, error) {
	def := u.Opts.Defaults
	def.Enabled = true // listing already filtered on the enable label
	p, err := rollout.PolicyFrom(labels, def)
	if u.Opts.ForceNotify {
		p.Mode = rollout.ModeNotify
	}
	return p, err
}

// plan resolves what ref should run. It returns the new ref ("" if current)
// and a newer release beyond the track, if any.
func (u *Updater) plan(ctx context.Context, current registry.Ref, runningDigest string, track version.Track) (next registry.Ref, beyond string, err error) {
	target := current
	// Tags are listed even for track=digest, to report releases beyond the
	// track. Listing is not rate-limited like pulls, but some registries
	// refuse it; then only the digest of the current tag is followed.
	if tags, err := u.Registry.Tags(ctx, current); err != nil {
		if track != version.Digest {
			u.Log.Warn("cannot list tags, following digest only", "image", current.Name(), "err", err)
		}
	} else {
		nextTag, newest := version.Next(current.Tag, tags, track)
		if nextTag != "" {
			target = current.WithTag(nextTag)
		}
		if newest != "" && newest != nextTag {
			beyond = newest
		}
	}
	digest, err := u.Registry.Digest(ctx, target)
	if err != nil {
		return registry.Ref{}, beyond, err
	}
	if target.Tag == current.Tag && digest == runningDigest {
		return registry.Ref{}, beyond, nil
	}
	return target.WithDigest(digest), beyond, nil
}

func (u *Updater) container(ctx context.Context, id, name string, labels map[string]string, emit func(Event)) {
	fail := func(err error) { emit(Event{Kind: Failed, Target: name, Err: err}) }
	pol, err := u.policy(labels)
	if err != nil {
		fail(err)
		return
	}
	if !pol.Enabled {
		return
	}
	ct, err := u.Engine.Inspect(ctx, id)
	if err != nil {
		fail(err)
		return
	}
	ref, err := registry.ParseRef(ct.ConfigImage())
	if err != nil || ref.Digest != "" {
		return // built locally, or pinned to a digest on purpose
	}
	var running string
	if img, err := u.Engine.InspectImage(ctx, ct.Image); err == nil {
		running = repoDigest(img.RepoDigests, ref)
		if running == "" {
			return // no registry digest: a locally built image
		}
	} else if engine.IsNotFound(err) {
		// The running image lost its tag (a newer one was pulled or built under
		// it) and the containerd image store no longer resolves it by ID. The
		// container is then behind its tag: leave running empty so the registry
		// digest counts as an update. Locally built tags are still skipped.
		tagged, terr := u.Engine.InspectImage(ctx, ct.ConfigImage())
		if terr != nil || repoDigest(tagged.RepoDigests, ref) == "" {
			return
		}
	} else {
		fail(err)
		return
	}
	next, beyond, err := u.plan(ctx, ref, running, pol.Track)
	if err != nil {
		fail(err)
		return
	}
	if beyond != "" {
		emit(Event{Kind: Beyond, Target: name, From: ref.Tag, To: beyond})
	}
	if next.Digest == "" {
		return
	}
	from, to := ref.String(), next.WithDigest("").String()
	if next.Tag == ref.Tag {
		to += " (" + short(next.Digest) + ")"
	}
	if pol.Mode == rollout.ModeNotify {
		emit(Event{Kind: Available, Target: name, From: from, To: to})
		return
	}
	gate := u.Opts.Gate
	gate.Timeout = pol.HealthTimeout
	// Recreate by tag, as compose would; the pull resolves the same digest.
	rec := rollout.Recreate{Auth: u.auth(next.Host), Gate: gate, OldConfig: u.oldConfig(ref)}
	if pol.Snapshot {
		rec.Snapshots = &snapshot.Snapshotter{Engine: u.Engine, Keep: u.Opts.SnapshotKeep}
		rec.Exclude = snapshot.Excludes(labels[rollout.LabelSnapshotExclude])
	}
	err = rollout.RecreateContainer(ctx, u.Engine, id, next.WithDigest("").String(), rec)
	switch {
	case errors.Is(err, rollout.ErrRolledBack):
		emit(Event{Kind: RolledBack, Target: name, From: from, To: to, Err: err})
	case err != nil:
		fail(err)
	default:
		emit(Event{Kind: Updated, Target: name, From: from, To: to})
	}
}

func (u *Updater) service(ctx context.Context, svc *engine.Service, emit func(Event)) {
	name := svc.Name()
	fail := func(err error) { emit(Event{Kind: Failed, Target: name, Swarm: true, Err: err}) }
	pol, err := u.policy(svc.Labels())
	if err != nil {
		fail(err)
		return
	}
	if !pol.Enabled {
		return
	}
	ref, err := registry.ParseRef(svc.Image())
	if err != nil {
		fail(err)
		return
	}
	running := ref.Digest
	ref.Digest = ""
	next, beyond, err := u.plan(ctx, ref, running, pol.Track)
	if err != nil {
		fail(err)
		return
	}
	if beyond != "" {
		emit(Event{Kind: Beyond, Target: name, Swarm: true, From: ref.Tag, To: beyond})
	}
	if next.Digest == "" {
		return
	}
	from, to := ref.String(), next.WithDigest("").String()
	if next.Tag == ref.Tag {
		to += " (" + short(next.Digest) + ")"
	}
	if pol.Mode == rollout.ModeNotify {
		emit(Event{Kind: Available, Target: name, Swarm: true, From: from, To: to})
		return
	}
	err = rollout.UpdateService(ctx, u.Engine, svc.ID, next.String(), u.auth(next.Host), u.Opts.SwarmTimeout)
	switch {
	case errors.Is(err, rollout.ErrRolledBack):
		emit(Event{Kind: RolledBack, Target: name, Swarm: true, From: from, To: to, Err: err})
	case err != nil:
		fail(err)
	default:
		emit(Event{Kind: Updated, Target: name, Swarm: true, From: from, To: to})
	}
}

// oldConfig fetches a vanished running image's config from its registry,
// using the manifest digest the daemon recorded on the container.
func (u *Updater) oldConfig(ref registry.Ref) rollout.OldConfig {
	return func(ctx context.Context, old *engine.Container) (map[string]any, error) {
		d, err := u.Engine.ManifestDigest(ctx, old.ID)
		if err != nil {
			return nil, err
		}
		if d == "" {
			return nil, fmt.Errorf("the daemon does not report the container's manifest digest (needs Engine API 1.48)")
		}
		return u.Registry.ImageConfig(ctx, ref.WithDigest(d))
	}
}

func (u *Updater) auth(host string) *engine.RegistryAuth {
	if u.Registry.Creds == nil {
		return nil
	}
	c := u.Registry.Creds(host)
	if c == nil {
		return nil
	}
	return &engine.RegistryAuth{Username: c.Username, Password: c.Password, ServerAddress: host}
}

// repoDigest finds the digest an image was pulled by for ref's repository.
func repoDigest(repoDigests []string, ref registry.Ref) string {
	for _, rd := range repoDigests {
		r, err := registry.ParseRef(rd)
		if err == nil && r.Host == ref.Host && r.Repo == ref.Repo {
			return r.Digest
		}
	}
	return ""
}

func short(d string) string {
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}
