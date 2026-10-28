// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

const testPrefix = "request-trace"

var errCrash = errors.New("simulated crash")

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type counts struct {
	mu     sync.Mutex
	values map[string]int64
}

func (c *counts) Add(name string, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]int64{}
	}
	c.values[name] += delta
}

func (c *counts) Get(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.values[name]
}

type harness struct {
	t       *testing.T
	cfg     Config
	clock   *fakeClock
	counter *counts
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	spool := t.TempDir()
	cfg := Config{SpoolDir: spool, Holder: "pod-a", LockWait: 300 * time.Millisecond}.WithDefaults()
	return &harness{t: t, cfg: cfg, clock: newClock(), counter: &counts{}}
}

func (h *harness) open() *Store {
	h.t.Helper()
	store, err := Open(context.Background(), h.cfg, Options{Now: h.clock.Now, Counter: h.counter})
	if err != nil {
		h.t.Fatalf("Open: %v", err)
	}
	h.t.Cleanup(func() { _ = store.Close() })
	return store
}

// restart closes store, as a killed process would release its lock, and
// opens a new store on the same spool.
func (h *harness) restart(store *Store) *Store {
	h.t.Helper()
	if err := store.Close(); err != nil {
		h.t.Fatalf("Close: %v", err)
	}
	return h.open()
}

func (h *harness) writeSegment(index int, content string) journal.SegmentInfo {
	h.t.Helper()
	name := segmentName(index)
	path := filepath.Join(h.cfg.SpoolDir, name)
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		h.t.Fatalf("write segment: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		h.t.Fatalf("stat segment: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	return journal.SegmentInfo{
		Name:    name,
		Prefix:  testPrefix,
		Index:   index,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		SHA256:  hex.EncodeToString(sum[:]),
	}
}

func segmentName(index int) string {
	return testPrefix + "." + pad6(index) + ".jsonl.gz"
}

func pad6(n int) string {
	s := "000000" + itoa(n)
	return s[len(s)-6:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// discover records a segment and returns its id.
func discover(t *testing.T, store *Store, info journal.SegmentInfo) string {
	t.Helper()
	seg, _, err := store.Discover(info)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return seg.ID
}

// prepare takes a discovered segment to Prepared with one transport file per
// destination, written where TransportPath says.
func prepare(t *testing.T, store *Store, id, backendName string, destinations ...string) map[string]journal.Transport {
	t.Helper()
	if err := store.BeginPrepare(id); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}
	transports := map[string]journal.Transport{}
	for _, dest := range destinations {
		path := TransportPath(store.TransportDir(), id, dest, ".parquet")
		if err := os.WriteFile(path, []byte("transport "+dest), 0o640); err != nil {
			t.Fatalf("write transport: %v", err)
		}
		transports[dest] = journal.Transport{Path: path, SHA256: "ab" + dest, Rows: 3, Bytes: 13}
	}
	if err := store.MarkPrepared(id, backendName, transports); err != nil {
		t.Fatalf("MarkPrepared: %v", err)
	}
	return transports
}

func mustState(t *testing.T, store *Store, id string, want journal.State) journal.Segment {
	t.Helper()
	seg, ok := store.Segment(id)
	if !ok {
		t.Fatalf("segment %s missing", id[:8])
	}
	if seg.State != want {
		t.Fatalf("segment state = %s, want %s", seg.State, want)
	}
	return seg
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
