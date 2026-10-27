// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	defaults := Config{SpoolDir: "/spool", Holder: "pod-a"}.WithDefaults()
	if defaults.StateDir != "/spool/.rtu-state" {
		t.Fatalf("default StateDir = %q, want /spool/.rtu-state", defaults.StateDir)
	}
	if defaults.LockWait != 70*time.Second || defaults.TombstoneTTL != 24*time.Hour {
		t.Fatalf("defaults = %+v, want 70s lock wait and 24h tombstone TTL", defaults)
	}
	if err := defaults.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"relative spool", func(c *Config) { c.SpoolDir = "spool" }},
		{"relative state dir", func(c *Config) { c.StateDir = "state" }},
		{"state dir is the spool", func(c *Config) { c.StateDir = "/spool/" }},
		{"no holder", func(c *Config) { c.Holder = " " }},
		{"negative lock wait", func(c *Config) { c.LockWait = -time.Second }},
		{"negative tombstone TTL", func(c *Config) { c.TombstoneTTL = -time.Hour }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}
}

func TestOpenRejectsInvalidConfig(t *testing.T) {
	h := newHarness(t)
	h.cfg.Holder = ""
	if _, err := Open(t.Context(), h.cfg, Options{}); err == nil {
		t.Fatal("Open accepted a config with no lock holder")
	}
}

func TestReopenReturnsLastSavedState(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")
	id := discover(t, store, info)
	transports := prepare(t, store, id, "fake", "primary")

	store = h.restart(store)

	seg := mustState(t, store, id, journal.StatePrepared)
	if seg.Name != info.Name || seg.SHA256 != info.SHA256 || seg.Index != 1 || seg.Backend != "fake" {
		t.Fatalf("reloaded segment = %+v, want identity of %s and backend fake", seg, info.Name)
	}
	dest := seg.Destinations["primary"]
	if dest == nil || dest.State != journal.StatePrepared || dest.Transport != transports["primary"] {
		t.Fatalf("reloaded destination = %+v, want prepared %+v", dest, transports["primary"])
	}

	file := filepath.Join(h.cfg.StateDir, "segments", info.Name+"."+id+".json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("journal file not named <segment name>.<id>.json: %v", err)
	}
	var schema struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || schema.V != journal.SchemaVersion {
		t.Fatalf("journal file schema = %d (%v), want v %d", schema.V, err, journal.SchemaVersion)
	}
}

func TestCrashBeforeRenameKeepsPreviousVersion(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))

	store.hooks.beforeRename = func(string) error { return errCrash }
	if err := store.BeginPrepare(id); !errors.Is(err, errCrash) {
		t.Fatalf("BeginPrepare error = %v, want the simulated crash", err)
	}
	mustState(t, store, id, journal.StateDiscovered)

	store.hooks.beforeRename = nil
	store = h.restart(store)

	mustState(t, store, id, journal.StateDiscovered)
	leftovers, _ := filepath.Glob(filepath.Join(h.cfg.StateDir, "segments", "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left after restart: %v", leftovers)
	}
}

func TestDiscoverIsIdempotent(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")

	first, created, err := store.Discover(info)
	if err != nil || !created || first.State != journal.StateDiscovered {
		t.Fatalf("first Discover = %+v, %v, %v; want a new Discovered entry", first, created, err)
	}
	if err := store.BeginPrepare(first.ID); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}
	second, created, err := store.Discover(info)
	if err != nil || created || second.ID != first.ID || second.State != journal.StatePreparing {
		t.Fatalf("second Discover = %+v, %v, %v; want the existing Preparing entry", second, created, err)
	}
}

func TestReusedNameWithNewContentGetsNewEntry(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	old := discover(t, store, h.writeSegment(1, "content from the first pod lifetime"))
	if err := store.BeginPrepare(old); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}

	// F22: Dynamo restarted on an empty directory and reused the name.
	reused := h.writeSegment(1, "content from the second pod lifetime")
	seg, created, err := store.Discover(reused)
	if err != nil || !created || seg.ID == old {
		t.Fatalf("Discover of a reused name = %+v, %v, %v; want a new entry", seg, created, err)
	}
	mustState(t, store, old, journal.StatePreparing)
	mustState(t, store, seg.ID, journal.StateDiscovered)

	store = h.restart(store)
	if got := len(store.Segments()); got != 2 {
		t.Fatalf("segments after restart = %d, want 2", got)
	}
}

