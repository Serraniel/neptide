// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/Serraniel/neptide/internal/updater"
)

// sender is what notifier needs from shoutrrr; tests replace it.
type sender interface {
	Send(message string, params *types.Params) []error
}

// notifier sends events with a title and a level, so services that support
// them (Discord embeds, Slack, Teams, ntfy, Telegram …) show them as such.
// In digest mode it collects a cycle's events and sends one message.
type notifier struct {
	targets []target
	want    map[string]bool
	digest  bool
	host    string
	log     *slog.Logger
	pending []updater.Event
}

// target is one notification URL. ownTitle: the URL sets a title or
// subject itself, which neptide's title must not override.
type target struct {
	send     sender
	ownTitle bool
}

func newNotifier(urls, kinds []string, digest bool, log *slog.Logger) (*notifier, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	host, _ := os.Hostname()
	if h := os.Getenv("NEPTIDE_HOST_NAME"); h != "" {
		host = h
	}
	n := &notifier{want: map[string]bool{}, digest: digest, host: host, log: log}
	for _, raw := range urls {
		s, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{Timeout: 20 * time.Second}, raw)
		if err != nil {
			return nil, fmt.Errorf("notification URL: %w", err)
		}
		n.targets = append(n.targets, target{s, hasOwnTitle(raw)})
	}
	for _, k := range kinds {
		n.want[k] = true
	}
	return n, nil
}

// Event handles one event: sent now, or kept for the digest.
func (n *notifier) Event(e updater.Event) {
	if n == nil || !n.want[string(e.Kind)] {
		return
	}
	if n.digest {
		n.pending = append(n.pending, e)
		return
	}
	n.deliver(n.title(headline(e)), e.String(), level(e.Kind))
}

// Flush sends the digest of the cycle that just ended.
func (n *notifier) Flush() {
	if n == nil || len(n.pending) == 0 {
		return
	}
	events := n.pending
	n.pending = nil
	byKind := map[updater.EventKind][]updater.Event{}
	worst := "info"
	for _, e := range events {
		byKind[e.Kind] = append(byKind[e.Kind], e)
		if l := level(e.Kind); rank[l] > rank[worst] {
			worst = l
		}
	}
	var parts, body []string
	for _, k := range digestOrder {
		es := byKind[k]
		if len(es) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", len(es), strings.ReplaceAll(string(k), "_", " ")))
		lines := make([]string, len(es))
		for i, e := range es {
			lines[i] = "• " + e.String()
		}
		sort.Strings(lines)
		body = append(body, lines...)
	}
	n.deliver(n.title(strings.Join(parts, ", ")), capLines(body, maxDigest), worst)
}

func (n *notifier) title(s string) string {
	if n.host != "" {
		return "neptide@" + n.host + ": " + s
	}
	return "neptide: " + s
}

func (n *notifier) deliver(title, message, lvl string) {
	for _, t := range n.targets {
		params := types.Params{types.TitleKey: title, types.LevelKey: lvl}
		if t.ownTitle {
			delete(params, types.TitleKey)
			message = title + "\n" + message // the summary must not get lost
		}
		for _, err := range t.send.Send(message, &params) {
			if err != nil {
				n.log.Warn("notification failed", "err", err)
			}
		}
	}
}

func hasOwnTitle(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	q := u.Query()
	return q.Has("title") || q.Has("subject")
}

// maxDigest keeps a digest below the smallest service limit (Telegram:
// 4096 characters including the title).
const maxDigest = 3500

var digestOrder = []updater.EventKind{updater.Failed, updater.RolledBack, updater.Updated, updater.Available, updater.Deferred, updater.Beyond}

var rank = map[string]int{"info": 0, "warning": 1, "error": 2}

func level(k updater.EventKind) string {
	switch k {
	case updater.Failed, updater.RolledBack:
		return "error"
	case updater.Beyond:
		return "warning"
	}
	return "info"
}

func headline(e updater.Event) string {
	switch e.Kind {
	case updater.Updated:
		return "updated " + e.Target
	case updater.RolledBack:
		return "rolled back " + e.Target
	case updater.Failed:
		return "update of " + e.Target + " failed"
	case updater.Available:
		return "update available for " + e.Target
	case updater.Deferred:
		return "update for " + e.Target + " waits for its window"
	case updater.Beyond:
		return e.Target + ": new release outside its track"
	}
	return e.Target
}

// capLines joins lines up to limit characters and says how many were left out.
func capLines(lines []string, limit int) string {
	var b strings.Builder
	for i, l := range lines {
		if b.Len()+len(l)+1 > limit-40 {
			fmt.Fprintf(&b, "… and %d more", len(lines)-i)
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
	}
	return b.String()
}
