// SPDX-License-Identifier: AGPL-3.0-or-later

// Package kuma talks to Uptime Kuma's Socket.IO API, the only API Kuma has
// for maintenance windows. It implements just enough Engine.IO v4 /
// Socket.IO v5 over a WebSocket: connect, events, acknowledgements, pings.
package kuma

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// debugFrames, when set (tests), sees every received frame.
var debugFrames func(string)

// conn is one Socket.IO session.
type conn struct {
	ws *websocket.Conn

	mu      sync.Mutex
	nextID  int
	acks    map[int]chan json.RawMessage
	events  map[string]json.RawMessage // last payload per pushed event
	waiters map[string][]chan struct{}
	closed  chan struct{}
	err     error
	// stop ends the read loop; it lives as long as the session, not a call.
	stop context.CancelFunc
}

func dial(ctx context.Context, base string, hc *http.Client) (*conn, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/socket.io/")
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	q := u.Query()
	q.Set("EIO", "4")
	q.Set("transport", "websocket")
	u.RawQuery = q.Encode()
	ws, res, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: hc})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", base, err)
	}
	ws.SetReadLimit(64 << 20) // monitor lists can be large
	c := &conn{ws: ws, acks: map[int]chan json.RawMessage{}, events: map[string]json.RawMessage{},
		waiters: map[string][]chan struct{}{}, closed: make(chan struct{})}

	// Engine.IO open packet, then Socket.IO connect to the default namespace.
	_, msg, err := ws.Read(ctx)
	if err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("Engine.IO handshake: %w", err)
	}
	if len(msg) == 0 || msg[0] != '0' {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("unexpected Engine.IO handshake %q", msg)
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte("40")); err != nil {
		_ = ws.CloseNow()
		return nil, err
	}
	session, stop := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = stop
	go c.readLoop(session)
	// Kuma registers its event handlers asynchronously after the connection;
	// anything sent earlier is silently dropped. It announces the end of that
	// with loginRequired (or autoLogin when auth is disabled).
	if err := c.waitAny(ctx, "loginRequired", "autoLogin"); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// waitAny blocks until one of the events has arrived.
func (c *conn) waitAny(ctx context.Context, names ...string) error {
	c.mu.Lock()
	for _, n := range names {
		if _, ok := c.events[n]; ok {
			c.mu.Unlock()
			return nil
		}
	}
	w := make(chan struct{})
	for _, n := range names {
		c.waiters[n] = append(c.waiters[n], w)
	}
	c.mu.Unlock()
	select {
	case <-w:
		return nil
	case <-c.closed:
		return errors.New("connection closed during the Socket.IO handshake")
	case <-ctx.Done():
		return fmt.Errorf("waiting for Uptime Kuma to accept events: %w", ctx.Err())
	}
}

func (c *conn) readLoop(ctx context.Context) {
	defer close(c.closed)
	for {
		_, msg, err := c.ws.Read(ctx)
		if err != nil {
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
			return
		}
		s := string(msg)
		if debugFrames != nil {
			debugFrames("<- " + s)
		}
		if reply := c.handle(s); reply != "" {
			_ = c.ws.Write(ctx, websocket.MessageText, []byte(reply))
		}
	}
}

// handle processes one frame from the server and returns the frame to send
// back, if any. Frames are untrusted: anything malformed is ignored.
func (c *conn) handle(s string) (reply string) {
	switch {
	case s == "2": // Engine.IO ping from the server
		return "3"
	case strings.HasPrefix(s, "43"): // ack: 43<id>[...]
		id, payload := splitID(s[2:])
		c.mu.Lock()
		ch := c.acks[id]
		delete(c.acks, id)
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- json.RawMessage(payload):
			default: // defensive: each ack channel receives at most once
			}
		}
	case strings.HasPrefix(s, "42"): // event: 42["name", data]
		var arr []json.RawMessage
		if json.Unmarshal([]byte(s[2:]), &arr) != nil || len(arr) == 0 {
			return ""
		}
		var name string
		_ = json.Unmarshal(arr[0], &name)
		var data json.RawMessage
		if len(arr) > 1 {
			data = arr[1]
		}
		c.mu.Lock()
		c.events[name] = data
		for _, w := range c.waiters[name] {
			select {
			case <-w: // already closed through another name
			default:
				close(w)
			}
		}
		delete(c.waiters, name)
		c.mu.Unlock()
	}
	return ""
}

func splitID(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	id, _ := strconv.Atoi(s[:i])
	return id, s[i:]
}

// call emits an event with arguments and waits for its acknowledgement,
// returning the first ack argument.
func (c *conn) call(ctx context.Context, event string, args ...any) (json.RawMessage, error) {
	payload, err := json.Marshal(append([]any{event}, args...))
	if err != nil {
		return nil, err
	}
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.acks[id] = ch
	c.mu.Unlock()
	if err := c.ws.Write(ctx, websocket.MessageText, []byte("42"+strconv.Itoa(id)+string(payload))); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
			return nil, fmt.Errorf("%s: malformed acknowledgement %s", event, raw)
		}
		return arr[0], nil
	case <-c.closed:
		return nil, fmt.Errorf("%s: connection closed: %w", event, c.err)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// event returns the latest payload of a server-pushed event, waiting for
// the first one if none has arrived yet.
func (c *conn) event(ctx context.Context, name string) (json.RawMessage, error) {
	c.mu.Lock()
	if data, ok := c.events[name]; ok {
		c.mu.Unlock()
		return data, nil
	}
	w := make(chan struct{})
	c.waiters[name] = append(c.waiters[name], w)
	c.mu.Unlock()
	select {
	case <-w:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.events[name], nil
	case <-c.closed:
		return nil, errors.New("connection closed while waiting for " + name)
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for %s: %w", name, ctx.Err())
	}
}

func (c *conn) close() {
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
	c.stop()
}

// result is Kuma's usual acknowledgement shape.
type result struct {
	OK            bool   `json:"ok"`
	Msg           string `json:"msg"`
	Token         string `json:"token"`
	MaintenanceID int    `json:"maintenanceID"`
}

func decodeResult(event string, raw json.RawMessage, err error) (result, error) {
	var r result
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("%s: %w", event, err)
	}
	if !r.OK {
		return r, fmt.Errorf("%s: %s", event, r.Msg)
	}
	return r, nil
}
