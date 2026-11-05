// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"slices"
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
	for _, bad := range []map[string]string{{LabelHookPost: `["unterminated`}, {LabelHookPre: `[]`}, {LabelHookTimeout: "soon"}} {
		if _, err := HooksFrom(bad); err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
}
