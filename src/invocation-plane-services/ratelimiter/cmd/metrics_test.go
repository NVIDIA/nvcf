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

// A swallowed bind error leaves the pod running with no metrics and no log line.
func TestSetupMetricsLogsListenFailure(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer taken.Close()

	core, logs := observer.New(zap.InfoLevel)
	defer zap.ReplaceGlobals(zap.New(core))()

	setupMetrics(taken.Addr().String())

	failures := func() []observer.LoggedEntry {
		return logs.FilterLevelExact(zap.ErrorLevel).FilterMessage("metrics server failed").All()
	}
	require.Eventually(t, func() bool { return len(failures()) > 0 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, failures()[0].ContextMap()["error"], taken.Addr().String())
}
