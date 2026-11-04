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

package miniservice

import (
	"fmt"
	"regexp"
	"time"
)

const (
	// HelmChartInstanceServiceAccountName is the legacy name for the restricted instance
	// ServiceAccount. It is still provisioned by the agent's Helm-chart RBAC path.
	HelmChartInstanceServiceAccountName = "helm-instance-permissions"

	// InstanceServiceAccountName is the ServiceAccount that mini-service workloads
	// run as, and under which the MiniService reconciler applies workload objects (via an
	// impersonating client). It is the identity the admission webhook must match to validate
	// mini-service workloads. This constant is the single source of truth for that name; the
	// MiniService controller (internal/miniservice) references it so the two cannot drift.
	InstanceServiceAccountName = "miniservice-instance-permissions"
)

// InstanceServiceAccountUsername returns the Kubernetes username used when the MiniService
// reconciler impersonates the ServiceAccount that applies mini-service workload objects.
func InstanceServiceAccountUsername(namespace string) string {
	return fmt.Sprintf("system:serviceaccount:%s:%s", namespace, InstanceServiceAccountName)
}

var (
	HelmChartInstanceServiceAccountNameRegexp = regexp.MustCompile(fmt.Sprintf(
		"^system:serviceaccount:.+:%s$",
		HelmChartInstanceServiceAccountName))

	// InstanceServiceAccountNameRegexp matches the username of the ServiceAccount
	// under which mini-service workload objects are applied.
	InstanceServiceAccountNameRegexp = regexp.MustCompile(fmt.Sprintf(
		"^system:serviceaccount:.+:%s$",
		InstanceServiceAccountName))

	// DefaultMaxHelmObjectPendingTimeout is the maximum time a Helm release or repository
	// can have a "pending" state
	DefaultMaxHelmObjectPendingTimeout = 20 * time.Minute
)
