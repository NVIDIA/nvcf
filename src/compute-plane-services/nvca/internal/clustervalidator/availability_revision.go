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
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// haRevisionAdvice is the advice for quorum members behind their update
// revision.
const haRevisionAdvice = "Bring each quorum member onto its StatefulSet's update revision, one at a time: under " +
	"updateStrategy OnDelete delete a member, standbys before the leader, and wait for it to be Ready before the " +
	"next; under a RollingUpdate held at a partition, lower the partition to 0."

// judgeLagging reports quorum members whose pods are not on the StatefulSet's
// update revision while no rollout will move them: under OnDelete the
// controller replaces a member only once it is deleted, and never moves the
// current revision, so the pods tell; a RollingUpdate held at its partition
// with every member Ready keeps the members below it where they are. A
// RollingUpdate in progress, or held with a member down, is the Tier-2 row's.
//
// Members that lack the placement terms the template renders do not have the
// mode's placement in effect: under enforced that fails, and the finding names
// which members lack which terms. Members that only lag warn.
func (g *haGuarantee) judgeLagging(f *haFound, want int32) {
	sts, update := f.sts, f.sts.Status.UpdateRevision
	if !f.podsRead || update == "" || sts.Status.CurrentRevision == update {
		return
	}
	var lagging []*corev1.Pod
	for _, p := range f.pods {
		if rev := p.Labels[appsv1.ControllerRevisionHashLabelKey]; rev != "" && rev != update {
			lagging = append(lagging, p)
		}
	}
	if len(lagging) == 0 {
		return
	}
	how := "under updateStrategy OnDelete a member moves to it only once deleted: delete such members one at a " +
		"time, standbys first, and wait for each to be Ready before the next"
	if sts.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		partition := rollingUpdatePartition(sts)
		held := partition > 0 && len(f.ready) >= int(want) && len(f.ready) == len(f.pods) &&
			!slices.ContainsFunc(lagging, func(p *corev1.Pod) bool {
				ordinal := podOrdinal(sts, p)
				return ordinal < 0 || ordinal >= partition
			})
		if !held {
			return
		}
		how = fmt.Sprintf("rollingUpdate partition %d holds such members there: lower it to 0 so the rollout "+
			"replaces them one at a time", partition)
	}
	names := make([]string, len(lagging))
	// lacking groups the members that lack placement terms by the terms they
	// lack, in the order first seen.
	var lacks []string
	lacking := map[string][]string{}
	for i, p := range lagging {
		names[i] = p.Name
		if missing := lackedPlacement(f, p); len(missing) > 0 {
			terms := joinAnd(missing)
			if _, seen := lacking[terms]; !seen {
				lacks = append(lacks, terms)
			}
			lacking[terms] = append(lacking[terms], p.Name)
		}
	}
	who := fmt.Sprintf("members %s are not on update revision %s", joinAnd(names), update)
	if len(lagging) == 1 {
		who = fmt.Sprintf("member %s is not on update revision %s", names[0], update)
	}
	if len(lacks) == 0 {
		g.find(haWarns, "", "%s: %s; %s", f.ref, who, how)
		return
	}
	// Every member behind lacking the same terms reads as one clause.
	clauses := []string{"and " + memberVerb(names, "lack") + " " + lacks[0]}
	if len(lacks) > 1 || len(lacking[lacks[0]]) < len(names) {
		clauses = clauses[:0]
		for _, terms := range lacks {
			clauses = append(clauses, joinAnd(lacking[terms])+" "+memberVerb(lacking[terms], "lack")+" "+terms)
		}
		who += ";"
	}
	g.find(haFailsEnforced, haRevisionAdvice, "%s: %s %s its template renders, %s; %s", f.ref, who,
		strings.Join(clauses, ", and "), g.mode.asks(haFailsEnforced, "that placement in effect on every member"), how)
}

// memberVerb is verb in the form names takes: "lacks" for one, "lack" for more.
func memberVerb(names []string, verb string) string {
	if len(names) == 1 {
		return verb + "s"
	}
	return verb
}

// lackedPlacement names the placement terms f's template renders that p has
// only in a weaker form or not at all.
func lackedPlacement(f *haFound, p *corev1.Pod) []string {
	wantHost, wantZone := hostnameSeparation(f.tpl, f.namespace), zoneSpread(f.tpl)
	own := &corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: p.Labels}, Spec: p.Spec}
	hostShort, zoneShort := hostnameSeparation(own, f.namespace) < wantHost, zoneSpread(own) < wantZone
	var missing []string
	if hostShort {
		missing = append(missing, map[separation]string{
			separationHard: "the required hostname pod anti-affinity",
			separationSoft: "the preferred hostname pod anti-affinity or hostname spread",
		}[wantHost])
	}
	if zoneShort {
		missing = append(missing, map[separation]string{
			separationHard: "the DoNotSchedule zone spread",
			separationSoft: "the ScheduleAnyway zone spread",
		}[wantZone])
	}
	return missing
}
