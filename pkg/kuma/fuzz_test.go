// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"encoding/json"
	"testing"
)

// Server frames are untrusted: none may panic or block the read loop.
func FuzzHandleFrame(f *testing.F) {
	for _, s := range []string{"2", "3", `430[{"ok":true}]`, "43", "4312", `42["monitorList",{}]`, "42[]", "42[1]", "42", `42["x"`, `0{"sid":"a"}`, "43999999999999999999999[]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, frame string) {
		c := &conn{acks: map[int]chan json.RawMessage{}, events: map[string]json.RawMessage{}, waiters: map[string][]chan struct{}{}}
		ack := make(chan json.RawMessage, 1)
		c.acks[0] = ack
		w := make(chan struct{})
		c.waiters["monitorList"] = []chan struct{}{w, w} // one waiter registered twice
		if reply := c.handle(frame); reply != "" && frame != "2" {
			t.Fatalf("%q answered with %q", frame, reply)
		}
		c.acks[0] = ack
		c.handle(frame) // a duplicate frame must not block or panic either
		select {
		case raw := <-ack:
			_, _ = decodeResult("x", raw, nil)
		default:
		}
	})
}

func FuzzMonitorRefs(f *testing.F) {
	f.Add([]byte("services:\n  web:\n    labels:\n      kuma.web.http.name: ${NAME}\n      kuma.web.http.url: https://${DOMAIN}\n"), "x.example")
	f.Add([]byte("services:\n  web:\n    labels: [\"kuma.a.b.name=$${X}\", \"kuma..=\", \"kuma\"]\n"), "")
	f.Add([]byte("services: {a: null, b: [1], c: {labels: 7}}"), "${")
	f.Fuzz(func(_ *testing.T, compose []byte, v string) {
		_ = MonitorRefs(compose, map[string]string{"DOMAIN": v, "NAME": v})
	})
}
