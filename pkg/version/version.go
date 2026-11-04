// SPDX-License-Identifier: AGPL-3.0-or-later

// Package version compares image tags that carry version numbers, such as
// 1.37.5, v11, 18-alpine or qemu-v10.2.3, and decides which newer tag an
// update policy ("track") may move to.
package version

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Track is the widest change an update may make on its own.
type Track string

const (
	// Digest keeps the tag and only follows its digest (rebuilds, patch tags like :18).
	Digest Track = "digest"
	// Patch moves to newer tags with the same major and minor.
	Patch Track = "patch"
	// Minor moves to newer tags with the same major.
	Minor Track = "minor"
	// Major moves to any newer tag.
	Major Track = "major"
)

// ParseTrack validates a track name. Empty means Digest.
func ParseTrack(s string) (Track, error) {
	switch t := Track(strings.ToLower(strings.TrimSpace(s))); t {
	case "":
		return Digest, nil
	case Digest, Patch, Minor, Major:
		return t, nil
	}
	return "", fmt.Errorf("unknown track %q (digest, patch, minor, major)", s)
}

// Tag is a tag split into prefix, numeric components and suffix.
type Tag struct {
	Raw    string
	Prefix string
	Nums   []int
	Suffix string
}

var tagRe = regexp.MustCompile(`^([^\d]*)(\d+(?:\.\d+)*)(.*)$`)

// Parse splits a tag. ok is false for tags without a version (latest, stable).
func Parse(tag string) (Tag, bool) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return Tag{}, false
	}
	parts := strings.Split(m[2], ".")
	nums := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return Tag{}, false
		}
		nums[i] = n
	}
	return Tag{Raw: tag, Prefix: m[1], Nums: nums, Suffix: m[3]}, true
}

// Compare orders tags by their numbers; missing components count as 0.
func Compare(a, b Tag) int {
	for i := range max(len(a.Nums), len(b.Nums)) {
		x, y := at(a.Nums, i), at(b.Nums, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func at(n []int, i int) int {
	if i < len(n) {
		return n[i]
	}
	return 0
}

// Candidates returns every tag in tags that belongs to the same release line
// as current: same prefix and suffix, at least as many components (so date
// tags like 2026110213 never compete with 10.11.11), and newer.
// The result is sorted oldest first.
func Candidates(current string, tags []string) []Tag {
	cur, ok := Parse(current)
	if !ok {
		return nil
	}
	var out []Tag
	for _, t := range tags {
		v, ok := Parse(t)
		if !ok || v.Prefix != cur.Prefix || v.Suffix != cur.Suffix || len(v.Nums) < len(cur.Nums) {
			continue
		}
		if Compare(v, cur) > 0 {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b Tag) int {
		if c := Compare(a, b); c != 0 {
			return c
		}
		return len(a.Nums) - len(b.Nums)
	})
	return out
}

// Allowed reports whether moving from current to next stays inside track.
func Allowed(current, next Tag, track Track) bool {
	switch track {
	case Major:
		return true
	case Minor:
		return at(next.Nums, 0) == at(current.Nums, 0)
	case Patch:
		return at(next.Nums, 0) == at(current.Nums, 0) && at(next.Nums, 1) == at(current.Nums, 1)
	}
	return false
}

// Next picks the newest tag track allows, preferring the one with the same
// precision as current (v11 → v12, not v12.0.1). It also returns the newest
// tag overall, which is "beyond track" when it differs.
func Next(current string, tags []string, track Track) (next, newest string) {
	cur, ok := Parse(current)
	if !ok {
		return "", ""
	}
	cands := Candidates(current, tags)
	if len(cands) == 0 {
		return "", ""
	}
	newest = cands[len(cands)-1].Raw
	if track == Digest {
		return "", newest
	}
	// cands is sorted ascending, so the last match wins. A floating tag of the
	// same precision (v12 for v11) beats a more precise one (v12.0.1): it keeps
	// receiving fixes without another tag move.
	var anyPrec, samePrec string
	for _, c := range cands {
		if !Allowed(cur, c, track) {
			continue
		}
		anyPrec = c.Raw
		if len(c.Nums) == len(cur.Nums) {
			samePrec = c.Raw
		}
	}
	if samePrec != "" {
		return samePrec, newest
	}
	return anyPrec, newest
}
