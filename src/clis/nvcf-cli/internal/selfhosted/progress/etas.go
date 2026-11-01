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

package progress

// p50Phases is the embedded P50 phase-duration table used by --plan-only ETAs.
// Numbers are pulled from production-orchestrator telemetry (rough order-of-
// magnitude estimates for v1; refresh each release from real telemetry).
//
// Phase order matches phaseNames in render_tty.go (1-indexed in events,
// 0-indexed here):
//
//	1 preflight        (~5s)   local-host tool checks
//	2 resolve-stack    (~3s)   OCI pull / git clone
//	3 render-cp        (~2s)   helmfile template
//	4 apply-cp         (~360s) full control-plane install
//	5 check-cp         (~10s)  init + admin-token mint
//	6 register         (~5s)   SIS register
//	7 apply-compute    (~120s) worker-layer install
//	8 final-health     (~30s)  function-deploy smoke
var p50Phases = [totalPhases]int{
	5, 3, 2, 360, 10, 5, 120, 30,
}

// PhaseETA returns the P50 historical-mean duration in seconds for the given
// 1-based phase number. Returns 0 if num is out of range.
func PhaseETA(num int) int {
	if num < 1 || num > totalPhases {
		return 0
	}
	return p50Phases[num-1]
}

// AllPhaseETAs returns a slice of PlannedPhase for every phase, in execution
// order. Used by --plan-only to construct the Planned event.
func AllPhaseETAs() []PlannedPhase {
	out := make([]PlannedPhase, totalPhases)
	for i := 0; i < totalPhases; i++ {
		out[i] = PlannedPhase{Num: i + 1, Name: phaseNames[i], ETASec: p50Phases[i]}
	}
	return out
}
