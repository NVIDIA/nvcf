/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Size carries terminal dimensions. Cols < compactThresholdCols OR
// Rows < compactThresholdRows triggers the bubbletea compact-mode fallback
// (M+7 accessibility delta).
type Size struct {
	Cols int
	Rows int
}

// compactThresholdCols and compactThresholdRows define the terminal-size
// boundary at which the bubbletea renderer falls back to compact mode.
// Below either dimension, the dashboard collapses sub-progress + next-up.
//
// Single source of truth for both SelectRenderer's row-8 branch and
// Model.compact() in render_tty.go.
const (
	compactThresholdCols = 100
	compactThresholdRows = 30
)

// RendererKind is a stable string discriminator returned by SelectRenderer so
// callers and tests know which row of the accessibility matrix matched without
// reflecting on the concrete EventSink type. See SRD/SDD §6.4.4.
type RendererKind string

const (
	RendererJSONL      RendererKind = "jsonl"
	RendererPlain      RendererKind = "plain"
	RendererAccessible RendererKind = "accessible"
	RendererTTYCompact RendererKind = "tty-compact"
	RendererTTYFull    RendererKind = "tty-full"
)

// RenderOpts captures the inputs to the selection matrix.
//
// TerminalSize, when non-nil, overrides the auto-detected size and also
// signals "treat stderr as a TTY for selection purposes" — used by tests
// that supply a non-TTY stderr but want to exercise the small/large-terminal
// branches deterministically. Production callers leave it nil and the
// selector detects the real terminal size from stderr.
//
// Env, when non-nil, overrides os.LookupEnv lookups for TERM, NO_COLOR, and
// CI. A missing key in Env counts as "unset" (note: TERM unset forces plain,
// since renderers cannot draw without a terminfo entry). Production callers
// leave Env nil and the selector reads os.LookupEnv directly.
//
// Cluster, Target, Stack are threaded into the bubbletea ModelOpts when the
// TTY renderer is constructed (RendererTTYCompact and RendererTTYFull). Empty
// strings are fine — the Model handles them gracefully in the header. These
// fields are ignored by the plain, JSONL, and accessible renderers.
//
// ControlPlaneContext and ComputePlaneContext are the split-cluster kubeconfig
// contexts (M+9.E, REQ-20). When both are non-empty and different the TTY
// renderer adds Control:/Compute: lines to the install header. Plain/JSONL
// renderers receive the context information via per-phase events and ignore
// these fields.
type RenderOpts struct {
	JSON         bool
	Plain        bool
	Accessible   bool
	TerminalSize *Size
	Env          map[string]string

	Cluster string
	Target  string
	Stack   string

	ControlPlaneContext string // M+9.E: split-cluster header (TTY only)
	ComputePlaneContext string // M+9.E: split-cluster header (TTY only)

	// Mode picks which TTY layout the bubbletea Model produces. Default
	// (ModeInstall) preserves M+7 behavior for `up` callers. Set to
	// ModeStatus from the status command, ModeCheck from check, etc.
	Mode RenderMode

	// TotalPhases overrides the install-mode default (8) for ModeDown
	// (7-phase teardown) and ModeUninstall (3-5 phase per-plane teardown).
	// Zero leaves the renderer's default in place.
	TotalPhases int
}

// SelectRenderer picks an EventSink based on opts and ambient state. The
// matrix is documented in SRD/SDD §6.4.4 and implemented row-by-row below in
// priority order — first match wins.
func SelectRenderer(stderr io.Writer, opts RenderOpts) (EventSink, RendererKind, error) {
	// Validate mutually-exclusive flags.
	if opts.JSON && opts.Plain {
		return nil, "", fmt.Errorf("--json and --plain are mutually exclusive")
	}

	// Row 1: --json wins over everything else.
	if opts.JSON {
		return NewJSONLRenderer(stderr), RendererJSONL, nil
	}

	// Row 2: --plain.
	if opts.Plain {
		return NewPlainRenderer(stderr), RendererPlain, nil
	}

	// Row 3: --accessible overrides bubbletea even on a wide TTY.
	if opts.Accessible {
		return NewPlainRenderer(stderr), RendererAccessible, nil
	}

	// Row 4: stderr is not a TTY. When TerminalSize is non-nil the caller
	// explicitly asserts "treat stderr as TTY-ish" (test seam), so skip
	// this check in that case.
	if opts.TerminalSize == nil && !isWriterTTY(stderr) {
		return NewPlainRenderer(stderr), RendererPlain, nil
	}

	// Rows 5–7: environment-based forcing.
	lookupEnv := func(key string) (string, bool) {
		if opts.Env != nil {
			v, ok := opts.Env[key]
			return v, ok
		}
		return os.LookupEnv(key)
	}

	// Row 5: TERM=dumb or TERM unset.
	termVal, termSet := lookupEnv("TERM")
	if !termSet || termVal == "dumb" {
		return NewPlainRenderer(stderr), RendererPlain, nil
	}

	// Row 6: NO_COLOR set to any value (including empty string).
	if _, set := lookupEnv("NO_COLOR"); set {
		return NewPlainRenderer(stderr), RendererPlain, nil
	}

	// Row 7: CI set to a truthy value.
	if ciVal, set := lookupEnv("CI"); set && isTruthy(ciVal) {
		return NewPlainRenderer(stderr), RendererPlain, nil
	}

	// Row 8: terminal size — small terminal (< compactThresholdCols cols OR
	// < compactThresholdRows rows) → compact bubbletea.
	size := opts.TerminalSize
	if size == nil {
		size = detectTerminalSize(stderr)
	}
	if size != nil && (size.Cols < compactThresholdCols || size.Rows < compactThresholdRows) {
		return NewTTYRenderer(stderr, ModelOpts{
			Output:              stderr,
			Mode:                opts.Mode,
			TotalPhases:         opts.TotalPhases,
			Cluster:             opts.Cluster,
			Target:              opts.Target,
			Stack:               opts.Stack,
			ControlPlaneContext: opts.ControlPlaneContext,
			ComputePlaneContext: opts.ComputePlaneContext,
			NowFunc:             func() time.Time { return time.Now().UTC() },
		}), RendererTTYCompact, nil
	}

	// Row 9: default — full bubbletea.
	return NewTTYRenderer(stderr, ModelOpts{
		Output:              stderr,
		Mode:                opts.Mode,
		TotalPhases:         opts.TotalPhases,
		Cluster:             opts.Cluster,
		Target:              opts.Target,
		Stack:               opts.Stack,
		ControlPlaneContext: opts.ControlPlaneContext,
		ComputePlaneContext: opts.ComputePlaneContext,
		NowFunc:             func() time.Time { return time.Now().UTC() },
	}), RendererTTYFull, nil
}

// isWriterTTY returns true iff w is an *os.File whose file descriptor is
// attached to a terminal. Named isWriterTTY (not isTerminal) to avoid
// colliding with sink.go's isTerminal(Event) bool.
func isWriterTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// detectTerminalSize queries the terminal dimensions of the given writer.
// Returns nil if the writer is not an *os.File or GetSize fails.
func detectTerminalSize(w io.Writer) *Size {
	f, ok := w.(*os.File)
	if !ok {
		return nil
	}
	cols, rows, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return nil
	}
	return &Size{Cols: cols, Rows: rows}
}

// isTruthy returns true for canonical CI-system truthy values.
// Aligns with how GitHub Actions, GitLab CI, CircleCI, etc. set CI=true.
// Falsy values (false, 0, no, empty) leave the matrix free to fall through.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "y", "on":
		return true
	default:
		return false
	}
}
