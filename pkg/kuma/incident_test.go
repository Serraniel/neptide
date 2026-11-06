// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"encoding/json"
	"testing"
)

// Status page JSON as Kuma 2.0.x and 2.1+ send it.
func TestStatusPageShapes(t *testing.T) {
	for name, c := range map[string]struct {
		raw            string
		single, shows7 bool
		has            int
	}{
		"2.0 without incident": {`{"incident":null,"publicGroupList":[{"monitorList":[{"id":7}]}]}`, true, true, 0},
		"2.0 with incident":    {`{"incident":{"id":3},"publicGroupList":[]}`, true, false, 3},
		"2.5 empty":            {`{"incidents":[],"publicGroupList":[{"monitorList":[{"id":1},{"id":7}]}]}`, false, true, 0},
		"2.5 with incidents":   {`{"incidents":[{"id":4},{"id":5}],"publicGroupList":[{"monitorList":[{"id":1}]}]}`, false, false, 5},
	} {
		var d statusPageData
		if err := json.Unmarshal([]byte(c.raw), &d); err != nil {
			t.Fatal(err)
		}
		if d.singleIncident() != c.single || shows(&d, []int{7}) != c.shows7 || (c.has != 0 && !d.has(c.has)) || d.has(99) {
			t.Errorf("%s: single=%v shows7=%v has(%d)=%v", name, d.singleIncident(), shows(&d, []int{7}), c.has, d.has(c.has))
		}
	}
}
