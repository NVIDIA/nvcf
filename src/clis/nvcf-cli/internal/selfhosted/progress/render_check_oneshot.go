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
	"sync"

	"golang.org/x/term"
)

// CheckOneShotRenderer renders a final check dashboard inline when Final
// arrives. Unlike TTYRenderer, it does not enter alt-screen mode, so failed
// pre-flight details remain visible after the command exits.
type CheckOneShotRenderer struct {
	w     io.Writer
	mu    sync.Mutex
	model Model
}

// NewCheckOneShotRenderer constructs a one-shot check renderer writing to w.
func NewCheckOneShotRenderer(w io.Writer, opts ModelOpts) *CheckOneShotRenderer {
	if opts.Mode == 0 {
		opts.Mode = ModeCheck
	}
	if opts.Output == nil {
		opts.Output = w
	}
	m := NewModel(opts)
	cols, rows := 200, 60
	if f, ok := w.(interface{ Fd() uintptr }); ok {
		if c, r, err := term.GetSize(int(f.Fd())); err == nil && c > 0 && r > 0 {
			cols = c
			rows = r
		}
	}
	m.SetSize(cols, rows)
	return &CheckOneShotRenderer{w: w, model: m}
}

// Emit forwards check events into the embedded model. Final flushes the static
// dashboard to the writer.
func (r *CheckOneShotRenderer) Emit(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	next, _ := r.model.applyEvent(e)
	r.model = next.(Model)
	if _, ok := e.(Final); ok {
		_, err := io.WriteString(r.w, r.model.View())
		return err
	}
	return nil
}

// Close is a no-op for one-shot rendering because Final flushes the view.
func (r *CheckOneShotRenderer) Close() error { return nil }
