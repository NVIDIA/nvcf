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

package clustervalidator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
)

// revise moves release's StatefulSet to update revision <name>-r2 and leaves
// the members at the ordinals in behind on <name>-r1. Members on r2 carry its
// template's placement terms; those behind carry them only when keepTerms.
func (f *haFixture) revise(release string, strategy appsv1.StatefulSetUpdateStrategyType, keepTerms bool,
	behind ...int) {
	s := f.sets[release]
	s.Spec.UpdateStrategy.Type = strategy
	s.Status.CurrentRevision, s.Status.UpdateRevision = s.Name+"-r1", s.Name+"-r2"
	for i, p := range f.pods[release] {
		p.Labels[appsv1.ControllerRevisionHashLabelKey] = s.Name + "-r2"
		lagging := false
		for _, b := range behind {
			lagging = lagging || b == i
		}
		if lagging {
			p.Labels[appsv1.ControllerRevisionHashLabelKey] = s.Name + "-r1"
		}
		if !lagging || keepTerms {
			p.Spec.Affinity = s.Spec.Template.Spec.Affinity.DeepCopy()
			p.Spec.TopologySpreadConstraints = s.Spec.Template.Spec.TopologySpreadConstraints
		}
	}
}

// partition holds release's RollingUpdate at partition n.
func (f *haFixture) partition(release string, n int32) {
	f.sets[release].Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{Partition: &n}
}

// Under OnDelete the controller replaces a member only once it is deleted,
// and never moves its current revision: members left on an older revision are
// found by their pods. Those that lack the placement terms the template
// renders fail under enforced and warn under preferred; those that only lag
// warn under both. Each finding names the members and how to move them.
func TestCheckHAGuarantee_MembersBehindTheUpdateRevision(t *testing.T) {
	const onDelete = "; under updateStrategy OnDelete a member moves to it only once deleted: delete such members " +
		"one at a time, standbys first, and wait for each to be Ready before the next"
	lacks := map[HAMode]string{
		HAModeEnforced: "the required hostname pod anti-affinity and the DoNotSchedule zone spread",
		HAModePreferred: "the preferred hostname pod anti-affinity or hostname spread and the ScheduleAnyway zone " +
			"spread",
	}
	for _, mode := range haModes {
		f := newHAFixture(t, mode)
		f.revise("openbao-server", appsv1.OnDeleteStatefulSetStrategyType, false, 0, 1)
		state, log := f.run(t, false, nil)
		want := "vault-system/openbao-server: members openbao-server-0 and openbao-server-1 are not on update " +
			"revision openbao-server-r2 and lack " + lacks[mode] + " its template renders, " +
			haAsk(mode, "that placement in effect on every member") + onDelete
		if mode == HAModeEnforced {
			haOutcome(t, "lacking/enforced", state, log, false, []string{want}, nil)
			assert.Contains(t, state.Recommendations, haRevisionAdvice)
		} else {
			haOutcome(t, "lacking/preferred", state, log, true, nil, []string{want})
		}

		f = newHAFixture(t, mode)
		f.revise("openbao-server", appsv1.OnDeleteStatefulSetStrategyType, true, 2)
		state, log = f.run(t, false, nil)
		haOutcome(t, "behind only/"+string(mode), state, log, true, nil, []string{"vault-system/openbao-server: " +
			"member openbao-server-2 is not on update revision openbao-server-r2" + onDelete})
	}

	// Members behind on different revisions, as an upgrade that went through
	// preferred leaves OpenBao: -0 and -1 on a revision with no terms, -2 on
	// one with the preferred terms. Only the members that lack a term are
	// named as lacking it.
	const who = "vault-system/openbao-server: members openbao-server-0, openbao-server-1 and openbao-server-2 " +
		"are not on update revision openbao-server-r2"
	mixed := func(mode HAMode, aff HAMode, spread HAMode) *haFixture {
		f := newHAFixture(t, mode)
		f.revise("openbao-server", appsv1.OnDeleteStatefulSetStrategyType, false, 0, 1, 2)
		sel := map[string]string{haInstanceLabel: "openbao-server"}
		p := f.pods["openbao-server"][2]
		p.Labels[appsv1.ControllerRevisionHashLabelKey] = "openbao-server-r1b"
		p.Spec.Affinity, _ = haPlacement(aff, sel)
		_, p.Spec.TopologySpreadConstraints = haPlacement(spread, sel)
		return f
	}
	state, log := mixed(HAModePreferred, HAModePreferred, HAModePreferred).run(t, false, nil)
	haOutcome(t, "mixed/preferred", state, log, true, nil, []string{who + "; openbao-server-0 and " +
		"openbao-server-1 lack " + lacks[HAModePreferred] + " its template renders, " +
		haAsk(HAModePreferred, "that placement in effect on every member") + onDelete})

	state, log = mixed(HAModeEnforced, HAModePreferred, HAModePreferred).run(t, false, nil)
	haOutcome(t, "mixed/enforced, all short", state, log, false, []string{who + " and lack " +
		lacks[HAModeEnforced] + " its template renders, "}, nil)

	state, log = mixed(HAModeEnforced, HAModeEnforced, HAModePreferred).run(t, false, nil)
	haOutcome(t, "mixed/enforced, short apart", state, log, false, []string{who + "; openbao-server-0 and " +
		"openbao-server-1 lack " + lacks[HAModeEnforced] + ", and openbao-server-2 lacks the DoNotSchedule zone " +
		"spread its template renders, " + haAsk(HAModeEnforced, "that placement in effect on every member")}, nil)

	// Every member recreated: OnDelete never moves the current revision, so
	// the mismatch alone says nothing.
	f := newHAFixture(t, HAModeEnforced)
	f.revise("openbao-server", appsv1.OnDeleteStatefulSetStrategyType, false)
	state, log = f.run(t, false, nil)
	haOutcome(t, "all recreated", state, log, true, nil, nil)
}

// A RollingUpdate replaces its members itself, so members behind its update
// revision are judged only where no rollout is in progress: held at its
// partition with every member Ready. A rollout under way, one past the
// partition, and one with a member down are the Tier-2 row's to judge.
func TestCheckHAGuarantee_MembersHeldBelowAPartition(t *testing.T) {
	f := newHAFixture(t, HAModeEnforced)
	f.revise("nats", appsv1.RollingUpdateStatefulSetStrategyType, false, 0, 1)
	f.partition("nats", 2)
	state, log := f.run(t, false, nil)
	haOutcome(t, "held", state, log, false, []string{"nats-system/nats: members nats-0 and nats-1 are not on update " +
		"revision nats-r2 and lack the required hostname pod anti-affinity and the DoNotSchedule zone spread its " +
		"template renders, mode enforced requires that placement in effect on every member; rollingUpdate " +
		"partition 2 holds such members there: lower it to 0 so the rollout replaces them one at a time"}, nil)

	for name, change := range map[string]func(*haFixture){
		"rolling":            func(*haFixture) {},
		"past the partition": func(f *haFixture) { f.partition("nats", 1) },
		"a member down": func(f *haFixture) {
			f.partition("nats", 2)
			f.pods["nats"][2].Status = readyPodStatus(false)
		},
	} {
		f := newHAFixture(t, HAModeEnforced)
		f.revise("nats", appsv1.RollingUpdateStatefulSetStrategyType, false, 0, 1)
		change(f)
		state, log := f.run(t, false, nil)
		assert.NotContains(t, log, "are not on update revision", name)
		for _, w := range state.Warnings {
			assert.NotContains(t, w, "are not on update revision", name)
		}
	}
}
