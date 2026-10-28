// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package shadow_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/cleanup"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/shadow"
)

const (
	testBackend     = "test"
	testDestination = "shadow"
)

type counts struct {
	mu sync.Mutex
	m  map[string]int64
}

func (c *counts) Add(name string, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int64{}
	}
	c.m[name] += delta
}

func (c *counts) Get(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[name]
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// sourceInfo writes a source segment into the spool and describes it as
// discovery would.
func sourceInfo(t *testing.T, spool string, index int, content []byte) (journal.SegmentInfo, string) {
	t.Helper()
	v := spoolView{cfg: shadow.Config{SourceDir: spool}}
	path := v.seg(index).Path
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return journal.SegmentInfo{
		Name:    filepath.Base(path),
		Prefix:  prefix,
		Index:   index,
		Size:    stat.Size(),
		ModTime: stat.ModTime(),
		SHA256:  hexSHA256(content),
	}, path
}

// confirm takes a discovered segment through prepare, one batch and a
// confirmed receipt, the way the service does.
func confirm(t *testing.T, store *journalfs.Store, id string, transport journal.Transport) {
	t.Helper()
	batchID := "batch-" + id[:12]
	steps := []func() error{
		func() error { return store.BeginPrepare(id) },
		func() error {
			return store.MarkPrepared(id, testBackend, map[string]journal.Transport{testDestination: transport})
		},
		func() error {
			return store.CreateBatch(journal.Batch{
				ID: batchID, Backend: testBackend, Destination: testDestination,
				Members: []journal.Member{{SegmentID: id, Path: transport.Path}},
			})
		},
		func() error {
			return store.SetReceipt(batchID, journal.Receipt{Backend: testBackend, ID: "job-" + id[:8]})
		},
		func() error { return store.ConfirmBatch(batchID) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("journal step %d: %v", i, err)
		}
	}
}

func itemFor(info journal.SegmentInfo, id, source string, transports ...string) cleanup.Item {
	return cleanup.Item{
		SegmentID:  id,
		Source:     source,
		Prefix:     info.Prefix,
		Index:      info.Index,
		Size:       info.Size,
		ModTime:    info.ModTime,
		SHA256:     info.SHA256,
		Transports: transports,
	}
}

func mustState(t *testing.T, store *journalfs.Store, id string, want journal.State) {
	t.Helper()
	seg, ok := store.Segment(id)
	if !ok {
		t.Fatalf("segment %s not in the journal", id[:8])
	}
	if seg.State != want {
		t.Fatalf("segment state = %s, want %s", seg.State, want)
	}
}

func discover(t *testing.T, store *journalfs.Store, info journal.SegmentInfo) string {
	t.Helper()
	seg, _, err := store.Discover(info)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	return seg.ID
}

func TestFinisherKeepsTheSourceAndRemovesItsTransports(t *testing.T) {
	spool := t.TempDir()
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")
	content := []byte("segment bytes")
	info, source := sourceInfo(t, spool, 1, content)
	id := discover(t, store, info)
	transport := journalfs.TransportPath(store.TransportDir(), id, testDestination, ".jsonl.gz")
	if err := os.WriteFile(transport, []byte("prepared"), 0o600); err != nil {
		t.Fatal(err)
	}
	confirm(t, store, id, journal.Transport{Path: transport, SHA256: hexSHA256([]byte("prepared")), Rows: 1, Bytes: 8})
	counter := &counts{}
	finisher, err := shadow.NewFinisher(cfg, store, counter)
	if err != nil {
		t.Fatalf("NewFinisher() error = %v", err)
	}

	result, err := finisher.Clean(context.Background(), itemFor(info, id, source, transport))
	if err != nil || result != cleanup.ResultDone {
		t.Fatalf("Clean() = %q, %v, want done", result, err)
	}
	if got, err := os.ReadFile(source); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("source changed or gone: %v", err)
	}
	if _, err := os.Stat(transport); !os.IsNotExist(err) {
		t.Fatalf("transport kept: %v", err)
	}
	mustState(t, store, id, journal.StateDone)
	if got := counter.Get(cleanup.CounterSegmentsDone); got != 1 {
		t.Fatalf("%s = %d, want 1", cleanup.CounterSegmentsDone, got)
	}
}

