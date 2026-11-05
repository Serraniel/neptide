// SPDX-License-Identifier: AGPL-3.0-or-later

package rollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Hook labels: commands run inside the container around an update, e.g.
// "pg_dump -U app app > /backup/app.sql" before it. A value starting with
// "[" is a JSON argv for images without a shell; anything else runs with
// sh -c.
const (
	// LabelHookPre runs in the old container before it is stopped; failing
	// cancels the update.
	LabelHookPre = "neptide.hook.pre-update"
	// LabelHookPost runs in the new container once it is healthy; failing
	// rolls the update back.
	LabelHookPost = "neptide.hook.post-update"
	// LabelHookTimeout bounds each hook (default 5m).
	LabelHookTimeout = "neptide.hook.timeout"
)

// Hooks are the commands of one container.
type Hooks struct {
	Pre, Post []string
	Timeout   time.Duration
}

// HooksFrom reads the hook labels.
func HooksFrom(labels map[string]string) (Hooks, error) {
	h := Hooks{Timeout: 5 * time.Minute}
	var err error
	if h.Pre, err = hookCmd(labels[LabelHookPre]); err != nil {
		return h, fmt.Errorf("%s: %w", LabelHookPre, err)
	}
	if h.Post, err = hookCmd(labels[LabelHookPost]); err != nil {
		return h, fmt.Errorf("%s: %w", LabelHookPost, err)
	}
	if v, ok := labels[LabelHookTimeout]; ok {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return h, fmt.Errorf("%s=%q: want a positive duration like 10m", LabelHookTimeout, v)
		}
		h.Timeout = d
	}
	return h, nil
}

func hookCmd(v string) ([]string, error) {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return nil, nil
	case strings.HasPrefix(v, `["`):
		// Exec form, as in a Dockerfile; anything else (also "[ -d /x ] &&
		// …") is a shell command.
		var argv []string
		if err := json.Unmarshal([]byte(v), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
			return nil, errors.New(`want a JSON array of strings, e.g. ["/backup", "--now"]`)
		}
		return argv, nil
	}
	return []string{"sh", "-c", v}, nil
}

// killable wraps a shell hook so it is killed at the timeout where the
// image has timeout(1): Docker cannot kill an exec, so a hung hook would
// otherwise keep running after neptide gives up. The command is passed as
// $0, so it needs no quoting.
func killable(cmd []string, timeout time.Duration) []string {
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		return cmd
	}
	secs := int(timeout.Seconds())
	wrapper := fmt.Sprintf(`if command -v timeout >/dev/null 2>&1; then exec timeout -s KILL %d sh -c "$0"; fi; exec sh -c "$0"`, secs)
	return []string{"sh", "-c", wrapper, cmd[2]}
}

func (h Hooks) run(ctx context.Context, e Engine, id string, cmd []string, name string) error {
	if len(cmd) == 0 {
		return nil
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	code, out, err := e.Exec(hctx, id, killable(cmd, timeout))
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s hook did not finish within %s", name, timeout)
	case err != nil:
		return fmt.Errorf("%s hook: %w", name, err)
	case code != 0:
		if out != "" {
			return fmt.Errorf("%s hook exited with %d: %s", name, code, out)
		}
		return fmt.Errorf("%s hook exited with %d", name, code)
	}
	return nil
}
