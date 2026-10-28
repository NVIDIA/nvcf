// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package journalfs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultStateDirName is created inside the spool. Every uploader path
	// starts with ".rtu-" so the Bash uploader never picks it up after a
	// rollback to Bash on the same spool.
	DefaultStateDirName = ".rtu-state"
	// DefaultLockWait is the default drain timeout (60 s) plus 10 s, the
	// time an old uploader gets to finish before a new one gives up.
	DefaultLockWait     = 70 * time.Second
	DefaultTombstoneTTL = 24 * time.Hour
)

// Config locates and bounds the journal.
type Config struct {
	// SpoolDir is the directory Dynamo writes segments to.
	SpoolDir string
	// StateDir holds the journal. It must be on the same volume as the
	// spool, so it survives with it and fencing can move files by rename.
	// Empty means SpoolDir/.rtu-state.
	StateDir string
	// Holder names this uploader in the lock file, usually the pod name.
	Holder string
	// LockWait bounds how long Open waits for another uploader to release
	// the spool lock. Integration sets it to the drain timeout plus 10 s.
	LockWait time.Duration
	// TombstoneTTL is how long a Done entry is kept.
	TombstoneTTL time.Duration
}

// WithDefaults fills unset fields.
func (c Config) WithDefaults() Config {
	if c.StateDir == "" && c.SpoolDir != "" {
		c.StateDir = filepath.Join(c.SpoolDir, DefaultStateDirName)
	}
	if c.LockWait == 0 {
		c.LockWait = DefaultLockWait
	}
	if c.TombstoneTTL == 0 {
		c.TombstoneTTL = DefaultTombstoneTTL
	}
	return c
}

// Validate rejects a configuration the store cannot run with.
func (c Config) Validate() error {
	var errs []error
	if !filepath.IsAbs(c.SpoolDir) {
		errs = append(errs, errors.New("journal spool directory must be an absolute path"))
	}
	if !filepath.IsAbs(c.StateDir) {
		errs = append(errs, errors.New("journal state directory must be an absolute path"))
	} else if filepath.IsAbs(c.SpoolDir) && filepath.Clean(c.StateDir) == filepath.Clean(c.SpoolDir) {
		errs = append(errs, errors.New("journal state directory must not be the spool directory itself"))
	}
	if strings.TrimSpace(c.Holder) == "" {
		errs = append(errs, errors.New("journal lock holder name is required"))
	}
	if c.LockWait <= 0 {
		errs = append(errs, fmt.Errorf("journal lock wait must be positive, got %s", c.LockWait))
	}
	if c.TombstoneTTL <= 0 {
		errs = append(errs, fmt.Errorf("journal tombstone TTL must be positive, got %s", c.TombstoneTTL))
	}
	return errors.Join(errs...)
}
