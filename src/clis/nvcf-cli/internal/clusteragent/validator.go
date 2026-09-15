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

package clusteragent

import "context"

// AgentValidator runs predefined health checks against a compute-plane cluster.
// It is a read-only counterpart to AgentInspector: every check reads cluster
// state (Kubernetes core resources and the NVCFBackend/ICMSRequest CRs) and
// reports a pass/fail/warn verdict without mutating anything.
//
// The Kubernetes implementation (k8s_validator.go) is the only one today. The
// interface is the seam where a future NVCA HTTP introspection endpoint can be
// swapped in without touching the cobra handlers or the table formatters.
type AgentValidator interface {
	// Validate runs the cluster-level checks selected by opts and returns one
	// CheckResult per check that ran, along with the cluster identity.
	Validate(ctx context.Context, opts ValidateOptions) (*ValidationResult, error)
	// ValidateDeployment runs the per-deployment checks for one function version.
	// An empty versionID matches the first scheduled request for the function.
	ValidateDeployment(ctx context.Context, functionID, versionID string, opts ValidateOptions) (*DeploymentValidation, error)
}

// The cluster-level check names. They double as the accepted values for the
// --check flag, so they are stable, lowercase, and hyphenated.
const (
	CheckNVCAReachable = "nvca-reachable"
	CheckGPUCapacity   = "gpu-capacity"
	CheckNATSHealth    = "nats-health"
	CheckPullSecret    = "pull-secret"
	CheckTLSCert       = "tls-cert"
)

// AllClusterChecks lists the cluster-level checks in display order. It is the
// default check set and the allow-list the cmd layer validates --check against.
var AllClusterChecks = []string{
	CheckNVCAReachable,
	CheckGPUCapacity,
	CheckNATSHealth,
	CheckPullSecret,
	CheckTLSCert,
}

// ValidateOptions controls Validate and ValidateDeployment.
type ValidateOptions struct {
	// BackendNS is the namespace holding the NVCFBackend CR. The CR supplies the
	// cluster identity and the system namespace the checks read from.
	BackendNS string
	// CheckNames restricts Validate to the named cluster checks. Empty runs all
	// of AllClusterChecks. Values must be Check* constants. Ignored by
	// ValidateDeployment, which always runs its fixed check set.
	CheckNames []string
	// FailFast stops Validate after the first failing check.
	FailFast bool
	// NVCAURL, when non-empty, enables the NVCA HTTP probes (/version for
	// nvca-reachable, /livez for nats-health). Empty falls back to CR-derived
	// state for those checks.
	NVCAURL string
}

// CheckStatus is the verdict for a single check.
type CheckStatus string

const (
	CheckPassed  CheckStatus = "PASS"
	CheckFailed  CheckStatus = "FAIL"
	CheckWarning CheckStatus = "WARN"
	CheckSkipped CheckStatus = "SKIP"
)

// CheckResult is the outcome of one named check.
type CheckResult struct {
	Name    string      `json:"name"`
	Status  CheckStatus `json:"status"`
	Message string      `json:"message"`
}

// ValidationResult is the outcome of a cluster-level Validate run.
type ValidationResult struct {
	ClusterID   string        `json:"clusterId,omitempty"`
	ClusterName string        `json:"clusterName,omitempty"`
	Checks      []CheckResult `json:"checks"`
}

// HasFailure reports whether any check failed, which the cmd layer maps to a
// non-zero exit code.
func (r *ValidationResult) HasFailure() bool {
	return hasFailure(r.Checks)
}

// DeploymentValidation is the outcome of ValidateDeployment for one function
// version.
type DeploymentValidation struct {
	FunctionID        string        `json:"functionId"`
	FunctionVersionID string        `json:"functionVersionId,omitempty"`
	Checks            []CheckResult `json:"checks"`
}

// HasFailure reports whether any deployment check failed.
func (d *DeploymentValidation) HasFailure() bool {
	return hasFailure(d.Checks)
}

func hasFailure(checks []CheckResult) bool {
	for _, c := range checks {
		if c.Status == CheckFailed {
			return true
		}
	}
	return false
}
