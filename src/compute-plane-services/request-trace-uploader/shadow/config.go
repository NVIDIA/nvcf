// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package shadow runs the uploader read-only beside another uploader that
// owns the spool, so the two outputs can be compared before ownership moves.
//
// The owner uploads and deletes segments. The shadow reads the same segments
// and never deletes, renames or locks anything the owner uses. Everything it
// writes, its journal, lock, transports and missed-segment state, lives under
// its own state directory, and it uploads to its own destination. A segment
// the owner removes before the shadow reads it is counted as missed, not
// treated as an error.
//
// In shadow mode the service:
//   - skips leftover cleanup: Config.Foreign removes nothing,
//   - opens its journal with Config.Journal, never the owner's,
//   - reads sources from Config.SourceDir and never from the journal
//     configuration, whose spool is the shadow state directory,
//   - reports every intake scan to Tracker.Observe, and runs every read of a
//     source, hashing and prepare, inside Tracker.Track,
//   - finishes confirmed segments with Finisher instead of the cleanup that
//     deletes sources.
package shadow

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/foreign"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/internal/journalfs"
)

const (
	// DefaultStateDirName is created inside the source directory when
	// StateDir is empty. Like every uploader path it starts with ".rtu-", so
	// neither uploader treats it as a segment or a leftover, and it is not
	// the owner's state directory.
	DefaultStateDirName = ".rtu-shadow"
	// MaxDestinationBytes bounds the destination name.
	MaxDestinationBytes = 1024

	// uploaderEntryPrefix starts every uploader entry in a spool.
	uploaderEntryPrefix = ".rtu-"
	// journalDirName holds the shadow journal under StateDir.
	journalDirName = "journal"
)

// Config sets up shadow mode.
type Config struct {
	// SourceDir is the spool the owner uploads from. The shadow only reads
	// it.
	SourceDir string
	// StateDir holds everything the shadow writes. Empty means
	// SourceDir/.rtu-shadow. Inside SourceDir it must sit under a top-level
	// entry whose name starts with ".rtu-".
	StateDir string
	// Destination is where the shadow uploads, in the backend's own terms,
	// such as a table or a key prefix.
	Destination string
	// OwnerDestination is where the owner uploads, when known. The shadow
	// refuses to start when Destination names it, ignoring case.
	OwnerDestination string
	// OwnerStateDir is the owner's state directory when the owner is this
	// uploader with a state directory outside the default. The default,
	// SourceDir/.rtu-state, is always protected.
	OwnerStateDir string
}

// WithDefaults fills unset fields.
func (c Config) WithDefaults() Config {
	if c.StateDir == "" && c.SourceDir != "" {
		c.StateDir = filepath.Join(c.SourceDir, DefaultStateDirName)
	}
	return c
}

// Validate reports every invalid setting. Call it on WithDefaults.
func (c Config) Validate() error {
	var errs []error
	if !filepath.IsAbs(c.SourceDir) {
		errs = append(errs, errors.New("shadow source directory must be an absolute path"))
	}
	if !filepath.IsAbs(c.StateDir) {
		errs = append(errs, errors.New("shadow state directory must be an absolute path"))
	}
	if c.OwnerStateDir != "" && !filepath.IsAbs(c.OwnerStateDir) {
		errs = append(errs, errors.New("owner state directory must be an absolute path"))
	}
	if len(errs) == 0 {
		errs = append(errs, c.validateStateDir()...)
	}
	errs = append(errs, c.validateDestination()...)
	return errors.Join(errs...)
}

