// SPDX-License-Identifier: AGPL-3.0-or-later

// Package metrics exposes update cycles in the Prometheus text format, with
// no client library: a handful of counters and gauges do not need one.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/neptide/internal/updater"
)

// Metrics accumulates what /metrics reports. The zero value is not usable;
// use New.
type Metrics struct {
	mu        sync.Mutex
	version   string
	events    map[updater.EventKind]uint64
	cycles    map[string]uint64 // ok | partial | error
	lastStart time.Time
	lastDur   time.Duration
	lastOK    time.Time
	pending   map[string]string // target → available | deferred
}

// New returns empty metrics for this build.
func New(version string) *Metrics {
	return &Metrics{version: version, events: map[updater.EventKind]uint64{}, cycles: map[string]uint64{}, pending: map[string]string{}}
}

// Cycle records one finished cycle.
func (m *Metrics) Cycle(start time.Time, events []updater.Event, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStart, m.lastDur = start, time.Since(start)
	failed := false
	for _, e := range events {
		failed = failed || e.Kind == updater.Failed || e.Kind == updater.RolledBack
	}
	// A cycle succeeds only if every target was handled: expired registry
	// credentials fail each target but not the cycle itself.
	result := "ok"
	switch {
	case err != nil:
		result = "error"
	case failed:
		result = "partial"
	default:
		m.lastOK = start
	}
	m.cycles[result]++
	pending := map[string]string{}
	for _, e := range events {
		m.events[e.Kind]++
		if e.Kind == updater.Available || e.Kind == updater.Deferred {
			pending[target(e)] = string(e.Kind)
		}
	}
	m.pending = pending
}

func target(e updater.Event) string {
	if e.Swarm {
		return "service/" + e.Target
	}
	return "container/" + e.Target
}

// kinds are always reported, so a rate() over them never starts at "no data".
var kinds = []updater.EventKind{updater.Available, updater.Deferred, updater.Restarted, updater.Beyond, updater.Updated, updater.RolledBack, updater.Failed}

// WriteTo writes the text exposition format.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	metric := func(name, typ, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	metric("neptide_build_info", "gauge", "Always 1; the version label says which neptide runs.")
	fmt.Fprintf(&b, "neptide_build_info{version=\"%s\"} 1\n", esc(m.version))

	metric("neptide_events_total", "counter", "Update events by kind since start.")
	for _, k := range kinds {
		fmt.Fprintf(&b, "neptide_events_total{kind=\"%s\"} %d\n", k, m.events[k])
	}
	metric("neptide_cycles_total", "counter", "Finished update cycles: ok, partial (some targets failed or rolled back) or error (could not list targets).")
	for _, r := range []string{"ok", "partial", "error"} {
		fmt.Fprintf(&b, "neptide_cycles_total{result=\"%s\"} %d\n", r, m.cycles[r])
	}
	metric("neptide_last_cycle_timestamp_seconds", "gauge", "Start of the last cycle (0 before the first).")
	fmt.Fprintf(&b, "neptide_last_cycle_timestamp_seconds %d\n", unix(m.lastStart))
	metric("neptide_last_success_timestamp_seconds", "gauge", "Start of the last cycle in which every target was checked and handled without failure.")
	fmt.Fprintf(&b, "neptide_last_success_timestamp_seconds %d\n", unix(m.lastOK))
	metric("neptide_last_cycle_duration_seconds", "gauge", "Duration of the last cycle.")
	fmt.Fprintf(&b, "neptide_last_cycle_duration_seconds %.3f\n", m.lastDur.Seconds())

	metric("neptide_updates_pending", "gauge", "Targets with an update found but not applied (notify mode or waiting for a maintenance window), as of the last cycle.")
	fmt.Fprintf(&b, "neptide_updates_pending %d\n", len(m.pending))
	metric("neptide_update_pending", "gauge", "1 for each target with an update not applied yet; reason is available (notify mode) or deferred (maintenance window).")
	names := make([]string, 0, len(m.pending))
	for t := range m.pending {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		kind, name, _ := strings.Cut(t, "/")
		fmt.Fprintf(&b, "neptide_update_pending{kind=\"%s\",name=\"%s\",reason=\"%s\"} 1\n", esc(kind), esc(name), m.pending[t])
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// Handler serves /metrics and /healthz.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = m.WriteTo(w)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

// esc escapes a label value the way the text format allows: only \\, \" and
// \n are valid escapes (Go's %q would produce others and break the scrape).
var esc = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace
