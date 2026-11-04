// SPDX-License-Identifier: AGPL-3.0-or-later

package version

import "testing"

func TestNext(t *testing.T) {
	tags := []string{"1.36.0", "1.37.4", "1.37.5", "1.38.0", "2.0.0", "2.0.0-rc1", "latest", "1.37.5-alpine"}
	for _, tc := range []struct {
		track        Track
		next, newest string
	}{
		{Digest, "", "2.0.0"},
		{Patch, "1.37.5", "2.0.0"},
		{Minor, "1.38.0", "2.0.0"},
		{Major, "2.0.0", "2.0.0"},
	} {
		next, newest := Next("1.37.4", tags, tc.track)
		if next != tc.next || newest != tc.newest {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.track, next, newest, tc.next, tc.newest)
		}
	}
}

func TestNextKeepsPrecisionAndScheme(t *testing.T) {
	// v11 should move to v12, not v12.0.1.
	if next, _ := Next("v11", []string{"v11.4.0", "v12", "v12.0.1", "v12.0"}, Major); next != "v12" {
		t.Errorf("got %q, want v12", next)
	}
	// Jellyfin publishes date tags next to semver tags.
	if _, newest := Next("10.11.11", []string{"2026110213", "10.11.12"}, Minor); newest != "10.11.12" {
		t.Errorf("date tag leaked in: newest=%q", newest)
	}
	// Suffixes must match.
	if next, _ := Next("18-alpine", []string{"19", "19-alpine", "19-bookworm"}, Major); next != "19-alpine" {
		t.Errorf("got %q, want 19-alpine", next)
	}
}

func TestUnversionedTag(t *testing.T) {
	if next, newest := Next("latest", []string{"1.0"}, Major); next != "" || newest != "" {
		t.Errorf("latest has no version line, got (%q, %q)", next, newest)
	}
}

func TestParseTrack(t *testing.T) {
	if tr, err := ParseTrack(""); err != nil || tr != Digest {
		t.Errorf("empty track = %v, %v", tr, err)
	}
	if _, err := ParseTrack("yolo"); err == nil {
		t.Error("expected error")
	}
}
