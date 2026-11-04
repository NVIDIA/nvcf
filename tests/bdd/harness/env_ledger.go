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

package harness

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// EnvLedger snapshots environment variables before their first mutation
// during a suite and restores the original state at teardown.
//
// Used by the `When I export command output to environment variable {string}`
// step: the EKS Helmfile feature captures the gateway ELB hostname into
// EKS_GATEWAY_ADDR and the gateway ELB IP into EKS_GATEWAY_ELB_IP via
// that step, and the ledger ensures both vars are reset (or unset) at
// suite teardown.
type EnvLedger struct {
	snapshots map[string]envSnapshot
}

type envSnapshot struct {
	value string
	isSet bool
}

// NewEnvLedger returns a new EnvLedger with an empty snapshot table.
func NewEnvLedger() *EnvLedger {
	return &EnvLedger{snapshots: make(map[string]envSnapshot)}
}

// Snapshot records the pre-suite state of the named environment
// variable. Repeat calls for the same name are no-ops so that step
// handlers re-running the same export (e.g. once per scenario Background)
// do not overwrite the original snapshot with a value the suite itself
// wrote in a prior scenario.
func (l *EnvLedger) Snapshot(name string) error {
	if name == "" {
		return errors.New("env ledger: snapshot name must be non-empty")
	}
	if _, ok := l.snapshots[name]; ok {
		return nil
	}
	value, isSet := os.LookupEnv(name)
	l.snapshots[name] = envSnapshot{value: value, isSet: isSet}
	return nil
}

// RestoreAll restores every recorded variable to its pre-suite state.
// Variables that did not exist before are removed; variables that did
// are reset to their original value. Errors from individual os.Setenv /
// os.Unsetenv calls are collected and returned joined; restoration
// continues past the first failure so a single bad var name does not
// hide later restoration work.
func (l *EnvLedger) RestoreAll() error {
	names := make([]string, 0, len(l.snapshots))
	for name := range l.snapshots {
		names = append(names, name)
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		snap := l.snapshots[name]
		if snap.isSet {
			if err := os.Setenv(name, snap.value); err != nil {
				errs = append(errs, fmt.Errorf("restore %s: %w", name, err))
			}
		} else {
			if err := os.Unsetenv(name); err != nil {
				errs = append(errs, fmt.Errorf("unset %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}
