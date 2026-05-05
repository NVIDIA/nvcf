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

package selfhosted

import "fmt"

// AuthGateInputs holds the environmental signals that determine whether the
// user must be prompted to run `nvcf self-hosted init` before proceeding.
// Exported so that cmd/ can construct and pass it to DecideAuthGate.
type AuthGateInputs struct {
	TokenSet       bool                 // --token=… was passed
	StoredSession  bool                 // ~/.nvcf-cli/session.json exists & valid
	IsTTY          bool                 // stdin is a terminal
	NonInteractive bool                 // --non-interactive was passed
	Prompt         func() (bool, error) // returns true on user "Y", false on "n"
}

// AuthGateDecision is the pure output of DecideAuthGate.
type AuthGateDecision struct {
	RunInit  bool // caller should run the init flow before proceeding
	ErrorOut bool // caller should exit with a configuration-required error
}

// DecideAuthGate distills the inline-init-prompt rules from spec §9.1 into a
// pure, testable function. It does not perform I/O itself; callers supply the
// Prompt callback for TTY interaction.
//
// Decision matrix:
//   - TokenSet || StoredSession → short-circuit, no auth action needed
//   - !IsTTY || NonInteractive  → ErrorOut (cannot prompt)
//   - IsTTY && interactive      → invoke Prompt; yes → RunInit, no → ErrorOut
func DecideAuthGate(in AuthGateInputs) (AuthGateDecision, error) {
	if in.TokenSet || in.StoredSession {
		return AuthGateDecision{}, nil
	}
	if !in.IsTTY || in.NonInteractive {
		return AuthGateDecision{ErrorOut: true}, nil
	}
	if in.Prompt == nil {
		return AuthGateDecision{}, fmt.Errorf("auth gate: TTY mode requires a Prompt callback")
	}
	yes, err := in.Prompt()
	if err != nil {
		return AuthGateDecision{}, err
	}
	if yes {
		return AuthGateDecision{RunInit: true}, nil
	}
	return AuthGateDecision{ErrorOut: true}, nil
}
