// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
)

// ErrRolledBack means the new version failed its health gate and the old one
// was restored. The returned error wraps the reason.
var ErrRolledBack = errors.New("rolled back")

// Engine is what rollout needs from a Docker daemon.
type Engine interface {
	Inspect(ctx context.Context, id string) (*engine.Container, error)
	InspectImage(ctx context.Context, ref string) (*engine.Image, error)
	Pull(ctx context.Context, ref string, auth *engine.RegistryAuth) error
	Create(ctx context.Context, name string, config, hostConfig map[string]any, networks map[string]map[string]any) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Rename(ctx context.Context, id, name string) error
	Remove(ctx context.Context, id string) error
	InspectService(ctx context.Context, id string) (*engine.Service, error)
	UpdateService(ctx context.Context, s *engine.Service, auth *engine.RegistryAuth) error
}

// Gate decides when a started container counts as healthy.
type Gate struct {
	// Timeout bounds the wait for a HEALTHCHECK to report healthy.
	Timeout time.Duration
	// Stable is how long a container without HEALTHCHECK must keep running.
	Stable time.Duration
	// Poll is the inspect interval.
	Poll time.Duration
}

// DefaultGate is used when a field is zero.
var DefaultGate = Gate{Timeout: 2 * time.Minute, Stable: 15 * time.Second, Poll: time.Second}

func (g Gate) withDefaults() Gate {
	if g.Timeout <= 0 {
		g.Timeout = DefaultGate.Timeout
	}
	if g.Stable <= 0 {
		g.Stable = DefaultGate.Stable
	}
	if g.Poll <= 0 {
		g.Poll = DefaultGate.Poll
	}
	return g
}

// OldConfig fetches the image config of a container whose image is no
// longer in the local store (see RecreateContainer). May be nil.
type OldConfig func(ctx context.Context, old *engine.Container) (map[string]any, error)

// RecreateContainer replaces container id with one running newRef, keeping
// its configuration. The old container is renamed and stopped, not removed,
// until the new one passes the gate; otherwise it is restored.
func RecreateContainer(ctx context.Context, e Engine, id, newRef string, auth *engine.RegistryAuth, gate Gate, oldConfig OldConfig) error {
	gate = gate.withDefaults()
	old, err := e.Inspect(ctx, id)
	if err != nil {
		return err
	}
	// Inspect the old image before pulling: the pull may take its tag, and
	// with the containerd image store an untagged image cannot be inspected.
	oldImage, err := e.InspectImage(ctx, old.Image)
	if engine.IsNotFound(err) {
		// Its tag moved outside neptide (manual pull or build). Recreating
		// without its defaults would carry them over, e.g. its HEALTHCHECK
		// into the new image, so a fallback is required or we refuse.
		if oldConfig == nil {
			return fmt.Errorf("the running image is gone from the local image store; refusing to recreate without knowing its defaults")
		}
		cfg, ferr := oldConfig(ctx, old)
		if ferr != nil {
			return fmt.Errorf("the running image is gone from the local image store and its config could not be fetched (%w); refusing to recreate", ferr)
		}
		oldImage = &engine.Image{ID: old.Image, Config: cfg}
	} else if err != nil {
		return fmt.Errorf("old image: %w", err)
	}
	if err := e.Pull(ctx, newRef, auth); err != nil {
		return err
	}
	newImage, err := e.InspectImage(ctx, newRef)
	if err != nil {
		return fmt.Errorf("new image: %w", err)
	}
	if newImage.ID == old.Image {
		return nil // already running this image
	}

	name := strings.TrimPrefix(old.Name, "/")
	parked := name + "-neptide-old"
	config := containerConfig(old, oldImage, newRef)
	networks := endpoints(old)
	stopTimeout := 10 * time.Second
	if s, ok := old.Config["StopTimeout"].(float64); ok && s > 0 {
		stopTimeout = time.Duration(s) * time.Second
	}

	if err := e.Rename(ctx, old.ID, parked); err != nil {
		return fmt.Errorf("park old container: %w", err)
	}
	restore := func(cause error, newID string) error {
		// Use a fresh context: the caller's may be what was cancelled.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		var errs []error
		if newID != "" {
			errs = append(errs, e.Remove(rctx, newID))
		}
		errs = append(errs, e.Rename(rctx, old.ID, name))
		if old.State.Running {
			errs = append(errs, e.Start(rctx, old.ID))
		}
		if err := errors.Join(errs...); err != nil {
			// Not ErrRolledBack: the old version is not running again.
			return fmt.Errorf("update failed (%w) and restoring the old container failed, it is parked as %q: %w", cause, parked, err)
		}
		return fmt.Errorf("%w: %w", ErrRolledBack, cause)
	}

	if err := e.Stop(ctx, old.ID, stopTimeout); err != nil {
		return restore(fmt.Errorf("stop old container: %w", err), "")
	}
	newID, err := e.Create(ctx, name, config, old.HostConfig, networks)
	if err != nil {
		return restore(fmt.Errorf("create: %w", err), "")
	}
	if err := e.Start(ctx, newID); err != nil {
		return restore(fmt.Errorf("start: %w", err), newID)
	}
	if err := waitHealthy(ctx, e, newID, gate); err != nil {
		return restore(err, newID)
	}
	if err := e.Remove(ctx, old.ID); err != nil {
		return fmt.Errorf("updated, but removing the old container failed: %w", err)
	}
	return nil
}

