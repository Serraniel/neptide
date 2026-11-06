// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// OptOutLabel on a service keeps its monitors out of maintenance windows.
const OptOutLabel = "nepfleet.maintenance"

// MonitorRef identifies a stack's monitor: ID is the AutoKuma id (the
// <id> in kuma.<id>.<type>.name), Name its display name.
type MonitorRef struct{ ID, Name string }

// AutoKumaTag is the tag AutoKuma puts on monitors it manages, with the
// label id as value.
const AutoKumaTag = "AutoKuma"

// MonitorRefs returns the AutoKuma monitors a rendered compose file declares,
// with ${VAR} in names taken from env.
func MonitorRefs(compose []byte, env map[string]string) []MonitorRef {
	var doc struct {
		Services map[string]struct {
			Labels any `yaml:"labels"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &doc) != nil {
		return nil
	}
	var out []MonitorRef
	for _, svc := range doc.Services {
		out = append(out, RefsFromLabels(labelMap(svc.Labels), env)...)
	}
	return dedup(out)
}

// RefsFromLabels returns the AutoKuma monitors one container or service
// declares in its labels (kuma.<id>.<type>.name), unless it opts out.
// ${VAR} in names is taken from env; labels of a running container are
// already interpolated, so env may be nil there.
func RefsFromLabels(labels map[string]string, env map[string]string) []MonitorRef {
	if labels[OptOutLabel] == "false" {
		return nil
	}
	var out []MonitorRef
	for k, v := range labels {
		parts := strings.Split(k, ".")
		if len(parts) == 4 && parts[0] == "kuma" && parts[3] == "name" {
			out = append(out, MonitorRef{ID: parts[1], Name: os.Expand(v, func(n string) string { return env[n] })})
		}
	}
	return dedup(out)
}

func dedup(refs []MonitorRef) []MonitorRef {
	seen := map[string]bool{}
	var out []MonitorRef
	for _, r := range refs {
		if !seen[r.ID] {
			seen[r.ID] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// match finds Kuma monitors for refs: by AutoKuma tag value, else by a name
// that exactly one monitor has. Ambiguous names are skipped and reported, so
// another host's monitor with the same name is never silenced.
func match(all []Monitor, refs []MonitorRef) (ids []int, ambiguous []string) {
	byTag := map[string][]int{}
	byName := map[string][]int{} // only monitors AutoKuma does not manage
	for _, m := range all {
		tagged := false
		for _, t := range m.Tags {
			if t.Name == AutoKumaTag && t.Value != "" {
				byTag[t.Value] = append(byTag[t.Value], m.ID)
				tagged = true
			}
		}
		// A monitor with an AutoKuma id belongs to that id; matching it by
		// name could silence another host's monitor.
		if !tagged {
			byName[m.Name] = append(byName[m.Name], m.ID)
		}
	}
	for _, r := range refs {
		switch {
		case len(byTag[r.ID]) == 1:
			ids = append(ids, byTag[r.ID][0])
		case len(byTag[r.ID]) == 0 && len(byName[r.Name]) == 1:
			ids = append(ids, byName[r.Name][0])
		case len(byTag[r.ID]) > 1 || len(byName[r.Name]) > 1:
			ambiguous = append(ambiguous, r.ID)
		}
	}
	return ids, ambiguous
}

func labelMap(labels any) map[string]string {
	out := map[string]string{}
	switch l := labels.(type) {
	case map[string]any:
		for k, v := range l {
			out[k] = fmt.Sprint(v)
		}
	case []any:
		for _, item := range l {
			if s, ok := item.(string); ok {
				k, v, _ := strings.Cut(s, "=")
				out[k] = v
			}
		}
	}
	return out
}

// Target is one Kuma instance with credentials.
type Target struct {
	Name, URL, Username, Password string
}

// Window is an open maintenance window across instances.
type Window struct {
	parts []open
}

type open struct {
	target Target
	m      Maintenance
}

// Open puts the named monitors into a manual maintenance window in every
// target that has them. Problems are returned as warnings, never as errors:
// a monitoring outage must not block a deployment.
// The window is scheduled from a minute ago until maxDuration from now, so it
// expires on its own even if NepFleet is killed before it can close it.
func Open(ctx context.Context, targets []Target, monitors []MonitorRef, title, description string, maxDuration time.Duration, hc *http.Client) (*Window, []string) {
	w := &Window{}
	var warnings []string
	for _, t := range targets {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ids, err := func() ([]int, error) {
			k, err := Login(tctx, t.URL, t.Username, t.Password, hc)
			if err != nil {
				return nil, err
			}
			defer k.Close()
			all, err := k.Monitors(tctx)
			if err != nil {
				return nil, err
			}
			ids, ambiguous := match(all, monitors)
			for _, a := range ambiguous {
				warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: monitor %q is ambiguous (several monitors match), not put into maintenance", t.Name, a))
			}
			if len(ids) == 0 {
				return nil, nil
			}
			now := time.Now()
			m, err := k.StartMaintenance(tctx, MaintenanceWindow{Title: title, Description: description, Monitors: ids,
				Start: now.Add(-time.Minute), End: now.Add(maxDuration)})
			if err != nil {
				return nil, err
			}
			w.parts = append(w.parts, open{target: t, m: m})
			return ids, nil
		}()
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: no maintenance window (%v)", t.Name, err))
		} else if ids != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: %d monitor(s) in maintenance", t.Name, len(ids)))
		}
	}
	return w, warnings
}

// Close ends every window that was opened.
func (w *Window) Close(ctx context.Context, hc *http.Client) []string {
	var warnings []string
	for _, p := range w.parts {
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := func() error {
			k, err := Login(tctx, p.target.URL, p.target.Username, p.target.Password, hc)
			if err != nil {
				return err
			}
			defer k.Close()
			return k.EndMaintenance(tctx, p.m)
		}()
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Uptime Kuma %s: could not end %s, end it in the UI (%v)", p.target.Name, p.m, err))
		}
	}
	return warnings
}