func TestFinisherRefusesPathsOutsideItsStateDirectory(t *testing.T) {
	spool := t.TempDir()
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")
	info, source := sourceInfo(t, spool, 1, []byte("segment bytes"))
	id := discover(t, store, info)
	transport := journalfs.TransportPath(store.TransportDir(), id, testDestination, ".jsonl.gz")
	if err := os.WriteFile(transport, []byte("prepared"), 0o600); err != nil {
		t.Fatal(err)
	}
	confirm(t, store, id, journal.Transport{Path: transport, SHA256: hexSHA256([]byte("prepared")), Rows: 1, Bytes: 8})
	finisher, err := shadow.NewFinisher(cfg, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := finisher.Clean(context.Background(), itemFor(info, id, source, transport, source)); err == nil {
		t.Fatal("Clean() error = nil for a transport path in the owner's spool")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source removed: %v", err)
	}
	if _, err := os.Stat(transport); err != nil {
		t.Fatalf("transport removed although the item was refused: %v", err)
	}
	mustState(t, store, id, journal.StateConfirmed)
}

func TestFinisherCleanEmptyKeepsTheSource(t *testing.T) {
	spool := t.TempDir()
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")
	info, source := sourceInfo(t, spool, 1, nil)
	id := discover(t, store, info)
	if err := store.BeginPrepare(id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkEmpty(id); err != nil {
		t.Fatal(err)
	}
	counter := &counts{}
	finisher, err := shadow.NewFinisher(cfg, store, counter)
	if err != nil {
		t.Fatal(err)
	}

	result, err := finisher.CleanEmpty(context.Background(), itemFor(info, id, source))
	if err != nil || result != cleanup.ResultDone {
		t.Fatalf("CleanEmpty() = %q, %v, want done", result, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("empty source removed: %v", err)
	}
	mustState(t, store, id, journal.StateDone)
	if got := counter.Get(cleanup.CounterSegmentsEmpty); got != 1 {
		t.Fatalf("%s = %d, want 1", cleanup.CounterSegmentsEmpty, got)
	}
}

func TestNewFinisherRejectsBadInput(t *testing.T) {
	cfg := shadow.Config{SourceDir: t.TempDir(), Destination: "shadow_table"}.WithDefaults()
	if _, err := shadow.NewFinisher(cfg, nil, nil); err == nil {
		t.Fatal("NewFinisher() error = nil without a journal")
	}
	store := openJournal(t, cfg, "shadow")
	if _, err := shadow.NewFinisher(shadow.Config{SourceDir: "spool", Destination: "x"}, store, nil); err == nil {
		t.Fatal("NewFinisher() error = nil for an invalid config")
	}
}

func TestFinisherRefusesATransportReachedThroughALink(t *testing.T) {
	spool := t.TempDir()
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")
	info, source := sourceInfo(t, spool, 1, []byte("segment bytes"))
	id := discover(t, store, info)
	transport := journalfs.TransportPath(store.TransportDir(), id, testDestination, ".jsonl.gz")
	if err := os.WriteFile(transport, []byte("prepared"), 0o600); err != nil {
		t.Fatal(err)
	}
	confirm(t, store, id, journal.Transport{Path: transport, SHA256: hexSHA256([]byte("prepared")), Rows: 1, Bytes: 8})
	link := filepath.Join(store.TransportDir(), "link")
	if err := os.Symlink(spool, link); err != nil {
		t.Fatal(err)
	}
	finisher, err := shadow.NewFinisher(cfg, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	viaLink := filepath.Join(link, filepath.Base(source))
	if _, err := finisher.Clean(context.Background(), itemFor(info, id, source, transport, viaLink)); err == nil {
		t.Fatal("Clean() error = nil for a transport path that leads into the owner's spool")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("owner source removed through the link: %v", err)
	}
	if _, err := os.Stat(transport); err != nil {
		t.Fatalf("transport removed although the item was refused: %v", err)
	}
	mustState(t, store, id, journal.StateConfirmed)
}

// filepath.Dir drops "link/.." before links are resolved, but the kernel
// follows the link first, so such a path must never reach a removal.
func TestFinisherRefusesANonCanonicalTransportPath(t *testing.T) {
	spool := t.TempDir()
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")
	info, source := sourceInfo(t, spool, 1, []byte("segment bytes"))
	id := discover(t, store, info)
	transport := journalfs.TransportPath(store.TransportDir(), id, testDestination, ".jsonl.gz")
	if err := os.WriteFile(transport, []byte("prepared"), 0o600); err != nil {
		t.Fatal(err)
	}
	confirm(t, store, id, journal.Transport{Path: transport, SHA256: hexSHA256([]byte("prepared")), Rows: 1, Bytes: 8})
	if err := os.Mkdir(filepath.Join(spool, "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(spool, "subdir"), filepath.Join(store.TransportDir(), "link")); err != nil {
		t.Fatal(err)
	}
	finisher, err := shadow.NewFinisher(cfg, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	sneaky := store.TransportDir() + "/link/../" + filepath.Base(source)
	if _, err := finisher.Clean(context.Background(), itemFor(info, id, source, transport, sneaky)); err == nil {
		t.Fatal("Clean() error = nil for a transport path with link/..")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("owner source removed: %v", err)
	}
}
