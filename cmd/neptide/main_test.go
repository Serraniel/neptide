// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"testing"
)

func TestCheckTZ(t *testing.T) {
	for _, ok := range []string{"", "UTC", "Europe/Berlin", ":Europe/Berlin"} {
		if err := checkTZ(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, err := os.Stat("/etc/localtime"); err == nil {
		for _, ok := range []string{"/etc/localtime", ":/etc/localtime"} {
			if err := checkTZ(ok); err != nil {
				t.Errorf("%q: %v", ok, err)
			}
		}
	}
	for _, bad := range []string{"Europe/Berln", "CET-1CEST,M3.5.0,M10.5.0/3", "/nonexistent/zone"} {
		if checkTZ(bad) == nil {
			t.Errorf("%q must be reported", bad)
		}
	}
}
