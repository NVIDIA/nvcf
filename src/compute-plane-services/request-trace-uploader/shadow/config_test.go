// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package shadow_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/foreign"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/shadow"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

func TestConfigValidate(t *testing.T) {
	valid := shadow.Config{SourceDir: "/spool", Destination: "shadow_table"}
	tests := []struct {
		name    string
		change  func(*shadow.Config)
		wantErr bool
	}{
		{"default state directory", func(*shadow.Config) {}, false},
		{"state directory outside the spool", func(c *shadow.Config) { c.StateDir = "/data/shadow" }, false},
		{"state directory under an uploader entry", func(c *shadow.Config) { c.StateDir = "/spool/.rtu-shadow-v2/state" }, false},
		{"source with a trailing slash", func(c *shadow.Config) { c.SourceDir = "/spool/" }, false},
		{"different owner destination", func(c *shadow.Config) { c.OwnerDestination = "request_traces" }, false},
		{"relative source", func(c *shadow.Config) { c.SourceDir = "spool" }, true},
		{"empty source", func(c *shadow.Config) { c.SourceDir = "" }, true},
		{"relative state", func(c *shadow.Config) { c.StateDir = "shadow" }, true},
		{"state is the spool", func(c *shadow.Config) { c.StateDir = "/spool/" }, true},
		{"state contains the spool", func(c *shadow.Config) { c.SourceDir, c.StateDir = "/data/spool", "/data" }, true},
		{"state inside the spool without the uploader prefix", func(c *shadow.Config) { c.StateDir = "/spool/shadow" }, true},
		{"state is the owner's default state", func(c *shadow.Config) { c.StateDir = "/spool/.rtu-state" }, true},
		{"state inside the owner's default state", func(c *shadow.Config) { c.StateDir = "/spool/.rtu-state/shadow" }, true},
		{"state inside the owner's configured state", func(c *shadow.Config) {
			c.OwnerStateDir, c.StateDir = "/data/owner", "/data/owner/shadow"
		}, true},
		{"state contains the owner's configured state", func(c *shadow.Config) {
			c.OwnerStateDir, c.StateDir = "/data/x/owner", "/data/x"
		}, true},
		{"relative owner state", func(c *shadow.Config) { c.OwnerStateDir = "owner" }, true},
		{"no destination", func(c *shadow.Config) { c.Destination = "" }, true},
		{"blank destination", func(c *shadow.Config) { c.Destination = "  " }, true},
		{"destination with surrounding space", func(c *shadow.Config) { c.Destination = " shadow_table" }, true},
		{"destination with a control character", func(c *shadow.Config) { c.Destination = "shadow\ntable" }, true},
		{"destination too long", func(c *shadow.Config) { c.Destination = strings.Repeat("a", shadow.MaxDestinationBytes+1) }, true},
		{"destination is the owner's", func(c *shadow.Config) {
			c.Destination, c.OwnerDestination = "Request_Traces", "request_traces"
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.change(&cfg)
			err := cfg.WithDefaults().Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWithDefaultsPutsStateUnderTheSpool(t *testing.T) {
	got := shadow.Config{SourceDir: "/spool/"}.WithDefaults().StateDir
	if want := "/spool/" + shadow.DefaultStateDirName; got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if got := (shadow.Config{SourceDir: "/spool", StateDir: "/data/shadow"}).WithDefaults().StateDir; got != "/data/shadow" {
		t.Fatalf("StateDir = %q, want the configured one kept", got)
	}
}

func init() {
	foreign.Register(foreign.Pattern{Kind: "stale", Glob: "*.stale", Action: foreign.Remove})
}

func TestForeignConfigIsShadowAndRemovesNothing(t *testing.T) {
	spool := t.TempDir()
	leftover := filepath.Join(spool, "old.stale")
	if err := os.WriteFile(leftover, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults().Foreign()
	if cfg.Mode != foreign.ModeShadow || cfg.Destination != "shadow_table" || cfg.DeletesSource() {
		t.Fatalf("Foreign() = %+v, want shadow mode to the shadow destination that deletes nothing", cfg)
	}
	if _, err := cfg.Clean(spool, func(string) bool { return false }); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("shadow mode removed a file the owner left: %v", err)
	}
}

func openJournal(t *testing.T, cfg shadow.Config, holder string) *journalfs.Store {
	t.Helper()
	jc, err := cfg.Journal()
	if err != nil {
		t.Fatalf("Journal() error = %v", err)
	}
	jc.Holder = holder
	jc.LockWait = 100 * time.Millisecond
	store, err := journalfs.Open(context.Background(), jc, journalfs.Options{})
	if err != nil {
		t.Fatalf("open shadow journal: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func inside(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// The owner keeps its lock while the shadow runs. The shadow must start
// without waiting for it and must not rewrite it.
func TestJournalOpensBesideTheOwnersLock(t *testing.T) {
	spool := t.TempDir()
	owner, err := journalfs.Open(context.Background(), journalfs.Config{SpoolDir: spool, Holder: "owner", LockWait: 100 * time.Millisecond}, journalfs.Options{})
	if err != nil {
		t.Fatalf("open owner journal: %v", err)
	}
	defer owner.Close()
	ownerLock := filepath.Join(owner.StateDir(), "lock")
	before, err := os.ReadFile(ownerLock)
	if err != nil {
		t.Fatal(err)
	}

	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")

	stateDir, err := filepath.EvalSymlinks(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !inside(stateDir, store.StateDir()) || !inside(stateDir, store.TransportDir()) {
		t.Fatalf("shadow journal at %q, transports at %q, want both under %q", store.StateDir(), store.TransportDir(), stateDir)
	}
	after, err := os.ReadFile(ownerLock)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("owner lock changed from %s to %s", before, after)
	}
}

// The journal fences an entry it cannot read together with its source. In
// shadow mode that source belongs to the owner and must stay where it is.
func TestCorruptJournalEntryNeverMovesTheSource(t *testing.T) {
	spool := t.TempDir()
	name := "request-trace.000001.jsonl.gz"
	source := filepath.Join(spool, name)
	content := []byte("segment bytes")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
	store := openJournal(t, cfg, "shadow")
	stateDir := store.StateDir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	id := journal.SegmentID("request-trace", 1, int64(len(content)), "f00d")
	entry := filepath.Join(stateDir, "segments", name+"."+id+".json")
	if err := os.WriteFile(entry, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	openJournal(t, cfg, "shadow")

	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("source left the spool: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("source content changed")
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("unreadable entry not fenced: %v", err)
	}
}

type noJournal struct{}

func (noJournal) BeginCleaning(string) error  { return nil }
func (noJournal) FinishCleaning(string) error { return nil }
func (noJournal) FinishEmpty(string) error    { return nil }

// A link can make a state directory that looks separate land in the spool.
// Every entry point resolves links before it writes anything.
func TestStateDirReachedThroughALinkIsRefused(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, spool string) shadow.Config
	}{
		{"state directory is a link to the spool", func(t *testing.T, spool string) shadow.Config {
			if err := os.Symlink(spool, filepath.Join(spool, shadow.DefaultStateDirName)); err != nil {
				t.Fatal(err)
			}
			return shadow.Config{SourceDir: spool, Destination: "shadow_table"}
		}},
		{"state directory under a link to the spool", func(t *testing.T, spool string) shadow.Config {
			if err := os.Symlink(spool, filepath.Join(spool, ".rtu-link")); err != nil {
				t.Fatal(err)
			}
			return shadow.Config{SourceDir: spool, StateDir: filepath.Join(spool, ".rtu-link", "state"), Destination: "shadow_table"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spool := t.TempDir()
			cfg := tt.setup(t, spool)
			if _, err := cfg.Journal(); err == nil {
				t.Error("Journal() error = nil")
			}
			metrics, _ := telemetry.NewMemory("test")
			if _, err := shadow.NewTracker(cfg, metrics); err == nil {
				t.Error("NewTracker() error = nil")
			}
			if _, err := shadow.NewFinisher(cfg, noJournal{}, nil); err == nil {
				t.Error("NewFinisher() error = nil")
			}
			if _, err := os.Stat(filepath.Join(spool, "state")); !os.IsNotExist(err) {
				t.Errorf("a directory was created in the spool: %v", err)
			}
		})
	}
}

// The journal directory is derived from the state directory, so a link there
// would open the owner's journal and take its lock.
func TestJournalDirectoryHoldingALinkIsRefused(t *testing.T) {
	tests := []struct {
		name string
		link func(t *testing.T, journalDir, ownerState string)
	}{
		{"journal directory is a link", func(t *testing.T, journalDir, ownerState string) {
			if err := os.Symlink(ownerState, journalDir); err != nil {
				t.Fatal(err)
			}
		}},
		{"journal lock is a link", func(t *testing.T, journalDir, ownerState string) {
			if err := os.MkdirAll(journalDir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(ownerState, "lock"), filepath.Join(journalDir, "lock")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spool := t.TempDir()
			owner, err := journalfs.Open(context.Background(), journalfs.Config{SpoolDir: spool, Holder: "owner", LockWait: 100 * time.Millisecond}, journalfs.Options{})
			if err != nil {
				t.Fatal(err)
			}
			ownerState := owner.StateDir()
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			cfg := shadow.Config{SourceDir: spool, Destination: "shadow_table"}.WithDefaults()
			if err := os.MkdirAll(cfg.StateDir, 0o750); err != nil {
				t.Fatal(err)
			}
			tt.link(t, filepath.Join(cfg.StateDir, "journal"), ownerState)

			if _, err := cfg.Journal(); err == nil {
				t.Fatal("Journal() error = nil")
			}
		})
	}
}
