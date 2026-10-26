// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"reflect"
)

// RulesetAPI supplies repository ruleset snapshots and targeted writes.
type RulesetAPI interface {
	Snapshot(Policy) ([]Object, error)
	Write(Change) error
}

// ApplyPlan requires an unchanged reviewed plan and verifies preserved fields.
func ApplyPlan(policy Policy, reviewed Plan, api RulesetAPI) error {
	live, err := api.Snapshot(policy)
	if err != nil {
		return err
	}
	current, err := MakePlan(policy, live)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, reviewed) {
		return fmt.Errorf("live state or policy changed since review; generate and review a new plan")
	}
	for _, change := range reviewed.Changes {
		one := policy
		one.Rulesets = nil
		for _, desired := range policy.Rulesets {
			if desired["name"] == change.Name {
				one.Rulesets = append(one.Rulesets, desired)
			}
		}
		// GitHub has no transactional multi-ruleset update. Minimize the remaining
		// read/write race and stop if another administrator changed this rule.
		live, err := api.Snapshot(one)
		if err != nil {
			return err
		}
		fresh, err := MakePlan(one, live)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(fresh.Changes, []Change{change}) {
			return fmt.Errorf("%s: live state changed before write; review a new plan", change.Name)
		}
		if err := api.Write(change); err != nil {
			return fmt.Errorf("%s: apply: %w", change.Name, err)
		}
		readback, err := api.Snapshot(one)
		if err != nil {
			return err
		}
		if len(readback) != 1 || !ExpectedReadback(change.After, Editable(readback[0])) {
			return fmt.Errorf("%s: readback differs from the reviewed change", change.Name)
		}
	}
	return nil
}
