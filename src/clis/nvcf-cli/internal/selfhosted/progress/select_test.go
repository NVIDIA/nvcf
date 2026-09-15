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
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectRenderer_JSONWins(t *testing.T) {
	_, kind, err := SelectRenderer(&bytes.Buffer{}, RenderOpts{JSON: true})
	require.NoError(t, err)
	assert.Equal(t, RendererJSONL, kind)
}

func TestSelectRenderer_PlainOverridesTTY(t *testing.T) {
	_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		Plain:        true,
		TerminalSize: &Size{Cols: 200, Rows: 60}, // would otherwise be tty-full
	})
	require.NoError(t, err)
	assert.Equal(t, RendererPlain, kind)
}

func TestSelectRenderer_JSONAndPlainIsError(t *testing.T) {
	_, _, err := SelectRenderer(&bytes.Buffer{}, RenderOpts{JSON: true, Plain: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--json")
	assert.Contains(t, err.Error(), "--plain")
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestSelectRenderer_AccessibleOverridesTTY(t *testing.T) {
	// Intentional: accessible mode returns *PlainRenderer (plain with verbose
	// state markers, no spinners). This is distinct from the TTY modes, which
	// return *TTYRenderer. The plain-return here is by spec, not a stub.
	sink, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		Accessible:   true,
		TerminalSize: &Size{Cols: 200, Rows: 60},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererAccessible, kind)
	_, isPlain := sink.(*PlainRenderer)
	assert.True(t, isPlain, "accessible mode must return *PlainRenderer (spec: plain with verbose markers)")
}

func TestSelectRenderer_NonTTYStderr(t *testing.T) {
	// bytes.Buffer is not an *os.File, so it can't be a TTY.
	_, kind, err := SelectRenderer(&bytes.Buffer{}, RenderOpts{
		TerminalSize: nil, // nil = let selector detect; Buffer → not a TTY
	})
	require.NoError(t, err)
	assert.Equal(t, RendererPlain, kind)
}

func TestSelectRenderer_TERMDumb(t *testing.T) {
	_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 200, Rows: 60},
		Env:          map[string]string{"TERM": "dumb"},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererPlain, kind)
}

func TestSelectRenderer_TERMUnset(t *testing.T) {
	_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 200, Rows: 60},
		Env:          map[string]string{}, // TERM not in map → unset
	})
	require.NoError(t, err)
	assert.Equal(t, RendererPlain, kind)
}

func TestSelectRenderer_NoColor(t *testing.T) {
	// NO_COLOR with empty value still triggers plain (any value counts).
	_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 200, Rows: 60},
		Env:          map[string]string{"TERM": "xterm-256color", "NO_COLOR": ""},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererPlain, kind)
}

func TestSelectRenderer_CITruthy(t *testing.T) {
	cases := []string{"true", "1", "yes", "TRUE"}
	for _, val := range cases {
		t.Run(val, func(t *testing.T) {
			_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
				TerminalSize: &Size{Cols: 200, Rows: 60},
				Env:          map[string]string{"TERM": "xterm-256color", "CI": val},
			})
			require.NoError(t, err)
			assert.Equal(t, RendererPlain, kind)
		})
	}
}

func TestSelectRenderer_CIFalsy(t *testing.T) {
	cases := []string{"false", "0", "no", ""}
	for _, val := range cases {
		t.Run("CI="+val, func(t *testing.T) {
			_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
				TerminalSize: &Size{Cols: 200, Rows: 60},
				Env: map[string]string{
					"TERM": "xterm-256color",
					"CI":   val,
				},
			})
			require.NoError(t, err)
			assert.Equal(t, RendererTTYFull, kind)
		})
	}
}

func TestSelectRenderer_CIUnset(t *testing.T) {
	_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 200, Rows: 60},
		Env: map[string]string{
			"TERM": "xterm-256color",
			// CI deliberately omitted
		},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererTTYFull, kind)
}

func TestSelectRenderer_SmallTerminalCompact(t *testing.T) {
	// Need a writer that IS a TTY for the size check to engage.
	// Test approach: use os.Stderr with TerminalSize override; the selector
	// treats TerminalSize non-nil as "caller asserts stderr is TTY-ish for
	// selection purposes." This is the test-injection seam.
	sink, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 80, Rows: 24},
		Env:          map[string]string{"TERM": "xterm-256color"},
		Cluster:      "test",
		Target:       "test-target",
		Stack:        "v0",
	})
	require.NoError(t, err)
	assert.Equal(t, RendererTTYCompact, kind)

	// Verify the swap from the M+7.5 stub: TTY modes must return *TTYRenderer.
	require.NotNil(t, sink)
	_, isTTY := sink.(*TTYRenderer)
	assert.True(t, isTTY, "RendererTTYCompact must return *TTYRenderer (no longer a *PlainRenderer stand-in)")
}

func TestSelectRenderer_LargeTerminalFull(t *testing.T) {
	sink, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 200, Rows: 60},
		Env:          map[string]string{"TERM": "xterm-256color"},
		Cluster:      "test",
		Target:       "test-target",
		Stack:        "v0",
	})
	require.NoError(t, err)
	assert.Equal(t, RendererTTYFull, kind)

	// Verify the swap from the M+7.5 stub: TTY modes must return *TTYRenderer.
	require.NotNil(t, sink)
	_, isTTY := sink.(*TTYRenderer)
	assert.True(t, isTTY, "RendererTTYFull must return *TTYRenderer (no longer a *PlainRenderer stand-in)")
}

func TestSelectRenderer_BoundaryConditions(t *testing.T) {
	// 99x60 → compact (cols < 100)
	_, kind, err := SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 99, Rows: 60},
		Env:          map[string]string{"TERM": "xterm-256color"},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererTTYCompact, kind)

	// 200x29 → compact (rows < 30)
	_, kind, err = SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 200, Rows: 29},
		Env:          map[string]string{"TERM": "xterm-256color"},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererTTYCompact, kind)

	// 100x30 → full (boundary inclusive: >= 100 cols AND >= 30 rows)
	_, kind, err = SelectRenderer(os.Stderr, RenderOpts{
		TerminalSize: &Size{Cols: 100, Rows: 30},
		Env:          map[string]string{"TERM": "xterm-256color"},
	})
	require.NoError(t, err)
	assert.Equal(t, RendererTTYFull, kind)
}
