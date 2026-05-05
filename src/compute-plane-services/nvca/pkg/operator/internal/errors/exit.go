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

package nvcaoperatorerrors

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
)

var ErrNotInKubernetesEnv = errors.New("not running in Kubernetes environment")

// terminationLogPath is the path to the Kubernetes termination log
// This variable can be overridden in tests
var terminationLogPath = "/dev/termination-log"

func ExitReason(ctx context.Context, err error) {
	if err == nil {
		return
	}
	if err := writeTerminationLog(err.Error()); err != nil && err != ErrNotInKubernetesEnv {
		core.GetLogger(ctx).WithError(err).Error("failed to write termination log")
	}
}

// writeTerminationLog writes a message to the Kubernetes termination log
// The message will be visible in the pod's events and logs
func writeTerminationLog(message string) error {
	if !isKubernetesEnv() {
		return ErrNotInKubernetesEnv
	}
	f, err := os.OpenFile(terminationLogPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open termination log: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(message); err != nil {
		return fmt.Errorf("failed to write to termination log: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync termination log: %w", err)
	}
	return nil
}

// isKubernetesEnv checks if the application is running in a Kubernetes environment
// by checking for the KUBERNETES_SERVICE_HOST environment variable which is automatically
// set by Kubernetes for all pods in the cluster
func isKubernetesEnv() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}
