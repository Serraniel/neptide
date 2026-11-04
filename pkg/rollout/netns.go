// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SharesNetworkWith returns the container whose network namespace c joins
// (network_mode: container:X; Compose turns service:X into that), or "".
func SharesNetworkWith(networkMode string) string {
	parent, ok := strings.CutPrefix(networkMode, "container:")
	if !ok {
		return ""
	}
	return parent
}

// Reattach moves container id into the network namespace of parentID, the
// container now running in its parent's place.
//
// A container that joins another's namespace (a VPN sidecar setup, for
// example) loses its network when the parent restarts, and cannot even
// start once the parent is replaced, because Docker resolved the parent to
// a container ID when the dependent was created. If the dependent already
// points at parentID it is restarted; otherwise it is recreated unchanged
// except for its network mode. It runs the same image as before: a tag that
// moved meanwhile is not picked up.
func Reattach(ctx context.Context, e Engine, id, parentID string, gate Gate) error {
	gate = gate.withDefaults()
	old, err := e.Inspect(ctx, id)
	if err != nil {
		return err
	}
	name := strings.TrimPrefix(old.Name, "/")
	mode, _ := old.HostConfig["NetworkMode"].(string)
	stopTimeout := 10 * time.Second
	if s, ok := old.Config["StopTimeout"].(float64); ok && s > 0 {
		stopTimeout = time.Duration(s) * time.Second
	}

	if SharesNetworkWith(mode) == parentID {
		if err := e.Stop(ctx, old.ID, stopTimeout); err != nil {
			return fmt.Errorf("restart %s: %w", name, err)
		}
		if err := e.Start(ctx, old.ID); err != nil {
			return fmt.Errorf("restart %s: %w", name, err)
		}
		return waitHealthy(ctx, e, old.ID, gate)
	}

	config := make(map[string]any, len(old.Config))
	for k, v := range old.Config {
		config[k] = v
	}
	// Inspect reports the parent's hostname here; the daemon rejects it
	// together with a container network mode.
	delete(config, "Hostname")
	delete(config, "Domainname")
	// Same image as before, by tag if the tag still points to it.
	if tag, _ := old.Config["Image"].(string); tag != "" {
		if img, err := e.InspectImage(ctx, tag); err != nil || img.ID != old.Image {
			config["Image"] = old.Image
		}
	}
	host := make(map[string]any, len(old.HostConfig))
	for k, v := range old.HostConfig {
		host[k] = v
	}
	host["NetworkMode"] = "container:" + parentID

	parked := name + "-neptide-old"
	if err := e.Rename(ctx, old.ID, parked); err != nil {
		return fmt.Errorf("park %s: %w", name, err)
	}
	// Its namespace is gone, so it is not serving anything; stop it so two
	// copies never run.
	if err := e.Stop(ctx, old.ID, stopTimeout); err != nil {
		_ = e.Rename(context.WithoutCancel(ctx), old.ID, name)
		return fmt.Errorf("stop %s: %w", name, err)
	}
	undo := func(cause error, newID string) error {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		var errs []error
		if newID != "" {
			errs = append(errs, e.Remove(rctx, newID))
		}
		errs = append(errs, e.Rename(rctx, old.ID, name))
		if err := errors.Join(errs...); err != nil {
			return fmt.Errorf("re-attaching %s to its network failed (%w), and so did cleaning up: %w", name, cause, err)
		}
		return fmt.Errorf("re-attaching %s to its network failed, it stays stopped: %w", name, cause)
	}
	newID, err := e.Create(ctx, name, config, host, nil)
	if err != nil {
		return undo(fmt.Errorf("create: %w", err), "")
	}
	if err := e.Start(ctx, newID); err != nil {
		return undo(fmt.Errorf("start: %w", err), newID)
	}
	if err := waitHealthy(ctx, e, newID, gate); err != nil {
		return undo(err, newID)
	}
	if err := e.Remove(ctx, old.ID); err != nil {
		return fmt.Errorf("re-attached %s, but removing its old container failed: %w", name, err)
	}
	return nil
}
