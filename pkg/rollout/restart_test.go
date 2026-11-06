// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"testing"
	"time"
)

func TestRestartSchedule(t *testing.T) {
	at := func(day, hh, mm int) time.Time { return time.Date(2026, 11, day, hh, mm, 0, 0, time.UTC) }
	every, err := ParseRestart("6h")
	if err != nil {
		t.Fatal(err)
	}
	if every.Due(at(5, 10, 0), at(5, 5, 0)) || !every.Due(at(5, 11, 0), at(5, 5, 0)) {
		t.Error("interval")
	}
	daily, err := ParseRestart("daily 04:00-05:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		now, started time.Time
		due          bool
	}{
		{at(5, 4, 10), at(3, 12, 0), true},  // in the window, running since before it
		{at(5, 4, 30), at(5, 4, 11), false}, // already restarted in this window
		{at(5, 12, 0), at(3, 12, 0), false}, // outside the window
	} {
		if got := daily.Due(c.now, c.started); got != c.due {
			t.Errorf("now %s started %s: due=%v", c.now.Format("02 15:04"), c.started.Format("02 15:04"), got)
		}
	}
	for _, bad := range []string{"1m", "sometimes", ""} {
		if _, err := ParseRestart(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}
