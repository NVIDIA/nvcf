// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"flag"

	"sigs.k8s.io/controller-runtime/pkg/manager/signals"

	cplane "github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/control-plane"
	"github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/utils"
)

const (
	componentsPath        = "COMPONENTS_PATH"
	defaultComponentsPath = "/etc/backend/components.yaml"
)

func main() {
	// liveness re-purposes this same binary as a Kubernetes exec liveness probe
	// (no HTTP server): the kubelet runs `control-plane -liveness`, which checks
	// the monitor's heartbeat is fresh and exits non-zero if it is stale.
	liveness := flag.Bool("liveness", false, "check the monitor heartbeat is fresh and exit (Kubernetes exec liveness probe)")
	flag.Parse()

	logger := utils.ConfigLogger()

	if *liveness {
		if err := cplane.CheckLiveness(); err != nil {
			logger.Fatal().Err(err).Msg("liveness check failed")
		}
		return
	}

	ctx := signals.SetupSignalHandler()
	ctx = logger.WithContext(ctx)

	k8sclient, err := utils.InitK8sClient()
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize K8s client")
	}

	monitor := cplane.New(k8sclient)
	if err = monitor.Loadcomponents(utils.GetEnvOr(componentsPath, defaultComponentsPath)); err != nil {
		logger.Fatal().Err(err).Msg("Failed to load components")
	}
	monitor.RunHealthChecks(ctx)
}
