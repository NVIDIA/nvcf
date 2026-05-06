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
	"context"
	"io"

	"golang.org/x/term"
)

// StatusOneShotRenderer renders a single styled status snapshot to its
// writer when Final arrives, then exits. Unlike the bubbletea TTY renderer
// it does NOT enter alt-screen mode — the output stays on the user's
// terminal after the command exits. Use this for `nvcf self-hosted status`
// without `--watch`, where the user wants a static snapshot they can
// scroll back to.
//
// The renderer reuses the bubbletea Model's viewStatus() output so the
// rendering matches what watch-mode would draw — same colors, same
// component table, same compute-cluster panel — minus the "press q to
// quit" footer and the alt-screen redraw machinery.
type StatusOneShotRenderer struct {
	w     io.Writer
	model Model
}

// NewStatusOneShotRenderer constructs a one-shot status renderer writing
// to w. opts controls rendering knobs (Cluster header, color profile via
// Output, etc.) — same shape as ModelOpts used by NewTTYRenderer so callers
// can pass through whatever they would have given to bubbletea.
//
// The Model is constructed with Mode=ModeStatus so applyEvent dispatches
// into the status-specific event handler path (snapshot/component/cluster
// row buffering).
func NewStatusOneShotRenderer(w io.Writer, opts ModelOpts) *StatusOneShotRenderer {
	if opts.Mode == 0 {
		opts.Mode = ModeStatus
	}
	if opts.Output == nil {
		opts.Output = w
	}
	m := NewModel(opts)
	// Detect terminal width so viewStatus' tables don't pre-wrap. Falls
	// through to a generous 200-col cap when stderr isn't a real TTY
	// (e.g. piped output) — wider than necessary is harmless; narrower
	// would clip cluster names mid-component-row.
	cols, rows := 200, 60
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		if c, r, err := term.GetSize(int(f.Fd())); err == nil && c > 0 && r > 0 {
			cols = c
			rows = r
		}
	}
	m.SetSize(cols, rows)
	return &StatusOneShotRenderer{w: w, model: m}
}

// Emit forwards the event into the embedded Model. Snapshot / ComponentHealth
// / ClusterRow / RecentEvent populate the Model's status-mode buffers; Final
// triggers the actual flush via the writer. Other event kinds (PhaseStarted
// etc.) are no-ops for status mode — Model.applyEvent handles dispatch
// internally.
func (r *StatusOneShotRenderer) Emit(_ context.Context, e Event) error {
	next, _ := r.model.applyEvent(e)
	r.model = next.(Model)
	if _, ok := e.(Final); ok {
		// Strip the trailing "press q to quit" footer that viewStatus
		// appends — it's a watch-mode hint that doesn't make sense for
		// a one-shot. The footer is the last non-empty line.
		out := r.model.View()
		out = stripPressQFooter(out)
		_, err := io.WriteString(r.w, out)
		return err
	}
	return nil
}

// Close is a no-op for one-shot — Final triggered the flush, there's
// nothing held back.
func (r *StatusOneShotRenderer) Close() error { return nil }

// stripPressQFooter removes the "press q to quit" / "press q to quit · w
// to toggle watch mode" footer (and the blank line preceding it) from a
// rendered View. The footer is informational only and only applies to
// the live-watch dashboard.
func stripPressQFooter(s string) string {
	const marker = "press q to quit"
	idx := indexOfMarker(s, marker)
	if idx < 0 {
		return s
	}
	// Walk back to the start of the line containing the marker, then
	// trim any blank line that immediately precedes it.
	lineStart := idx
	for lineStart > 0 && s[lineStart-1] != '\n' {
		lineStart--
	}
	out := s[:lineStart]
	// Strip a trailing blank line if present.
	for len(out) >= 2 && out[len(out)-1] == '\n' && out[len(out)-2] == '\n' {
		out = out[:len(out)-1]
	}
	return out
}

// indexOfMarker is a small allocation-free substring search. strings.Index
// would also work; rolling our own avoids pulling in another import for
// one call site and keeps stripPressQFooter trivially testable.
func indexOfMarker(haystack, needle string) int {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return -1
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
