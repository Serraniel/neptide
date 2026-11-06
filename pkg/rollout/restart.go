// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LabelRestart schedules restarts: a maintenance-window spec ("daily
// 04:00-05:00": once per window) or an interval ("6h": once the container
// has run that long). For containers that degrade over days.
const LabelRestart = "neptide.restart"

// RestartSchedule is a parsed LabelRestart.
type RestartSchedule struct {
	Window   Windows
	Interval time.Duration
	spec     string
}

// ParseRestart reads a LabelRestart value.
func ParseRestart(v string) (RestartSchedule, error) {
	v = strings.TrimSpace(v)
	if d, err := time.ParseDuration(v); err == nil {
		if d < 10*time.Minute {
			return RestartSchedule{}, fmt.Errorf("%s=%q: an interval below 10m would restart on most cycles", LabelRestart, v)
		}
		return RestartSchedule{Interval: d, spec: v}, nil
	}
	w, err := ParseWindows(v)
	if err != nil || len(w) == 0 {
		return RestartSchedule{}, fmt.Errorf("%s=%q: want an interval like 6h or a window like \"daily 04:00-05:00\"", LabelRestart, v)
	}
	return RestartSchedule{Window: w, spec: v}, nil
}

func (r RestartSchedule) String() string { return r.spec }

// Due reports whether a container that started at started should be
// restarted at now.
func (r RestartSchedule) Due(now, started time.Time) bool {
	if started.IsZero() {
		return false // unknown start: never restart blindly
	}
	if r.Interval > 0 {
		return now.Sub(started) >= r.Interval
	}
	if !r.Window.Open(now) {
		return false
	}
	// Once per window: only if it has not (re)started since the window opened.
	return started.Before(r.Window.openedAt(now))
}

// openedAt is the start of the window open at t (t must be inside one).
// Entries that touch (04:00-05:00; 05:00-06:00) count as one window; a
// union covering every minute stops the scan after two days.
func (ws Windows) openedAt(t time.Time) time.Time {
	s := t.Truncate(time.Minute)
	for i := 0; i < 2*24*60 && ws.Open(s.Add(-time.Minute)); i++ {
		s = s.Add(-time.Minute)
	}
	return s
}

// Restart stops and starts a container, then waits for the health gate.
func Restart(ctx context.Context, e Engine, id string, gate Gate) error {
	gate = gate.withDefaults()
	ct, err := e.Inspect(ctx, id)
	if err != nil {
		return err
	}
	stop := 10 * time.Second
	if s, ok := ct.Config["StopTimeout"].(float64); ok && s > 0 {
		stop = time.Duration(s) * time.Second
	}
	// Stop and start must both happen even if neptide is told to stop
	// meanwhile: a container stopped through the API stays down.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stop+time.Minute)
	defer cancel()
	if err := e.Stop(sctx, id, stop); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	if err := e.Start(sctx, id); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	return waitHealthy(ctx, e, id, gate)
}
