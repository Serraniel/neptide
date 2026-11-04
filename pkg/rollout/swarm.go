// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"context"
	"fmt"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
)

// safeUpdateConfig is applied when a service has no update_config: one task
// at a time, watch it for 30 s, roll back on failure. stop-first, because
// start-first breaks single-writer services (SQLite, most databases).
var safeUpdateConfig = map[string]any{
	"Parallelism":     1,
	"FailureAction":   "rollback",
	"Monitor":         int64(30 * time.Second),
	"MaxFailureRatio": 0,
	"Order":           "stop-first",
}

// UpdateService moves a swarm service to newRef (name:tag@digest) and waits
// for swarm to finish. Swarm itself performs the health-gated rolling update
// and the rollback; this function makes sure one is configured and reports
// the outcome.
func UpdateService(ctx context.Context, e Engine, id, newRef string, auth *engine.RegistryAuth, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	svc, err := e.InspectService(ctx, id)
	if err != nil {
		return err
	}
	if svc.Image() == newRef {
		return nil
	}
	if _, ok := svc.Spec["UpdateConfig"]; !ok {
		svc.Spec["UpdateConfig"] = safeUpdateConfig
	}
	svc.SetImage(newRef)
	if err := e.UpdateService(ctx, svc, auth); err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	quiet := 0 // consecutive polls without an UpdateStatus
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		cur, err := e.InspectService(ctx, id)
		if err != nil {
			return err
		}
		if cur.UpdateStatus != nil {
			switch cur.UpdateStatus.State {
			case "completed":
				return nil
			case "rollback_completed":
				return fmt.Errorf("%w by swarm: %s", ErrRolledBack, cur.UpdateStatus.Message)
			case "paused", "rollback_paused":
				return fmt.Errorf("swarm update %s: %s", cur.UpdateStatus.State, cur.UpdateStatus.Message)
			}
		} else if cur.Image() == newRef {
			// Services without running tasks finish without an UpdateStatus;
			// right after the update call it may also not be set yet.
			if quiet++; quiet >= 5 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("swarm update still running after %s", timeout)
		}
	}
}
