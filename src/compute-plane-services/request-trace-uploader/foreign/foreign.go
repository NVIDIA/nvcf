// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package foreign handles a spool that another uploader has used.
//
// This uploader owns the spool. At startup, before it loads its journal, it
// removes the files a previous uploader left and counts them by kind; it
// leaves the kinds marked Keep and counts those too. A module that links this
// package registers the patterns from an init function, so the names of the
// other uploader's files never appear here.
package foreign

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
)

// Action says what startup does with an entry that matches a pattern.
type Action int

const (
	// Remove deletes the entry, and everything under it for a directory.
	Remove Action = iota + 1
	// Keep leaves the entry in place, for files an operator may still need.
	Keep
)

// Pattern names one kind of leftover at the top level of the spool.
type Pattern struct {
	// Kind labels the count. It must be a short lowercase token.
	Kind string
	// Glob is matched with filepath.Match against an entry name in the spool
	// root. It never matches below the root.
	Glob   string
	Action Action
}

var (
	patternMu sync.RWMutex
	patterns  []Pattern
)

// Register adds a leftover pattern. Misuse panics, because it can only be a
// build wiring mistake.
func Register(p Pattern) {
	if err := telemetry.ValidLabelValue(p.Kind); err != nil {
		panic(fmt.Sprintf("foreign leftover kind: %v", err))
	}
	if p.Glob == "" || strings.ContainsAny(p.Glob, `/\`) {
		panic(fmt.Sprintf("foreign leftover %q glob %q must be a non-empty name pattern", p.Kind, p.Glob))
	}
	if _, err := filepath.Match(p.Glob, ""); err != nil {
		panic(fmt.Sprintf("foreign leftover %q glob %q: %v", p.Kind, p.Glob, err))
	}
	if p.Action != Remove && p.Action != Keep {
		panic(fmt.Sprintf("foreign leftover %q has no action", p.Kind))
	}
	patternMu.Lock()
	defer patternMu.Unlock()
	for _, existing := range patterns {
		if existing.Glob == p.Glob {
			panic(fmt.Sprintf("foreign leftover glob %q registered twice", p.Glob))
		}
	}
	patterns = append(patterns, p)
}

func registered() []Pattern {
	patternMu.RLock()
	defer patternMu.RUnlock()
	return append([]Pattern(nil), patterns...)
}

// Report counts regular files by kind. A matched directory counts the files
// under it; a matched file or symbolic link counts one.
type Report struct {
	Removed map[string]int
	Kept    map[string]int
}

// Config sets up leftover cleanup. It has no settings; the zero value is valid.
type Config struct{}

// DefaultConfig returns the leftover-cleanup configuration.
func DefaultConfig() Config { return Config{} }

// Clean applies the registered patterns to the entries in root. protected is
// required and is checked first: it must report Dynamo segments and the
// uploader's own directories, which are never removed even if a pattern
// matches. Keep wins over Remove when both match. Removal failures do not stop
// the pass; they are joined and returned with the counts of what did succeed.
func (c Config) Clean(root string, protected func(name string) bool) (Report, error) {
	report := Report{Removed: map[string]int{}, Kept: map[string]int{}}
	if protected == nil {
		return report, errors.New("clean leftovers: a protected-name check is required")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return report, fmt.Errorf("list spool for leftovers: %w", err)
	}
	active := registered()
	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if protected(name) {
			continue
		}
		pattern, ok := match(active, name)
		if !ok {
			continue
		}
		path := filepath.Join(root, name)
		files, err := countFiles(path, entry)
		if err != nil {
			errs = append(errs, fmt.Errorf("count leftover of kind %s: %w", pattern.Kind, err))
			continue
		}
		if pattern.Action == Keep {
			report.Kept[pattern.Kind] += files
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			errs = append(errs, fmt.Errorf("remove leftover of kind %s: %w", pattern.Kind, err))
			continue
		}
		report.Removed[pattern.Kind] += files
	}
	return report, errors.Join(errs...)
}

func match(active []Pattern, name string) (Pattern, bool) {
	var found Pattern
	matched := false
	for _, p := range active {
		if ok, _ := filepath.Match(p.Glob, name); !ok {
			continue
		}
		if p.Action == Keep {
			return p, true
		}
		if !matched {
			found, matched = p, true
		}
	}
	return found, matched
}

// countFiles counts regular files under a directory entry without following
// symbolic links; any other entry counts one.
func countFiles(path string, entry fs.DirEntry) (int, error) {
	if !entry.IsDir() {
		return 1, nil
	}
	count := 0
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			count++
		}
		return nil
	})
	return count, err
}

// Record presets the leftover counters to zero for every registered kind,
// under the counter that matches its action, then adds the counts in r.
func Record(m telemetry.Metrics, r Report) {
	for _, p := range registered() {
		name := telemetry.LeftoversRemoved
		if p.Action == Keep {
			name = telemetry.LeftoversKept
		}
		m.Add(name, 0, telemetry.Labels{"kind": p.Kind})
	}
	for _, kind := range sortedKeys(r.Removed) {
		m.Add(telemetry.LeftoversRemoved, float64(r.Removed[kind]), telemetry.Labels{"kind": kind})
	}
	for _, kind := range sortedKeys(r.Kept) {
		m.Add(telemetry.LeftoversKept, float64(r.Kept[kind]), telemetry.Labels{"kind": kind})
	}
}

func sortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
