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

package main

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestSetupMetricsLogsServeFailure guards against silently discarding the
// metrics server's ListenAndServe error (NVIDIA/nvcf#540). The metrics port is
// occupied up front so the bind fails deterministically, and the global logger
// is swapped for an observer so the error-level log line can be asserted.
func TestSetupMetricsLogsServeFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:7776")
	require.NoError(t, err)
	defer ln.Close()

	observed, logs := observer.New(zap.InfoLevel)
	restore := zap.ReplaceGlobals(zap.New(observed))
	defer restore()

	setupMetrics()

	require.Eventually(t, func() bool {
		return len(logs.FilterMessage("metrics server failed").All()) > 0
	}, 5*time.Second, 10*time.Millisecond)
}
