// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHooksFrom(t *testing.T) {
	h, err := HooksFrom(map[string]string{
		LabelHookPre:     "pg_dump -U app app > /backup/app.sql",
		LabelHookPost:    `["/app/migrate", "--check"]`,
		LabelHookTimeout: "10m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(h.Pre, []string{"sh", "-c", "pg_dump -U app app > /backup/app.sql"}) || !slices.Equal(h.Post, []string{"/app/migrate", "--check"}) || h.Timeout != 10*time.Minute {
		t.Errorf("hooks = %+v", h)
	}
	if h, _ := HooksFrom(nil); h.Pre != nil || h.Post != nil || h.Timeout != 5*time.Minute {
		t.Errorf("no labels: %+v", h)
	}
	if h, err := HooksFrom(map[string]string{LabelHookPre: "[ -d /data ] && touch /data/x"}); err != nil || h.Pre[0] != "sh" {
		t.Errorf("a shell test command is not JSON: %+v %v", h, err)
	}
	if got := killable([]string{"sh", "-c", "pg_dump > /x"}, 90*time.Second); len(got) != 4 || got[3] != "pg_dump > /x" || !strings.Contains(got[2], "timeout -s KILL 90") {
		t.Errorf("killable = %q", got)
	}
	if h, err := HooksFrom(map[string]string{LabelHookPre: `[ "/app/backup" ]`}); err != nil || !slices.Equal(h.Pre, []string{"/app/backup"}) {
		t.Errorf("spaced exec form: %+v %v", h, err)
	}
	if got := killable([]string{"sh", "-c", "x"}, 500*time.Millisecond); !strings.Contains(got[2], "KILL 1 sh") {
		t.Errorf("sub-second timeout must round up: %q", got[2])
	}
	if got := killable([]string{"/backup"}, time.Minute); len(got) != 1 {
		t.Errorf("exec form must not be wrapped: %q", got)
	}
	for _, bad := range []map[string]string{{LabelHookPost: `["unterminated`}, {LabelHookPre: `[""]`}, {LabelHookPre: `[]`}, {LabelHookPre: `[ "a", 1 ]`}, {LabelHookTimeout: "soon"}} {
		if _, err := HooksFrom(bad); err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
}
