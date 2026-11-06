// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestAgainstKuma needs a fresh Uptime Kuma at KUMA_TEST_URL (for example
// http://127.0.0.1:13101, which test/integration/kuma.sh starts) and creates
// its admin account. NEPFLEET_TEST_KUMA_URL still works.
func TestAgainstKuma(t *testing.T) {
	url := cmp.Or(os.Getenv("KUMA_TEST_URL"), os.Getenv("NEPFLEET_TEST_KUMA_URL"))
	if url == "" {
		t.Skip("KUMA_TEST_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	debugFrames = func(s string) {
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		t.Log(s)
	}
	sctx, scancel := context.WithTimeout(ctx, 60*time.Second) // the first setup after DB init is slow
	defer scancel()
	const user, pass = "nepfleet", "test-password-1234"
	if err := Setup(sctx, url, user, pass, nil); err != nil {
		t.Logf("setup: %v (fine if already set up)", err)
	}

	k, err := Login(ctx, url, user, pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	// A monitor to put into maintenance.
	raw, err := k.c.call(ctx, "add", map[string]any{
		"type": "http", "name": "nepfleet-test", "url": "http://127.0.0.1:3001", "interval": 60,
		"retryInterval": 60, "maxretries": 0, "accepted_statuscodes": []string{"200-299"},
		"notificationIDList": map[string]bool{}, "conditions": []any{},
	})
	var added struct {
		OK        bool   `json:"ok"`
		Msg       string `json:"msg"`
		MonitorID int    `json:"monitorID"`
	}
	if err != nil || json.Unmarshal(raw, &added) != nil || !added.OK {
		t.Fatalf("add monitor: %s %v", raw, err)
	}

	mons, err := k.Monitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("monitors: %+v", mons)

	m, err := k.StartMaintenance(ctx, MaintenanceWindow{Title: "NepFleet: apply test", Description: "integration test", Monitors: []int{added.MonitorID}})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := k.MaintenanceMonitors(ctx, m)
	if err != nil || !slices.Contains(ids, added.MonitorID) {
		t.Fatalf("monitors in %s = %v, %v", m, ids, err)
	}
	if err := k.EndMaintenance(ctx, m); err != nil {
		t.Fatal(err)
	}

	// A scheduled window, as for announced host updates.
	start := time.Now().Add(24 * time.Hour)
	s, err := k.StartMaintenance(ctx, MaintenanceWindow{Title: "NepFleet: host update", Start: start, End: start.Add(30 * time.Minute), Monitors: []int{added.MonitorID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := k.EndMaintenance(ctx, s); err != nil {
		t.Fatal(err)
	}

	// The window Open creates: scheduled around now, so it is active at once
	// and expires by itself.
	now := time.Now()
	a, err := k.StartMaintenance(ctx, MaintenanceWindow{Title: "NepFleet: apply", Start: now.Add(-time.Minute), End: now.Add(10 * time.Minute), Monitors: []int{added.MonitorID}})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := k.Status(ctx, a); err != nil || st != "under-maintenance" {
		t.Fatalf("status of a window around now = %q, %v; want under-maintenance", st, err)
	}
	if err := k.EndMaintenance(ctx, a); err != nil {
		t.Fatal(err)
	}

	// A status page that shows the monitor gets an incident, resolved again.
	if raw, err := k.c.call(ctx, "addStatusPage", "NepFleet test", "nepfleet-test"); err != nil || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("addStatusPage: %s %v", raw, err)
	}
	config := map[string]any{"slug": "nepfleet-test", "title": "NepFleet test", "description": "", "autoRefreshInterval": 300, "theme": "auto",
		"showTags": false, "footerText": "", "customCSS": "", "showPoweredBy": true, "rssTitle": "", "showOnlyLastHeartbeat": false,
		"showCertificateExpiry": false, "analyticsId": nil, "analyticsScriptUrl": nil, "analyticsType": nil, "domainNameList": []string{}}
	groups := []map[string]any{{"name": "Services", "monitorList": []map[string]int{{"id": added.MonitorID}}}}
	if raw, err := k.c.call(ctx, "saveStatusPage", "nepfleet-test", config, "/icon.svg", groups); err != nil || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("saveStatusPage: %s %v", raw, err)
	}
	if ids, err := k.StatusPageMonitors(ctx, "nepfleet-test"); err != nil || !slices.Contains(ids, added.MonitorID) {
		t.Fatalf("status page monitors = %v, %v", ids, err)
	}
	target := Target{Name: "test", URL: url, Username: user, Password: pass}
	inc, warnings := OpenIncident(ctx, []Target{target}, []MonitorRef{{ID: "x", Name: "nepfleet-test"}}, "Rolling update", "web is updated one replica at a time", nil)
	t.Logf("open: %v", warnings)
	if len(inc.parts) != 1 {
		t.Fatalf("incidents posted: %d (%v)", len(inc.parts), warnings)
	}
	posted := inc.parts[0].id
	if d, err := k.statusPage(ctx, "nepfleet-test"); err != nil || !d.has(posted) {
		t.Fatalf("incident not shown: %+v %v", d, err)
	}
	// Another one next to it (a person's, say) stays when ours is resolved.
	other, err := k.PostIncident(ctx, "nepfleet-test", "Planned work", "by hand", StyleInfo)
	if err != nil {
		t.Fatal(err)
	}
	if w := inc.Close(ctx, nil); len(w) > 0 {
		t.Fatal(w)
	}
	d, err := k.statusPage(ctx, "nepfleet-test")
	if err != nil || d.has(posted) {
		t.Errorf("incident still shown after Close: %+v %v", d, err)
	}
	if d != nil && !d.has(other) {
		t.Errorf("Close resolved someone else's incident too: %+v", d)
	}
}
