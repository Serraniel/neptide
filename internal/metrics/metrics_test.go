// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/neptide/internal/updater"
)

func TestMetrics(t *testing.T) {
	m := New("v1.2.3")
	start := time.Unix(1_800_000_000, 0)
	m.Cycle(start, []updater.Event{
		{Kind: updater.Available, Target: "web"},
		{Kind: updater.Available, Target: "api", Swarm: true},
		{Kind: updater.Updated, Target: "db"},
	}, nil)
	m.Cycle(start.Add(time.Hour), []updater.Event{{Kind: updater.Available, Target: "web"}}, errors.New("swarm info"))

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`neptide_build_info{version="v1.2.3"} 1`,
		`neptide_events_total{kind="available"} 3`,
		`neptide_events_total{kind="updated"} 1`,
		`neptide_events_total{kind="failed"} 0`,
		`neptide_cycles_total{result="ok"} 1`,
		`neptide_cycles_total{result="error"} 1`,
		`neptide_cycles_total{result="partial"} 0`,
		`neptide_last_cycle_timestamp_seconds 1800003600`,
		`neptide_last_success_timestamp_seconds 1800000000`,
		`neptide_updates_pending 1`,
		`neptide_update_pending{kind="container",name="web"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, `name="api"`) {
		t.Error("pending updates must reflect the last cycle only")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}
}

func TestFailedTargetsAreNoSuccess(t *testing.T) {
	m := New("v\t1\"x")
	m.Cycle(time.Unix(100, 0), nil, nil)
	m.Cycle(time.Unix(200, 0), []updater.Event{{Kind: updater.Failed, Target: "web"}}, nil)
	var b strings.Builder
	_, _ = m.WriteTo(&b)
	for _, want := range []string{`neptide_cycles_total{result="partial"} 1`, "neptide_last_success_timestamp_seconds 100", `version="v` + "\t" + `1\"x"`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in\n%s", want, b.String())
		}
	}
}
