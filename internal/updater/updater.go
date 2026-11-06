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
	Deferred   EventKind = "deferred"     // update found, waits for its maintenance window
	Restarted  EventKind = "restarted"    // scheduled restart (neptide.restart) passed the health gate
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
	// Window is the maintenance window a Deferred update waits for, or the
	// schedule of a Restarted container.
	Window string
}

func (e Event) String() string {
	what := "container"
	if e.Swarm {
		what = "service"
	}
	switch e.Kind {
	case Available:
		return fmt.Sprintf("Update available for %s %s: %s → %s", what, e.Target, e.From, e.To)
	case Restarted:
		return fmt.Sprintf("Restarted %s %s on schedule (%s)", what, e.Target, e.Window)
	case Deferred:
		return fmt.Sprintf("Update for %s %s waits for its maintenance window (%s): %s → %s", what, e.Target, e.Window, e.From, e.To)
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
	// Now is the clock for maintenance windows (time.Now when nil).
	Now func() time.Time

	wake time.Time
}

// deferUntil remembers when the window of a deferred update opens next.
func (u *Updater) deferUntil(w rollout.Windows) {
	next := w.Next(u.now())
	if !next.IsZero() && (u.wake.IsZero() || next.Before(u.wake)) {
		u.wake = next
	}
}

// NextWindow is when the earliest window of an update deferred in the last
// cycle opens (zero if none was deferred). The caller should run the next
// cycle then if that is sooner than its interval, so a window shorter than
// the interval is not missed.
func (u *Updater) NextWindow() time.Time { return u.wake }

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

// Run performs one cycle and returns the events it produced.
func (u *Updater) Run(ctx context.Context) ([]Event, error) {
	u.wake = time.Time{}
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
	var deps map[string][]engine.ContainerSummary
	if len(containers) > 0 {
		// Every container, enabled or not, running or not, may share the
		// network namespace of one we update.
		all, err := u.Engine.AllContainers(ctx)
		if err != nil {
			return nil, fmt.Errorf("list containers: %w", err)
		}
		deps = dependents(all)
	}
	for _, c := range containers {
		if skip(c.Labels) {
			continue
		}
		name := strings.TrimPrefix(firstOr(c.Names, c.ID), "/")
		// By name: an earlier update in this cycle may have replaced it.
		u.container(ctx, name, c.Labels, deps, emit)
		u.scheduledRestart(ctx, name, c.Labels, deps, emit)
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

// dependents maps a container ID to the containers, running or stopped,
// that share its network namespace.
func dependents(all []engine.ContainerSummary) map[string][]engine.ContainerSummary {
	out := map[string][]engine.ContainerSummary{}
	for _, c := range all {
		if parent := rollout.SharesNetworkWith(c.HostConfig.NetworkMode); parent != "" {
			out[parent] = append(out[parent], c)
		}
	}
	return out
}

// reattach follows up on an update attempt of before. If the container
// under its name now is a different one, or was restarted, the containers
// sharing its network namespace are moved into the new one, and theirs in
// turn. trusted is false when the update failed without a clean rollback:
// whatever holds the name then may be the broken new version.
func (u *Updater) reattach(ctx context.Context, before *engine.Container, deps map[string][]engine.ContainerSummary, trusted bool, emit func(Event)) {
	if len(deps[before.ID]) == 0 {
		return
	}
	// Like a rollback: finish even if neptide is being stopped.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	defer cancel()
	name := strings.TrimPrefix(before.Name, "/")
	after, err := u.Engine.Inspect(ctx, name)
	if err == nil && after.ID == before.ID && after.State.StartedAt == before.State.StartedAt && after.State.Running {
		return // never stopped: the namespace is the same
	}
	parentOK := err == nil && after.State.Running && (trusted || after.ID == before.ID)
	u.follow(ctx, name, before.ID, after, parentOK, deps, emit)
}

func (u *Updater) follow(ctx context.Context, parent, oldID string, after *engine.Container, parentOK bool, deps map[string][]engine.ContainerSummary, emit func(Event)) {
	for _, d := range deps[oldID] {
		dep := strings.TrimPrefix(firstOr(d.Names, d.ID), "/")
		if d.Labels["com.docker.compose.oneoff"] == "True" {
			// A docker compose run command: stopping it would kill the
			// command, and with --rm the container is gone afterwards.
			emit(Event{Kind: Failed, Target: dep, Err: fmt.Errorf("this docker compose run container shares the network of %s, which was restarted; run the command again", parent)})
			continue
		}
		if d.Labels[rollout.LabelSelf] == "true" {
			// Re-attaching means stopping it: neptide would kill itself.
			emit(Event{Kind: Failed, Target: dep, Err: fmt.Errorf("neptide shares the network of %s, which was restarted; restart neptide (docker restart %s) to get its network back", parent, dep)})
			continue
		}
		if !parentOK {
			emit(Event{Kind: Failed, Target: dep, Err: fmt.Errorf("it shares the network of %s, which is not running after neptide changed it (after a failed update the old version may be parked as %s-neptide-old); start %s, then restart %s", parent, parent, parent, dep)})
			continue
		}
		gate := u.Opts.Gate
		if pol, err := rollout.PolicyFrom(d.Labels, u.Opts.Defaults); err == nil && pol.HealthTimeout > 0 {
			gate.Timeout = pol.HealthTimeout
		}
		newID, err := rollout.Reattach(ctx, u.Engine, d.ID, after.ID, gate)
		if err != nil {
			emit(Event{Kind: Failed, Target: dep, Err: err})
			continue
		}
		u.Log.Info("re-attached " + dep + " to the network of " + parent)
		// Containers in this one's namespace now point at a removed ID.
		if newID != d.ID && len(deps[d.ID]) > 0 {
			if nd, err := u.Engine.Inspect(ctx, newID); err == nil {
				u.follow(ctx, dep, d.ID, nd, nd.State.Running, deps, emit)
			}
		}
	}
}

func (u *Updater) container(ctx context.Context, name string, labels map[string]string, deps map[string][]engine.ContainerSummary, emit func(Event)) {
	fail := func(err error) { emit(Event{Kind: Failed, Target: name, Err: err}) }
	pol, err := u.policy(labels)
	if err != nil {
		fail(err)
		return
	}
	if !pol.Enabled {
		return
	}
	// With the other labels, so a typo shows before an update is due.
	hooks, err := rollout.HooksFrom(labels)
	if err != nil {
		fail(err)
		return
	}
	ct, err := u.Engine.Inspect(ctx, name)
	if err != nil {
		fail(err)
		return
	}
	id := ct.ID
	if autoRemove(ct) {
		// docker run --rm: the daemon deletes the old container as soon as
		// it stops, so there is nothing to roll back to.
		fail(errors.New("started with --rm (AutoRemove), so an update could not be rolled back; not updated"))
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
	if !pol.Window.Open(u.now()) {
		u.deferUntil(pol.Window)
		emit(Event{Kind: Deferred, Target: name, From: from, To: to, Window: pol.Window.String()})
		return
	}
	gate := u.Opts.Gate
	gate.Timeout = pol.HealthTimeout
	// Recreate by tag, as compose would; the pull resolves the same digest.
	rec := rollout.Recreate{Auth: u.auth(next.Host), Gate: gate, OldConfig: u.oldConfig(ref), Hooks: hooks,
		Log: func(msg string) { u.Log.Warn(msg) }}
	if pol.Snapshot {
		rec.Snapshots = &snapshot.Snapshotter{Engine: u.Engine, Keep: u.Opts.SnapshotKeep, DockerRoot: u.dockerRoot(ctx),
			Binds: labels[rollout.LabelSnapshotBinds] == "true"}
		rec.Exclude = snapshot.Excludes(labels[rollout.LabelSnapshotExclude])
		rec.Log = func(msg string) { u.Log.Warn(msg) }
	}
	err = rollout.RecreateContainer(ctx, u.Engine, id, next.WithDigest("").String(), rec)
	defer u.reattach(ctx, ct, deps, err == nil || errors.Is(err, rollout.ErrRolledBack), emit)
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
	if !pol.Window.Open(u.now()) {
		u.deferUntil(pol.Window)
		emit(Event{Kind: Deferred, Target: name, Swarm: true, From: from, To: to, Window: pol.Window.String()})
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

func (u *Updater) dockerRoot(ctx context.Context) string {
	if root, err := u.Engine.DockerRoot(ctx); err == nil && root != "" {
		return root
	}
	return "/var/lib/docker"
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

// skip reports containers that are never updated directly: swarm tasks
// (updated through their service), neptide itself (it cannot replace itself
// mid-run) and one-off `docker compose run` containers, which carry their
// service's labels but are short-lived commands, not the service.
func skip(labels map[string]string) bool {
	return labels["com.docker.swarm.service.id"] != "" || labels[rollout.LabelSelf] == "true" ||
		labels["com.docker.compose.oneoff"] == "True"
}

// autoRemove reports a container created with --rm.
func autoRemove(ct *engine.Container) bool {
	v, _ := ct.HostConfig["AutoRemove"].(bool)
	return v
}

// scheduledRestart restarts a container whose neptide.restart schedule is
// due, behind the health gate. A container updated in this cycle has just
// started, so it is not due.
func (u *Updater) scheduledRestart(ctx context.Context, name string, labels map[string]string, deps map[string][]engine.ContainerSummary, emit func(Event)) {
	spec, ok := labels[rollout.LabelRestart]
	if !ok {
		return
	}
	sched, err := rollout.ParseRestart(spec)
	if err != nil {
		emit(Event{Kind: Failed, Target: name, Err: err})
		return
	}
	ct, err := u.Engine.Inspect(ctx, name)
	if err != nil || !ct.State.Running || ct.State.Paused || ct.State.Restarting {
		return // paused on purpose, or already restarting
	}
	started, _ := time.Parse(time.RFC3339Nano, ct.State.StartedAt)
	if !sched.Due(u.now(), started) {
		if len(sched.Window) > 0 && !sched.Window.Open(u.now()) && !u.Opts.ForceNotify {
			u.deferUntil(sched.Window) // wake up when the window opens
		}
		return
	}
	if u.Opts.ForceNotify {
		u.Log.Info("monitor only: " + name + " would be restarted on schedule (" + sched.String() + ")")
		return
	}
	gate := u.Opts.Gate
	if pol, err := u.policy(labels); err == nil && pol.HealthTimeout > 0 {
		gate.Timeout = pol.HealthTimeout
	}
	err = rollout.Restart(ctx, u.Engine, ct.ID, gate)
	u.reattach(ctx, ct, deps, true, emit) // the namespace was recreated
	if err != nil {
		emit(Event{Kind: Failed, Target: name, Err: fmt.Errorf("scheduled restart: %w", err)})
		return
	}
	emit(Event{Kind: Restarted, Target: name, Window: sched.String()})
}
