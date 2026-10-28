// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package shadow_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/format"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/segment"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/shadow"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

type fileState struct {
	mode    fs.FileMode
	size    int64
	modTime time.Time
	sha256  string
}

// snapshot records every entry of the spool except the shadow's own state.
func snapshot(t *testing.T, spool, skip string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(spool, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == skip {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		state := fileState{mode: info.Mode(), size: info.Size(), modTime: info.ModTime()}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			file.Close()
			if err != nil {
				return err
			}
			state.sha256 = hex.EncodeToString(hash.Sum(nil))
		}
		rel, _ := filepath.Rel(spool, path)
		out[rel] = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// One shadow pass beside an owner that holds its lock and deletes a segment
// before the shadow reaches it: the shadow uploads what it could read, counts
// the rest as missed, and leaves every file the owner uses as it was.
func TestShadowPassLeavesTheSpoolUntouched(t *testing.T) {
	ctx := context.Background()
	spool := uploadertest.NewSpool(t, prefix)
	for i := range 4 {
		spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("req-"+string(rune('a'+i)), uint64(1000+i))).Bytes())
	}
	if err := os.WriteFile(filepath.Join(spool.Dir, "owner-marker"), []byte("busy"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := journalfs.Open(ctx, journalfs.Config{SpoolDir: spool.Dir, Holder: "owner", LockWait: 100 * time.Millisecond}, journalfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()

	cfg := shadow.Config{SourceDir: spool.Dir, Destination: "shadow_table"}.WithDefaults()
	before := snapshot(t, spool.Dir, cfg.StateDir)
	store := openJournal(t, cfg, "shadow")
	metrics, err := telemetry.NewMemory("test")
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := shadow.NewTracker(cfg, metrics)
	if err != nil {
		t.Fatal(err)
	}
	finisher, err := shadow.NewFinisher(cfg, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := transform.New(transform.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}

	scan, err := segment.ScanDir(spool.Dir, []string{prefix})
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Closed) != 3 {
		t.Fatalf("closed segments = %d, want 3", len(scan.Closed))
	}
	// The owner uploads and deletes the second segment first.
	ownerDeleted := scan.Closed[1]
	if err := os.Remove(ownerDeleted.Path); err != nil {
		t.Fatal(err)
	}

	finished := 0
	for _, seg := range scan.Closed {
		var (
			data []byte
			id   string
			w    format.Writer
		)
		missedIt, err := tracker.Track(seg, func() error {
			var err error
			if data, err = os.ReadFile(seg.Path); err != nil {
				return err
			}
			id = discover(t, store, journal.SegmentInfo{
				Name: filepath.Base(seg.Path), Prefix: seg.Prefix, Index: seg.Index,
				Size: seg.Size, ModTime: seg.ModTime, SHA256: hexSHA256(data),
			})
			w, err = format.GzipJSONL{}.NewWriter(format.Target{
				Segment: seg, SegmentID: id, Dir: store.TransportDir(), Destinations: []backend.Destination{testDestination},
			})
			if err != nil {
				return err
			}
			_, err = preparer.Prepare(ctx, seg.Path, w, nil)
			return err
		})
		if err != nil {
			t.Fatalf("Track() error = %v", err)
		}
		if missedIt {
			continue
		}
		out := w.Outputs()[0]
		confirm(t, store, id, journal.Transport{Path: out.Path, SHA256: out.SHA256, Rows: out.Rows, Bytes: out.Bytes})
		info := journal.SegmentInfo{Prefix: seg.Prefix, Index: seg.Index, Size: seg.Size, ModTime: seg.ModTime, SHA256: hexSHA256(data)}
		if _, err := finisher.Clean(ctx, itemFor(info, id, seg.Path, out.Path)); err != nil {
			t.Fatalf("Clean() error = %v", err)
		}
		mustState(t, store, id, journal.StateDone)
		finished++
	}
	if _, err := tracker.Observe(scan); err != nil {
		t.Fatal(err)
	}

	if finished != 2 {
		t.Fatalf("finished = %d, want 2", finished)
	}
	if got := missed(t, metrics); got != 1 {
		t.Fatalf("%s = %v, want 1", telemetry.SegmentsMissed, got)
	}
	after := snapshot(t, spool.Dir, cfg.StateDir)
	want := maps.Clone(before)
	delete(want, filepath.Base(ownerDeleted.Path))
	// Creating the shadow state directory updates the spool's own mtime.
	delete(want, ".")
	delete(after, ".")
	if !maps.Equal(after, want) {
		t.Fatalf("spool changed outside the shadow state directory:\nbefore %v\nafter  %v", want, after)
	}
	entries, err := os.ReadDir(store.TransportDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%d transports left after finishing", len(entries))
	}
}
