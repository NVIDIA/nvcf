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

package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

func main() {
	logger := logs.NewZapLogger(zap.NewAtomicLevelAt(zap.DebugLevel))
	zap.RedirectStdLog(logger.GetZapLogger())
	defer logger.Close()

	zap.L().Info("Starting mock NVCT server")
	nvctServer := testutils.NewMockNvctServer(
		"10b076eb-b6d2-4cd9-878b-a3614a931570",
		"test-instance-id",
		"test-instance-type",
		"http://localhost:8003",
		5*time.Minute,
	)

	if err := nvctServer.Run("0.0.0.0:9091"); err != nil {
		zap.S().Panic(err)
	}
	defer nvctServer.Shutdown()

	// Waiting for termination signal.
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	<-done

	zap.L().Info("Terminating mock NVCT server")

}
