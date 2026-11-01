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
	"os"
	"os/signal"
	"syscall"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/cli"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/byoo-otel-collector/internal/logger"
)

func main() {
	logger.Init()
	rootCmd := cli.NewCommand()
	if err := rootCmd.Execute(); err != nil {
		logger.Logger.Error(err)

		// Hang the process until a signal is received
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
	}
}
