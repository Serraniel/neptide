// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

// statusPageData is the part of /api/status-page/<slug> used here. Kuma
// 2.0.x shows one pinned incident (incident), 2.1+ a list (incidents).
type statusPageData struct {
	Incident        *struct{ ID int }   `json:"incident"`
	Incidents       *[]struct{ ID int } `json:"incidents"`
	PublicGroupList []struct {
		MonitorList []struct {
			ID int `json:"id"`
		} `json:"monitorList"`
	} `json:"publicGroupList"`
}

func (k *Client) statusPage(ctx context.Context, slug string) (*statusPageData, error) {
	// Kuma caches this endpoint for minutes, keyed by URL: a fresh query
	// string sees incidents and monitors as they are now.
	u := k.base + "/api/status-page/" + url.PathEscape(slug) + "?nepfleet=" + strconv.FormatInt(time.Now().UnixNano(), 36)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil) //nolint:gosec // the Kuma URL the operator configured

	if err != nil {
		return nil, err
	}
	hc := k.hc
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req) //nolint:gosec // the Kuma URL the operator configured
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status page %s: HTTP %d", slug, res.StatusCode)
	}
	var d statusPageData
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&d); err != nil {
		return nil, fmt.Errorf("status page %s: %w", slug, err)
	}
	return &d, nil
}

// StatusPageMonitors returns the IDs of the monitors a status page shows.
func (k *Client) StatusPageMonitors(ctx context.Context, slug string) ([]int, error) {
	d, err := k.statusPage(ctx, slug)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, g := range d.PublicGroupList {
		for _, m := range g.MonitorList {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// Incident styles Kuma shows.
const (
	StyleInfo    = "info"
	StyleWarning = "warning"
	StyleDanger  = "danger"
)

// PostIncident pins a new incident on a status page and returns its ID.
func (k *Client) PostIncident(ctx context.Context, slug, title, content, style string) (int, error) {
	raw, err := k.c.call(ctx, "postIncident", slug, map[string]string{"title": title, "content": content, "style": style})
	if err != nil {
		return 0, err
	}
	var r struct {
		OK       bool   `json:"ok"`
		Msg      string `json:"msg"`
		Incident struct {
			ID int `json:"id"`
		} `json:"incident"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, fmt.Errorf("postIncident: %w", err)
	}
	if !r.OK {
		return 0, fmt.Errorf("postIncident: %s", r.Msg)
	}
	return r.Incident.ID, nil
}

// ResolveIncident resolves one incident (Uptime Kuma 2.1+), leaving any
// other incident on the page alone.
func (k *Client) ResolveIncident(ctx context.Context, slug string, id int) error {
	raw, err := k.c.call(ctx, "resolveIncident", slug, id)
	_, err = decodeResult("resolveIncident", raw, err)
	return err
}

// Incident is a set of status-page incidents posted by OpenIncident.
type Incident struct {
	parts []postedIncident
}

type postedIncident struct {
	target Target
	slug   string
	id     int
}

// OpenIncident posts a warning incident on every status page, in every
// target, that shows one of the monitors: for changes that degrade a service
// without taking it down (a rolling update of several replicas). Needs
// Uptime Kuma 2.1+, where a page lists several incidents (one written by a
// person stays visible) and one can be resolved. Like Open, problems are
// warnings, never errors.
func OpenIncident(ctx context.Context, targets []Target, monitors []MonitorRef, title, content string, hc *http.Client) (*Incident, []string) {
	inc := &Incident{}
	var warnings []string
	for _, t := range targets {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := func() (int, error) {
			k, err := Login(tctx, t.URL, t.Username, t.Password, hc)
			if err != nil {
				return 0, err
			}
			defer k.Close()
			all, err := k.Monitors(tctx)
			if err != nil {
				return 0, err
			}
			ids, ambiguous := match(all, monitors)
			for _, a := range ambiguous {
				warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: monitor %q is ambiguous (several monitors match), no incident for it", t.Name, a))
			}
			if len(ids) == 0 {
				return 0, nil
			}
			pages, err := k.StatusPages(tctx)
			if err != nil {
				return 0, err
			}
			posted := 0
			for _, p := range pages {
				d, err := k.statusPage(tctx, p.Slug)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: %v", t.Name, err))
					continue
				}
				if !shows(d, ids) {
					continue
				}
				if d.singleIncident() {
					warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: status page %s: incidents need Uptime Kuma 2.1 or newer, none posted", t.Name, p.Slug))
					continue
				}
				id, err := k.PostIncident(tctx, p.Slug, title, content, StyleWarning)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: no incident on %s (%v)", t.Name, p.Slug, err))
					continue
				}
				inc.parts = append(inc.parts, postedIncident{target: t, slug: p.Slug, id: id})
				posted++
			}
			return posted, nil
		}()
		cancel()
		switch {
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: no incident (%v)", t.Name, err))
		case n > 0:
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: incident on %d status page(s)", t.Name, n))
		}
	}
	return inc, warnings
}

// singleIncident: Kuma 2.0.x, which shows one incident per page and cannot
// resolve a single incident (resolveIncident came with 2.1), so NepFleet
// does not post there: it could neither leave a person's incident visible
// nor remove its own cleanly.
func (d *statusPageData) singleIncident() bool { return d.Incidents == nil }

// has reports whether incident id is shown.
func (d *statusPageData) has(id int) bool {
	if d.Incident != nil && d.Incident.ID == id {
		return true
	}
	if d.Incidents != nil {
		for _, i := range *d.Incidents {
			if i.ID == id {
				return true
			}
		}
	}
	return false
}

func shows(d *statusPageData, ids []int) bool {
	for _, g := range d.PublicGroupList {
		for _, m := range g.MonitorList {
			if slices.Contains(ids, m.ID) {
				return true
			}
		}
	}
	return false
}

// Close resolves the incidents that were posted.
func (i *Incident) Close(ctx context.Context, hc *http.Client) []string {
	var warnings []string
	for _, p := range i.parts {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := func() error {
			k, err := Login(tctx, p.target.URL, p.target.Username, p.target.Password, hc)
			if err != nil {
				return err
			}
			defer k.Close()
			return k.ResolveIncident(tctx, p.slug, p.id)
		}()
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: could not resolve the incident on %s, resolve it in the UI (%v)", p.target.Name, p.slug, err))
		}
	}
	return warnings
}
