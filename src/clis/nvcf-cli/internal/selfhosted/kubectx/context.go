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

package kubectx

import (
	"errors"
	"fmt"
	"strings"
)

// Mode describes the cluster topology selected by the operator's context flags.
type Mode int

const (
	ModeSingle Mode = iota // both flags unset → single kubeconfig context drives both planes
	ModeSplit              // both flags set, different values → split-cluster topology
	ModeError              // exactly one flag set, OR both set to the same value (ambiguous)
)

// String for error messages and event logs.
func (m Mode) String() string {
	switch m {
	case ModeSingle:
		return "single"
	case ModeSplit:
		return "split"
	case ModeError:
		return "error"
	default:
		return "unknown"
	}
}

// SelectMode returns the topology Mode given the two context flag values.
// Both empty -> ModeSingle (current behavior).
// Both set, different -> ModeSplit.
// Both set, same value -> ModeError (ambiguous; the operator probably meant single-cluster + a leftover flag).
// Exactly one set -> ModeError (REQ-20 mandates symmetry).
func SelectMode(controlCtx, computeCtx string) Mode {
	if controlCtx == "" && computeCtx == "" {
		return ModeSingle
	}
	if controlCtx == "" || computeCtx == "" {
		return ModeError
	}
	if controlCtx == computeCtx {
		return ModeError
	}
	return ModeSplit
}

// EnvForPhase returns a child-process env (suitable for exec.Cmd.Env) with
// KUBE_CONTEXT set to ctxName when ctxName != "". KUBECONFIG is preserved
// from parent (caller's responsibility to pass os.Environ()).
//
// helmfile reads KUBE_CONTEXT for the active context; kubectl honors
// --kube-context but for subprocesses without that flag, the env var is
// the universal lever.
func EnvForPhase(parent []string, ctxName string) []string {
	if ctxName == "" {
		return parent
	}
	out := make([]string, 0, len(parent)+1)
	seen := false
	for _, kv := range parent {
		if strings.HasPrefix(kv, "KUBE_CONTEXT=") {
			out = append(out, "KUBE_CONTEXT="+ctxName)
			seen = true
			continue
		}
		out = append(out, kv)
	}
	if !seen {
		out = append(out, "KUBE_CONTEXT="+ctxName)
	}
	return out
}

// errFlagSymmetry is the REQ-20 error message — exactly the wording from SRD/SDD.
const errFlagSymmetry = "--control-plane-context and --compute-plane-context must both be set or both be empty"

// ValidateFlags returns the REQ-20 error wording for flag-symmetry violations,
// or nil if the combination is valid.
func ValidateFlags(controlCtx, computeCtx string) error {
	switch SelectMode(controlCtx, computeCtx) {
	case ModeSingle, ModeSplit:
		return nil
	case ModeError:
		if controlCtx == computeCtx && controlCtx != "" {
			return fmt.Errorf("--control-plane-context and --compute-plane-context cannot both be %q (use neither for single-cluster mode)", controlCtx)
		}
		return errors.New(errFlagSymmetry)
	}
	return errors.New("kubectx: unknown mode")
}
