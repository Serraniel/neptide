// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/Serraniel/neptide/internal/updater"
)

type sent struct{ message, title, level string }

type fakeSender struct{ got []sent }

func (f *fakeSender) Send(message string, p *types.Params) []error {
	f.got = append(f.got, sent{message, (*p)[types.TitleKey], (*p)[types.LevelKey]})
	return nil
}

func TestNotifier(t *testing.T) {
	f := &fakeSender{}
	n := &notifier{targets: []target{{send: f}}, want: map[string]bool{"updated": true, "failed": true, "rolled_back": true, "beyond_track": true}, host: "nas", log: slog.Default()}
	n.Event(updater.Event{Kind: updater.Updated, Target: "web", From: "a:1", To: "a:2"})
	n.Event(updater.Event{Kind: updater.Available, Target: "db"}) // not wanted
	if len(f.got) != 1 || f.got[0].title != "neptide@nas: updated web" || f.got[0].level != "info" {
		t.Fatalf("got %+v", f.got)
	}

	f.got = nil
	n.digest = true
	n.Event(updater.Event{Kind: updater.Updated, Target: "web"})
	n.Event(updater.Event{Kind: updater.Updated, Target: "api"})
	n.Event(updater.Event{Kind: updater.Failed, Target: "db", Err: errors.New("pull denied")})
	if len(f.got) != 0 {
		t.Fatal("digest mode must wait for Flush")
	}
	n.Flush()
	n.Flush() // nothing pending: nothing sent
	if len(f.got) != 1 {
		t.Fatalf("want one digest, got %+v", f.got)
	}
	d := f.got[0]
	if d.title != "neptide@nas: 1 failed, 2 updated" || d.level != "error" || strings.Count(d.message, "•") != 3 || !strings.Contains(d.message, "pull denied") {
		t.Errorf("digest = %+v", d)
	}

	f.got = nil
	n.digest = false
	n.Event(updater.Event{Kind: updater.Beyond, Target: "db", To: "17"})
	if f.got[0].level != "warning" {
		t.Errorf("beyond track: %+v", f.got[0])
	}

	// A URL with its own title keeps it; neptide's goes into the message.
	own := &fakeSender{}
	n.targets = []target{{send: own, ownTitle: true}}
	n.Event(updater.Event{Kind: updater.RolledBack, Target: "web"})
	if own.got[0].title != "" || !strings.HasPrefix(own.got[0].message, "neptide@nas: rolled back web\n") || own.got[0].level != "error" {
		t.Errorf("own title: %+v", own.got[0])
	}
	if !hasOwnTitle("ntfy://ntfy.sh/topic?title=Mine") || !hasOwnTitle("smtp://u:p@h:25/?subject=x") || hasOwnTitle("discord://token@id") {
		t.Error("hasOwnTitle")
	}

	var none *notifier // no URLs configured
	none.Event(updater.Event{Kind: updater.Updated})
	none.Flush()
}

func TestDigestIsCapped(t *testing.T) {
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "• Updated container app" + strings.Repeat("x", 40)
	}
	out := capLines(lines, maxDigest)
	if len(out) > maxDigest || !strings.HasSuffix(out, "more") {
		t.Errorf("len %d, tail %q", len(out), out[len(out)-20:])
	}
}
