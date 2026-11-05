// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"testing"
	"time"
)

func TestWindows(t *testing.T) {
	at := func(day time.Weekday, hhmm string) time.Time {
		c, _ := time.Parse("15:04", hhmm)
		// 2026-11-01 is a Sunday.
		return time.Date(2026, 11, 1+int(day), c.Hour(), c.Minute(), 0, 0, time.UTC)
	}
	for _, c := range []struct {
		spec string
		t    time.Time
		open bool
	}{
		{"", at(time.Monday, "12:00"), true},
		{"Sat,Sun 03:00-05:00", at(time.Sunday, "04:59"), true},
		{"Sat,Sun 03:00-05:00", at(time.Sunday, "05:00"), false},
		{"Sat,Sun 03:00-05:00", at(time.Monday, "04:00"), false},
		{"mon-fri 22:00-02:00", at(time.Friday, "23:30"), true},
		{"mon-fri 22:00-02:00", at(time.Saturday, "01:00"), true}, // Friday's window
		{"mon-fri 22:00-02:00", at(time.Monday, "01:00"), false},  // Sunday has none
		{"fri-mon 10:00-11:00", at(time.Sunday, "10:30"), true},   // range wraps the week
		{"daily 02:00-04:00; Wed 12:00-13:00", at(time.Wednesday, "12:15"), true},
		{"daily 02:00-04:00; Wed 12:00-13:00", at(time.Thursday, "12:15"), false},
	} {
		ws, err := ParseWindows(c.spec)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		if got := ws.Open(c.t); got != c.open {
			t.Errorf("%q at %s: open=%v, want %v", c.spec, c.t.Format("Mon 15:04"), got, c.open)
		}
	}
	for _, bad := range []string{"Sat", "someday 03:00-04:00", "Sat 03:00", "Sat 3am-4am", "Sat 03:00-03:00"} {
		if _, err := ParseWindows(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestWindowsNext(t *testing.T) {
	ws, _ := ParseWindows("Sat,Sun 03:00-05:00")
	mon := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) // Monday
	if got, want := ws.Next(mon), time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("next = %s, want %s", got, want)
	}
	open := time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC) // Sunday, inside
	if got := ws.Next(open); !got.Equal(open) {
		t.Errorf("open window: next = %s", got)
	}
	if got := (Windows(nil)).Next(mon); !got.Equal(mon) {
		t.Errorf("no window: next = %s", got)
	}
	// Across a DST change the wall-clock start stays 03:00.
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	fri := time.Date(2026, 10, 23, 12, 0, 0, 0, berlin)
	if got := ws.Next(fri); got.Hour() != 3 || got.Weekday() != time.Saturday {
		t.Errorf("next after DST = %s", got)
	}
}

func TestEmptyWindowLabelLiftsDefault(t *testing.T) {
	def, _ := ParseWindows("Sun 03:00-04:00")
	p, err := PolicyFrom(map[string]string{LabelWindow: ""}, Policy{Window: def})
	if err != nil || len(p.Window) != 0 {
		t.Errorf("empty label: %v %v", p.Window, err)
	}
	if ws, err := ParseWindows(" ; "); err != nil || len(ws) != 0 {
		t.Errorf("blank spec must mean any time: %v %v", ws, err)
	}
	if _, err := PolicyFrom(map[string]string{LabelWindow: "Sun 3-4"}, Policy{}); err == nil {
		t.Error("invalid label must be an error")
	}
}
