// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client is a logged-in Uptime Kuma session.
type Client struct {
	c    *conn
	base string
	hc   *http.Client
}

// Login connects and authenticates. Close the client when done.
func Login(ctx context.Context, url, username, password string, hc *http.Client) (*Client, error) {
	c, err := dial(ctx, url, hc)
	if err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, "login", map[string]string{"username": username, "password": password, "token": ""})
	if _, err := decodeResult("login", raw, err); err != nil {
		c.close()
		return nil, err
	}
	return &Client{c: c, base: strings.TrimRight(url, "/"), hc: hc}, nil
}

// Setup creates the first admin account of a fresh Kuma (first-run setup).
func Setup(ctx context.Context, url, username, password string, hc *http.Client) error {
	c, err := dial(ctx, url, hc)
	if err != nil {
		return err
	}
	defer c.close()
	raw, err := c.call(ctx, "setup", username, password)
	_, err = decodeResult("setup", raw, err)
	return err
}

// Close ends the session.
func (k *Client) Close() { k.c.close() }

// Monitor is the part of a Kuma monitor NepFleet needs.
type Monitor struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Tags []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"tags"`
}

// Monitors returns every monitor (pushed by Kuma after login).
func (k *Client) Monitors(ctx context.Context) ([]Monitor, error) {
	raw, err := k.c.event(ctx, "monitorList")
	if err != nil {
		return nil, err
	}
	var byID map[string]Monitor
	if err := json.Unmarshal(raw, &byID); err != nil {
		return nil, fmt.Errorf("monitorList: %w", err)
	}
	out := make([]Monitor, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// StatusPage is a Kuma status page.
type StatusPage struct {
	ID    int    `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// StatusPages returns every status page.
func (k *Client) StatusPages(ctx context.Context) ([]StatusPage, error) {
	raw, err := k.c.event(ctx, "statusPageList")
	if err != nil {
		return nil, err
	}
	var byID map[string]StatusPage
	if err := json.Unmarshal(raw, &byID); err != nil {
		return nil, fmt.Errorf("statusPageList: %w", err)
	}
	out := make([]StatusPage, 0, len(byID))
	for _, p := range byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// MaintenanceWindow describes a maintenance window to create.
type MaintenanceWindow struct {
	Title       string
	Description string
	// Start and End set a scheduled ("single") window; zero values make a
	// manual window that is active until it is ended.
	Start, End  time.Time
	Monitors    []int
	StatusPages []int
}

// Maintenance is a created window.
type Maintenance struct {
	ID int
}

// StartMaintenance creates a maintenance window and attaches monitors and
// status pages. For a manual window it is active immediately.
func (k *Client) StartMaintenance(ctx context.Context, w MaintenanceWindow) (Maintenance, error) {
	m := map[string]any{
		"title":           w.Title,
		"description":     w.Description,
		"strategy":        "manual",
		"active":          true,
		"intervalDay":     1,
		"dateRange":       []any{},
		"timeRange":       []map[string]int{{"hours": 0, "minutes": 0}, {"hours": 0, "minutes": 0}},
		"weekdays":        []int{},
		"daysOfMonth":     []int{},
		"timezoneOption":  "UTC",
		"cron":            "30 3 * * *",
		"durationMinutes": 60,
	}
	if !w.Start.IsZero() {
		const layout = "2006-01-02 15:04:05"
		m["strategy"] = "single"
		m["dateRange"] = []string{w.Start.UTC().Format(layout), w.End.UTC().Format(layout)}
	}
	raw, err := k.c.call(ctx, "addMaintenance", m)
	r, err := decodeResult("addMaintenance", raw, err)
	if err != nil {
		return Maintenance{}, err
	}
	out := Maintenance{ID: r.MaintenanceID}
	if len(w.Monitors) > 0 {
		ids := make([]map[string]int, len(w.Monitors))
		for i, id := range w.Monitors {
			ids[i] = map[string]int{"id": id}
		}
		raw, err := k.c.call(ctx, "addMonitorMaintenance", out.ID, ids)
		if _, err := decodeResult("addMonitorMaintenance", raw, err); err != nil {
			k.cleanup(ctx, out)
			return Maintenance{}, err
		}
	}
	if len(w.StatusPages) > 0 {
		ids := make([]map[string]int, len(w.StatusPages))
		for i, id := range w.StatusPages {
			ids[i] = map[string]int{"id": id}
		}
		raw, err := k.c.call(ctx, "addMaintenanceStatusPage", out.ID, ids)
		if _, err := decodeResult("addMaintenanceStatusPage", raw, err); err != nil {
			k.cleanup(ctx, out)
			return Maintenance{}, err
		}
	}
	return out, nil
}

// cleanup removes a half-created window with its own deadline: the
// caller's context may be what just expired.
func (k *Client) cleanup(ctx context.Context, m Maintenance) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	_ = k.EndMaintenance(cctx, m)
}

// Status returns Kuma's status of a window (e.g. "under-maintenance", "scheduled").
func (k *Client) Status(ctx context.Context, m Maintenance) (string, error) {
	raw, err := k.c.call(ctx, "getMaintenance", m.ID)
	if err != nil {
		return "", err
	}
	var r struct {
		OK          bool   `json:"ok"`
		Msg         string `json:"msg"`
		Maintenance struct {
			Status string `json:"status"`
		} `json:"maintenance"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("getMaintenance: %w", err)
	}
	if !r.OK {
		return "", fmt.Errorf("getMaintenance: %s", r.Msg)
	}
	return r.Maintenance.Status, nil
}

// EndMaintenance removes the window; monitors return to normal immediately.
func (k *Client) EndMaintenance(ctx context.Context, m Maintenance) error {
	raw, err := k.c.call(ctx, "deleteMaintenance", m.ID)
	_, err = decodeResult("deleteMaintenance", raw, err)
	return err
}

// MaintenanceMonitors lists the monitor IDs attached to a window.
func (k *Client) MaintenanceMonitors(ctx context.Context, m Maintenance) ([]int, error) {
	raw, err := k.c.call(ctx, "getMonitorMaintenance", m.ID)
	if err != nil {
		return nil, err
	}
	var r struct {
		OK       bool      `json:"ok"`
		Msg      string    `json:"msg"`
		Monitors []Monitor `json:"monitors"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("getMonitorMaintenance: %w", err)
	}
	if !r.OK {
		return nil, fmt.Errorf("getMonitorMaintenance: %s", r.Msg)
	}
	ids := make([]int, len(r.Monitors))
	for i, mon := range r.Monitors {
		ids[i] = mon.ID
	}
	return ids, nil
}

// String renders a maintenance reference.
func (m Maintenance) String() string { return "maintenance #" + strconv.Itoa(m.ID) }
