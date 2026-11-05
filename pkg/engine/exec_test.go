// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s))) //nolint:gosec // test data is small
	return append(h, s...)
}

func TestDemux(t *testing.T) {
	var b bytes.Buffer
	b.Write(frame(1, "dumping\n"))
	b.Write(frame(2, "no space left\n"))
	out, err := demux(&b)
	if err != nil || out != "dumping\nno space left" {
		t.Errorf("clean stream: %q %v", out, err)
	}

	cut := frame(1, "half of a frame")[:12]
	if _, err := demux(bytes.NewReader(cut)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated stream must be an error, got %v", err)
	}

	// A garbled header claiming 4 GiB must not allocate it.
	huge := []byte{1, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}
	if _, err := demux(io.MultiReader(bytes.NewReader(huge), strings.NewReader("short"))); err == nil {
		t.Error("expected an error for a frame longer than the stream")
	}

	var long bytes.Buffer
	long.Write(frame(1, strings.Repeat("x", 10000)+"END"))
	if out, err := demux(&long); err != nil || len(out) > 4096 || !strings.HasSuffix(out, "END") {
		t.Errorf("long output: len %d, %v", len(out), err)
	}
}
