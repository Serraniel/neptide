// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"fmt"
	"strings"
	"time"
)

// LabelWindow restricts when updates are applied, e.g. "Sat,Sun 03:00-05:00"
// or "daily 02:00-04:00; Wed 12:00-13:00". Outside it, updates are reported
// as available and applied in a later cycle inside the window.
const LabelWindow = "neptide.window"

// Windows is a set of weekly maintenance windows. The zero value allows any
// time.
type Windows []window

type window struct {
	days       [7]bool // indexed by time.Weekday
	start, end int     // minutes since midnight; end < start wraps past midnight
	spec       string
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseWindows reads "DAYS HH:MM-HH:MM" entries separated by ";". DAYS is
// daily, a day (Mon), a range (Mon-Fri) or a list (Sat,Sun). Times are in
// the local time zone (TZ).
func ParseWindows(s string) (Windows, error) {
	var ws Windows
	for part := range strings.SplitSeq(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		days, times, ok := strings.Cut(part, " ")
		if !ok {
			return nil, fmt.Errorf("window %q: want DAYS HH:MM-HH:MM, e.g. Sat,Sun 03:00-05:00", part)
		}
		w := window{spec: part}
		if err := w.parseDays(strings.ToLower(days)); err != nil {
			return nil, fmt.Errorf("window %q: %w", part, err)
		}
		from, to, ok := strings.Cut(strings.TrimSpace(times), "-")
		var err1, err2 error
		w.start, err1 = clock(from)
		w.end, err2 = clock(to)
		if !ok || err1 != nil || err2 != nil || w.start == w.end {
			return nil, fmt.Errorf("window %q: want a time range like 03:00-05:00", part)
		}
		ws = append(ws, w)
	}
	return ws, nil
}

func (w *window) parseDays(s string) error {
	if s == "daily" || s == "*" {
		for i := range w.days {
			w.days[i] = true
		}
		return nil
	}
	for item := range strings.SplitSeq(s, ",") {
		from, to, isRange := strings.Cut(item, "-")
		a, ok1 := weekdays[from]
		b, ok2 := weekdays[to]
		if !ok1 || (isRange && !ok2) {
			return fmt.Errorf("unknown day %q (use mon … sun or daily)", item)
		}
		if !isRange {
			b = a
		}
		for d := a; ; d = (d + 1) % 7 {
			w.days[d] = true
			if d == b {
				break
			}
		}
	}
	return nil
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Open reports whether t lies in one of the windows. A window that wraps
// past midnight belongs to the day it starts on.
func (ws Windows) Open(t time.Time) bool {
	if len(ws) == 0 {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	for _, w := range ws {
		if w.start < w.end {
			if w.days[t.Weekday()] && m >= w.start && m < w.end {
				return true
			}
			continue
		}
		if (w.days[t.Weekday()] && m >= w.start) || (w.days[(t.Weekday()+6)%7] && m < w.end) {
			return true
		}
	}
	return false
}

func (ws Windows) String() string {
	specs := make([]string, len(ws))
	for i, w := range ws {
		specs[i] = w.spec
	}
	return strings.Join(specs, "; ")
}
