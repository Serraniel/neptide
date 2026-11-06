// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"fmt"
	"strings"
	"testing"
)

func TestMonitorNames(t *testing.T) {
	compose := []byte(`services:
  web:
    labels:
      caddy: x
      kuma.web.http.name: Web ${NEPFLEET_STACK}
      kuma.web.http.url: https://x
  db:
    labels: ["kuma.db.port.name=Database"]
  quiet:
    labels:
      kuma.q.http.name: Quiet
      nepfleet.maintenance: "false"
`)
	got := MonitorRefs(compose, map[string]string{"NEPFLEET_STACK": "shop"})
	var s []string
	for _, r := range got {
		s = append(s, r.ID+"="+r.Name)
	}
	if strings.Join(s, ",") != "db=Database,web=Web shop" {
		t.Errorf("got %v", s)
	}
}

func TestMatch(t *testing.T) {
	tag := func(v string) []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} {
		return []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{{Name: AutoKumaTag, Value: v}}
	}
	all := []Monitor{
		{ID: 1, Name: "Vault", Tags: tag("vaultwarden")},
		{ID: 2, Name: "Vault"}, // same name on another host, untagged
		{ID: 3, Name: "Logs"},  // unique name, untagged
		{ID: 4, Name: "Dup"}, {ID: 5, Name: "Dup"},
		{ID: 6, Name: "Media", Tags: tag("jellyfin-otherhost")}, // unique name, but another id
	}
	ids, amb := match(all, []MonitorRef{{"vaultwarden", "Vault"}, {"dozzle", "Logs"}, {"x", "Dup"}, {"gone", "Missing"}, {"jellyfin", "Media"}})
	if fmt.Sprint(ids) != "[1 3]" || fmt.Sprint(amb) != "[x]" {
		t.Errorf("ids=%v ambiguous=%v", ids, amb)
	}
}

func TestRefsFromLabels(t *testing.T) {
	got := RefsFromLabels(map[string]string{
		"kuma.web.http.name":    "Web",
		"kuma.web.http.url":     "https://x",
		"kuma.db.port.name":     "DB ${STACK}",
		"kuma.web.keyword.name": "Web again",
		"other":                 "x",
	}, map[string]string{"STACK": "s"})
	if len(got) != 2 || got[0].ID != "db" || got[0].Name != "DB s" || got[1].ID != "web" {
		t.Errorf("refs = %+v", got)
	}
	for _, l := range []string{OptOutLabel, NeptideOptOutLabel} {
		if refs := RefsFromLabels(map[string]string{"kuma.web.http.name": "Web", l: "false"}, nil); refs != nil {
			t.Errorf("opted out with %s: %+v", l, refs)
		}
	}
	tg := Target{Name: "k", URL: "https://k", Username: "u", Password: "s3cret"}
	if s := fmt.Sprintf("%v %+v %s", tg, tg, tg); strings.Contains(s, "s3cret") {
		t.Errorf("password printed: %s", s)
	}
}