// containerConfig copies the old config for the new image. Settings equal to
// the old image's defaults are dropped, so the new image's defaults apply
// (otherwise an image that changes its CMD or ENV would keep the old ones).
func containerConfig(old *engine.Container, oldImage *engine.Image, newRef string) map[string]any {
	cfg := make(map[string]any, len(old.Config))
	for k, v := range old.Config {
		cfg[k] = v
	}
	cfg["Image"] = newRef
	for _, k := range []string{"Cmd", "Entrypoint", "WorkingDir", "User", "ExposedPorts", "Volumes", "Healthcheck", "StopSignal", "Shell", "OnBuild"} {
		if v, ok := cfg[k]; ok && reflect.DeepEqual(v, oldImage.Config[k]) {
			delete(cfg, k)
		}
	}
	cfg["Env"] = minus(cfg["Env"], oldImage.Config["Env"])
	if labels, ok := cfg["Labels"].(map[string]any); ok {
		imgLabels, _ := oldImage.Config["Labels"].(map[string]any)
		kept := map[string]any{}
		for k, v := range labels {
			if iv, ok := imgLabels[k]; !ok || iv != v {
				kept[k] = v
			}
		}
		cfg["Labels"] = kept
	}
	// A hostname equal to the old short ID was generated, not chosen.
	if h, _ := cfg["Hostname"].(string); len(old.ID) >= 12 && h == old.ID[:12] {
		delete(cfg, "Hostname")
	}
	return cfg
}

func minus(list, remove any) any {
	l, ok := list.([]any)
	if !ok {
		return list
	}
	r, _ := remove.([]any)
	drop := map[any]bool{}
	for _, v := range r {
		drop[v] = true
	}
	out := []any{}
	for _, v := range l {
		if !drop[v] {
			out = append(out, v)
		}
	}
	return out
}

// endpoints keeps the user-set parts of each network attachment.
func endpoints(old *engine.Container) map[string]map[string]any {
	mode, _ := old.HostConfig["NetworkMode"].(string)
	if mode == "host" || mode == "none" || strings.HasPrefix(mode, "container:") {
		return nil
	}
	out := map[string]map[string]any{}
	for name, ep := range old.NetworkSettings.Networks {
		keep := map[string]any{}
		for _, k := range []string{"IPAMConfig", "Links", "DriverOpts"} {
			if v, ok := ep[k]; ok && v != nil {
				keep[k] = v
			}
		}
		if aliases, ok := ep["Aliases"].([]any); ok {
			var a []any
			for _, al := range aliases {
				if s, _ := al.(string); len(old.ID) < 12 || s != old.ID[:12] {
					a = append(a, al)
				}
			}
			if len(a) > 0 {
				keep["Aliases"] = a
			}
		}
		out[name] = keep
	}
	return out
}

func waitHealthy(ctx context.Context, e Engine, id string, g Gate) error {
	deadline := time.Now().Add(g.Timeout)
	var runningSince time.Time
	var restarts = -1
	for {
		ct, err := e.Inspect(ctx, id)
		if err != nil {
			return err
		}
		if restarts < 0 {
			restarts = ct.RestartCount
		}
		switch {
		case !ct.State.Running && !ct.State.Restarting:
			return fmt.Errorf("container exited with code %d", ct.State.ExitCode)
		case ct.RestartCount > restarts || ct.State.Restarting:
			return errors.New("container is restarting")
		case ct.State.Health != nil:
			switch ct.State.Health.Status {
			case "healthy":
				return nil
			case "unhealthy":
				return errors.New("health check reports unhealthy")
			}
		default:
			if runningSince.IsZero() {
				runningSince = time.Now()
			}
			if time.Since(runningSince) >= g.Stable {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy after %s", g.Timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.Poll):
		}
	}
}
