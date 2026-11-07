// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"context"
	"fmt"
	"slices"
)

// DefaultStatusPage is the slug of the status page every maintenance window
// shows on, whichever monitors it covers.
const DefaultStatusPage = "maintenance"

// EnsureStatusPage creates a status page unless one with the slug exists,
// leaving an existing page as it is. Kuma sends its list of pages once per
// login, so a page created earlier in the same session is not seen here.
func (k *Client) EnsureStatusPage(ctx context.Context, title, slug string) error {
	pages, err := k.StatusPages(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(pages, func(p StatusPage) bool { return p.Slug == slug }) {
		return nil
	}
	raw, err := k.c.call(ctx, "addStatusPage", title, slug)
	if _, err := decodeResult("addStatusPage", raw, err); err != nil {
		return fmt.Errorf("status page %s: %w", slug, err)
	}
	return nil
}

// pagesFor are the status pages a window should show on: the default page
// and those showing one of its monitors. A page that cannot be read is
// skipped: the window matters more than where it shows.
func (k *Client) pagesFor(ctx context.Context, monitors []int) []int {
	pages, err := k.StatusPages(ctx)
	if err != nil {
		return nil
	}
	var out []int
	for _, p := range pages {
		if p.Slug == DefaultStatusPage {
			out = append(out, p.ID)
			continue
		}
		shown, err := k.StatusPageMonitors(ctx, p.Slug)
		if err == nil && slices.ContainsFunc(shown, func(id int) bool { return slices.Contains(monitors, id) }) {
			out = append(out, p.ID)
		}
	}
	return out
}
