/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package nvca

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_startDebugServer(t *testing.T) {
	ctx, cancel := context.WithCancel(newTestContext())
	t.Cleanup(cancel)
	addr := newRandomAddress(t)

	a := &Agent{
		AgentOptions: &AgentOptions{
			NVCADebugAddr: addr,
		},
	}
	err := a.startDebugServer(ctx)
	require.NoError(t, err)

	pprofURLStr := "http://" + addr + "/debug/pprof/"
	assert.EventuallyWithT(t, func(ct *assert.CollectT) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pprofURLStr, nil)
		if !assert.NoError(ct, err) {
			return
		}
		res, err := http.DefaultClient.Do(req)
		if assert.NoError(ct, err) {
			t.Cleanup(func() { res.Body.Close() })
			assert.Equal(ct, http.StatusOK, res.StatusCode)
			b, err := io.ReadAll(res.Body)
			if !assert.NoError(ct, err) {
				return
			}
			assert.Contains(ct, string(b), "<html>")
		}
	}, 2*time.Second, 100*time.Millisecond)
}