func TestDiscoverRejectsIncompleteIdentity(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	good := h.writeSegment(1, "segment one")
	cases := []struct {
		name   string
		mutate func(*journal.SegmentInfo)
	}{
		{"path in name", func(i *journal.SegmentInfo) { i.Name = "../" + i.Name }},
		{"no name", func(i *journal.SegmentInfo) { i.Name = "" }},
		{"no prefix", func(i *journal.SegmentInfo) { i.Prefix = "" }},
		{"no content hash", func(i *journal.SegmentInfo) { i.SHA256 = "" }},
		{"negative size", func(i *journal.SegmentInfo) { i.Size = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := good
			tc.mutate(&info)
			if _, _, err := store.Discover(info); err == nil {
				t.Fatalf("Discover accepted %s", tc.name)
			}
		})
	}
}

func TestInvalidTransitionIsRejectedAndNotPersisted(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))

	path := TransportPath(store.TransportDir(), id, "primary", ".parquet")
	err := store.MarkPrepared(id, "fake", map[string]journal.Transport{"primary": {Path: path}})
	if !errors.Is(err, journal.ErrInvalidTransition) {
		t.Fatalf("MarkPrepared from Discovered error = %v, want ErrInvalidTransition", err)
	}
	if err := store.BeginPrepare("unknown"); !errors.Is(err, journal.ErrNotFound) {
		t.Fatalf("BeginPrepare of unknown id error = %v, want ErrNotFound", err)
	}
	store = h.restart(store)
	mustState(t, store, id, journal.StateDiscovered)
}

func TestEveryUploaderPathIsHiddenFromBash(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	id := discover(t, store, h.writeSegment(1, "segment one"))
	prepare(t, store, id, "fake", "primary")

	segmentPattern := regexp.MustCompile(`\.\d{6}\.jsonl\.gz$`)
	entries, err := os.ReadDir(h.cfg.SpoolDir)
	if err != nil {
		t.Fatalf("read spool: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == segmentName(1) {
			continue
		}
		if !strings.HasPrefix(entry.Name(), ".rtu-") {
			t.Errorf("spool entry %q does not start with .rtu-", entry.Name())
		}
	}
	err = filepath.WalkDir(h.cfg.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if segmentPattern.MatchString(d.Name()) {
			t.Errorf("state path %q matches the segment file pattern", d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk state dir: %v", err)
	}
}

func TestMarkPreparedOnlyAcceptsTransportsTheSegmentOwns(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	info := h.writeSegment(1, "segment one")
	id := discover(t, store, info)
	other := discover(t, store, h.writeSegment(2, "segment two"))
	if err := store.BeginPrepare(id); err != nil {
		t.Fatalf("BeginPrepare: %v", err)
	}
	cases := []struct {
		name string
		path string
	}{
		{"the source segment itself", filepath.Join(h.cfg.SpoolDir, info.Name)},
		{"another segment's transport", TransportPath(store.TransportDir(), other, "primary", ".parquet")},
		{"outside the transport directory", filepath.Join(h.cfg.StateDir, id+".parquet")},
		{"escapes through dot-dot", filepath.Join(store.TransportDir(), "..", "segments", id+".parquet")},
		{"relative", id + ".parquet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.MarkPrepared(id, "fake", map[string]journal.Transport{"primary": {Path: tc.path}})
			if err == nil {
				t.Fatalf("MarkPrepared accepted %s", tc.name)
			}
		})
	}
	mustState(t, store, id, journal.StatePreparing)
}

func TestClosedStoreRefusesEveryWrite(t *testing.T) {
	h := newHarness(t)
	store := h.open()
	ids, transports := preparedSegments(t, h, store, 1, "primary")
	other := h.writeSegment(5, "segment five")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	writes := map[string]func() error{
		"Discover":    func() error { _, _, err := store.Discover(other); return err },
		"Quarantine":  func() error { return store.Quarantine(ids[0], "x") },
		"CreateBatch": func() error { return store.CreateBatch(batchFor("b1", "primary", ids, transports)) },
		"Evict":       func() error { return store.Evict(ids[0]) },
		"Sweep":       func() error { _, err := store.Sweep(); return err },
		"Recover":     func() error { _, err := store.Recover(RecoverOptions{Backend: "fake"}); return err },
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, ErrClosed) {
			t.Errorf("%s after Close error = %v, want ErrClosed", name, err)
		}
	}
	store = h.open()
	mustState(t, store, ids[0], journal.StatePrepared)
}