// validateStateDir keeps the shadow's files apart from the owner's: never the
// spool itself or a directory above it, never the owner's state, and inside
// the spool only under an uploader entry the owner skips.
func (c Config) validateStateDir() []error {
	source := filepath.Clean(c.SourceDir)
	state := filepath.Clean(c.StateDir)
	var errs []error
	if state == source {
		errs = append(errs, errors.New("shadow state directory must not be the source directory"))
	} else if _, ok := below(state, source); ok {
		errs = append(errs, errors.New("shadow state directory must not contain the source directory"))
	} else if rel, ok := below(source, state); ok {
		entry := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if !strings.HasPrefix(entry, uploaderEntryPrefix) {
			errs = append(errs, fmt.Errorf("shadow state directory inside the source directory must be under an entry named %s*", uploaderEntryPrefix))
		}
	}
	owners := []string{filepath.Join(source, journalfs.DefaultStateDirName)}
	if c.OwnerStateDir != "" {
		owners = append(owners, filepath.Clean(c.OwnerStateDir))
	}
	for _, owner := range owners {
		_, inside := below(owner, state)
		_, contains := below(state, owner)
		if state == owner || inside || contains {
			errs = append(errs, errors.New("shadow state directory must not overlap the owner's state directory"))
			break
		}
	}
	return errs
}

func (c Config) validateDestination() []error {
	destination := c.Destination
	switch {
	case strings.TrimSpace(destination) == "":
		return []error{errors.New("shadow mode needs its own destination")}
	case strings.TrimSpace(destination) != destination:
		return []error{errors.New("shadow destination must not start or end with space")}
	case len(destination) > MaxDestinationBytes:
		return []error{fmt.Errorf("shadow destination is over %d bytes", MaxDestinationBytes)}
	case strings.IndexFunc(destination, unicode.IsControl) >= 0:
		return []error{errors.New("shadow destination must not contain control characters")}
	case c.OwnerDestination != "" && strings.EqualFold(destination, strings.TrimSpace(c.OwnerDestination)):
		return []error{errors.New("shadow destination must differ from the owner's destination")}
	}
	return nil
}

// resolved returns the config with links resolved in every directory and
// checks it again, before anything is created, so a link cannot send the
// shadow's writes into the owner's spool or state.
func (c Config) resolved() (Config, error) {
	c = c.WithDefaults()
	if err := c.Validate(); err != nil {
		return c, err
	}
	var err error
	if c.SourceDir, err = resolveLinks(c.SourceDir); err != nil {
		return c, fmt.Errorf("resolve shadow source directory: %w", err)
	}
	if c.StateDir, err = resolveLinks(c.StateDir); err != nil {
		return c, fmt.Errorf("resolve shadow state directory: %w", err)
	}
	if c.OwnerStateDir != "" {
		if c.OwnerStateDir, err = resolveLinks(c.OwnerStateDir); err != nil {
			return c, fmt.Errorf("resolve owner state directory: %w", err)
		}
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("shadow directories after resolving links: %w", err)
	}
	return c, nil
}

// resolveLinks resolves the links in the longest existing part of path and
// keeps the rest as written, so a directory can be checked before it exists.
func resolveLinks(path string) (string, error) {
	existing, rest := filepath.Clean(path), ""
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, rest), nil
}

// below reports whether path is strictly inside dir, and its path relative
// to dir. Both must be clean absolute paths.
func below(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// Foreign returns the leftover-cleanup configuration for shadow mode, which
// removes nothing and deletes no uploaded source.
func (c Config) Foreign() foreign.Config {
	return foreign.Config{Mode: foreign.ModeShadow, Destination: c.Destination}
}

// Journal returns where the shadow journal lives, with links resolved. The
// caller sets Holder and the timeouts.
//
// The journal's spool is the shadow state directory, not SourceDir. The
// journal moves the source of an unreadable entry aside, next to the entry,
// and pointing it at the shadow's own directory means that step can never
// move a file the owner still needs.
func (c Config) Journal() (journalfs.Config, error) {
	c, err := c.resolved()
	if err != nil {
		return journalfs.Config{}, err
	}
	journalDir := filepath.Join(c.StateDir, journalDirName)
	if err := checkJournalDir(journalDir); err != nil {
		return journalfs.Config{}, err
	}
	return journalfs.Config{SpoolDir: c.StateDir, StateDir: journalDir}, nil
}

// checkJournalDir refuses a journal directory that is a link or holds one at
// its top level. The journal store creates, locks and writes through those
// entries, so a link there could reach the owner's journal and lock.
func checkJournalDir(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check shadow journal directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("shadow journal directory must be a plain directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("check shadow journal directory: %w", err)
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("shadow journal directory must not hold links")
		}
	}
	return nil
}
