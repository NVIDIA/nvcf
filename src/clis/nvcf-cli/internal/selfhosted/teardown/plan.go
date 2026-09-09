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

package teardown

import (
	"fmt"
	"strings"

	"nvcf-cli/internal/selfhosted/progress"
)

// PlanOpts controls a Plan dry-run call.
type PlanOpts struct {
	Plane       string // "control-plane" | "compute-plane"
	ClusterName string // required for compute-plane
	KubeContext string
	StackPath   string
	Releases    []ReleaseRef // pre-enumerated; in production the caller walks helmfile.d/
	Selector    string       // optional helmfile release selector used by real destroy
}

// DownPlan is the output of Plan: the ordered phase sequence plus the set of
// helm releases that would be uninstalled.
type DownPlan struct {
	Phases        []PlannedPhaseDown
	TotalEstSec   int
	WillUninstall []progress.ReleaseDescriptor // one entry per Helm release in PlanOpts.Releases
}

// PlannedPhaseDown describes a single phase in the down direction.
type PlannedPhaseDown struct {
	Num    int
	Name   string
	EstSec int
}

// p50DownPhases is the embedded P50 phase-duration table for the down
// direction. Numbers are rough estimates pending real telemetry (M+11.I).
var p50DownPhases = []PlannedPhaseDown{
	{1, "drain-active", 30},
	{2, "uninstall-compute-plane", 60},
	{3, "remove-cluster-row", 5},
	{4, "uninstall-control-plane", 90},
	{5, "remove-namespaces", 5},
	{6, "remove-pvcs", 5},
	{7, "verify-clean", 2},
}

// Plan returns a dry-run plan describing the phase sequence and the helm
// uninstall commands that down would execute. No cluster contact is made;
// Plan is safe to call offline (--plan-only).
func Plan(opts PlanOpts) (DownPlan, error) {
	var total int
	for _, p := range p50DownPhases {
		total += p.EstSec
	}

	will := make([]progress.ReleaseDescriptor, 0, len(opts.Releases))
	for _, rel := range opts.Releases {
		cmd := planReleaseCommand(opts, rel)
		will = append(will, progress.ReleaseDescriptor{
			Kind:      "helm",
			Name:      rel.Name,
			Namespace: rel.Namespace,
			Command:   cmd,
		})
	}

	return DownPlan{
		Phases:        p50DownPhases,
		TotalEstSec:   total,
		WillUninstall: will,
	}, nil
}

func planReleaseCommand(opts PlanOpts, rel ReleaseRef) string {
	if opts.Selector == "" {
		cmd := fmt.Sprintf("helm uninstall %s -n %s", rel.Name, rel.Namespace)
		if opts.KubeContext != "" {
			cmd += " --kube-context=" + opts.KubeContext
		}
		return cmd
	}

	args := []string{"helmfile"}
	if opts.StackPath != "" {
		args = append(args, "-f", strings.TrimRight(opts.StackPath, "/")+"/helmfile.d/")
	}
	if opts.KubeContext != "" {
		args = append(args, "--kube-context="+opts.KubeContext)
	}
	args = append(args, "--selector", opts.Selector, "destroy")
	return strings.Join(args, " ")
}
