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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatusOneShotRenderer_FlushesOnFinal asserts that the one-shot
// renderer buffers Snapshot/ComponentHealth events and writes the styled
// status view to its writer when Final arrives. This is the path used
// for `nvcf self-hosted status` (no --watch) on a TTY — bubbletea's
// alt-screen would clear the dashboard on exit; the one-shot keeps it
// inline so the user can scroll back.
func TestStatusOneShotRenderer_FlushesOnFinal(t *testing.T) {
	var buf bytes.Buffer
	r := NewStatusOneShotRenderer(&buf, ModelOpts{
		Cluster:   "ncp-local",
		AsciiOnly: true,
	})

	ctx := context.Background()

	// Pre-Final emits should not write anything to the user-facing buffer.
	require.NoError(t, r.Emit(ctx, Snapshot{Cluster: "ncp-local", Verdict: "healthy"}))
	require.NoError(t, r.Emit(ctx, ComponentHealth{Name: "SIS", Ready: 1, Total: 1, Healthy: true}))
	require.NoError(t, r.Emit(ctx, ComponentHealth{Name: "NATS", Ready: 3, Total: 3, Healthy: true}))
	require.NoError(t, r.Emit(ctx, ClusterRow{Name: "ncp-local", Healthy: true, IsCurrent: true}))
	require.Empty(t, buf.String(),
		"one-shot must not flush until Final fires")

	// Final triggers the flush.
	require.NoError(t, r.Emit(ctx, Final{Success: true}))
	out := buf.String()
	require.NotEmpty(t, out, "Final must trigger the flush")

	assert.Contains(t, out, "ncp-local",
		"cluster name must appear in the rendered snapshot")
	assert.Contains(t, out, "SIS",
		"component name must appear")
	assert.Contains(t, out, "healthy",
		"verdict must appear (case may vary by styling)")

	// "press q to quit" is a watch-mode hint and must not appear in the
	// one-shot output — the user scrolling back should see a clean
	// snapshot, not stale interactive instructions.
	assert.NotContains(t, out, "press q to quit",
		"one-shot must strip the watch-mode footer")
}

// TestStatusOneShotRenderer_CloseIsNoop asserts that Close on a one-shot
// renderer is safe to call (deferred cleanup pattern) and does not
// produce any additional output beyond what Final already flushed.
func TestStatusOneShotRenderer_CloseIsNoop(t *testing.T) {
	var buf bytes.Buffer
	r := NewStatusOneShotRenderer(&buf, ModelOpts{Cluster: "c", AsciiOnly: true})
	ctx := context.Background()
	require.NoError(t, r.Emit(ctx, Snapshot{Cluster: "c", Verdict: "healthy"}))
	require.NoError(t, r.Emit(ctx, Final{Success: true}))
	beforeClose := buf.Len()

	require.NoError(t, r.Close())
	require.Equal(t, beforeClose, buf.Len(),
		"Close must not write additional bytes")
}

// TestStripPressQFooter exercises the helper that removes the watch-mode
// "press q to quit" footer (and its preceding blank line) from a rendered
// View. The footer can take two forms: install-mode plain ("press q to
// quit") and status-mode with-toggle ("press q to quit · w to toggle
// watch mode"); both must be handled.
func TestStripPressQFooter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no footer leaves input unchanged",
			in:   "header\n\ncontent\n",
			want: "header\n\ncontent\n",
		},
		{
			name: "plain footer is stripped with its blank-line padding",
			in:   "header\n\ncontent\n\npress q to quit\n",
			want: "header\n\ncontent\n",
		},
		{
			name: "watch-toggle footer is stripped",
			in:   "content\n\npress q to quit · w to toggle watch mode\n",
			want: "content\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripPressQFooter(tc.in)
			assert.Equal(t, tc.want, got)
			assert.False(t, strings.Contains(got, "press q to quit"),
				"footer marker must not survive in output")
		})
	}
}
