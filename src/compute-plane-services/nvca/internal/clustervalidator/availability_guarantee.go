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
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
)

// haGuaranteeLabel names the HA Guarantee row and starts its warnings.
const haGuaranteeLabel = "HA Guarantee"

// The advice for each kind of HA Guarantee failure.
const (
	haSizingAdvice = "Restore the replica counts highAvailability.mode sets (helmfile apply): 2 or more for each " +
		"replica-safe Deployment, exactly 1 for grpc-proxy and invocation-service, and 3 or more for each quorum " +
		"StatefulSet. A secrets-file value or a kubectl scale below them weakens the guarantee."
	haPDBAdvice = "Restore one PodDisruptionBudget per HA workload, as highAvailability.mode renders them: " +
		"minAvailable 1 for each replica-safe Deployment and maxUnavailable 1 for each quorum StatefulSet."
	haPlacementAdvice = "Restore the placement mode enforced renders: required hostname pod anti-affinity and a " +
		"DoNotSchedule topology.kubernetes.io/zone spread. An explicit {} or [] under global.affinity or " +
		"global.topologySpreadConstraints removes them."
	haCoLocationAdvice = "Move co-located replicas onto distinct nodes and zones: add schedulable nodes or zones, " +
		"then delete one co-located pod so it is rescheduled."
	haZoneSkewAdvice = "Even out a quorum crowded into one zone: delete one member in the crowded zone, a standby " +
		"first, and wait for it to be Ready in the zone short of members before the next. A member whose volume " +
		"is node-local stays on its node."
	haZoneLabelAdvice = "Label every node that runs or can run the control plane with topology.kubernetes.io/zone: " +
		"under mode enforced the zone spread is DoNotSchedule, so no such pod can be placed on an unlabelled node."
	haMissingAdvice = "Install every always-on control-plane release, or check that the kube context points at the " +
		"control-plane cluster."
)

// haSeverity is how a finding grades under the HA mode.
type haSeverity int

const (
	// haFails fails under preferred and enforced: sizing, disruption budgets,
	// a missing workload and too few nodes for a quorum, which the two modes
	// share.
	haFails haSeverity = iota
	// haFailsEnforced fails under enforced and warns under preferred:
	// placement, zones and room for it, which only enforced makes hard.
	haFailsEnforced
	// haWarns warns under both.
	haWarns
)

// haFound is a workload of haWorkloads as found in the cluster.
type haFound struct {
	w      haWorkload
	ref    string
	tpl    *corev1.PodTemplateSpec
	deploy *appsv1.Deployment
	sts    *appsv1.StatefulSet
	// podsRead says its pods were read: pods are those it owns that are not
	// terminating, and ready those of them that are Ready.
	podsRead  bool
	pods      []*corev1.Pod
	ready     []*corev1.Pod
	namespace string
}

// haGuarantee collects what the HA Guarantee row found.
type haGuarantee struct {
	log      *logrus.Entry
	state    *ValidationState
	mode     HAMode
	external map[string]bool
	dates    *rolloutDating

	found    []*haFound
	pdbs     []policyv1.PodDisruptionBudget
	pdbErr   error
	nodes    []corev1.Node
	nodesErr error

	failures   []string
	advice     []string
	warned     int
	unobserved int
}

// checkHAGuarantee checks, under highAvailability.mode preferred or enforced,
// that each workload of haWorkloads meets what the mode renders for it:
// sizing, its PodDisruptionBudget, its rollout strategy, its rendered
// placement, whether a quorum's members run it (judgeLagging), where its Ready
// pods run, a quorum crowding one zone included, and that the nodes that run
// or can run them carry a zone label. Each finding names the workload, what it
// has and what the mode requires, so an override that weakens the guarantee
// reads as a degraded guarantee rather than as mode none.
//
// Sizing and disruption budgets fail under both modes. Placement and zones
// fail under enforced and warn under preferred, which only asks for them.
// Before install only what exists is judged. Under mode none the row is not
// run: it reads nothing, and its row says why. Without a mode only placement
// is judged (checkRenderedPlacement).
//
// Critical: a workload short of the mode's guarantee is one node or zone loss
// away from an outage the mode was chosen to prevent.
func checkHAGuarantee(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	if state.HAMode == "" {
		checkRenderedPlacement(ctx, client, state)
		return
	}
	if !state.HAMode.guaranteed() {
		return
	}
	printHeader(state.Log, "HA Guarantee (mode "+string(state.HAMode)+")")
	external, _ := externalComponents()
	g := &haGuarantee{log: state.Log, state: state, mode: state.HAMode, external: external,
		dates: &rolloutDating{ctx: ctx, client: client}}
	g.findWorkloads(ctx, client, haReplicaSafe, haSingleton, haQuorum)
	g.readPods(ctx, client)
	pdbs, err := observe(ctx, func(c context.Context) (*policyv1.PodDisruptionBudgetList, error) {
		return client.PolicyV1().PodDisruptionBudgets("").List(c, metav1.ListOptions{})
	})
	if g.pdbErr = err; err == nil {
		g.pdbs = pdbs.Items
	} else {
		g.notObserved("PodDisruptionBudgets", err)
	}
	nodes, err := observe(ctx, func(c context.Context) (*corev1.NodeList, error) {
		return client.CoreV1().Nodes().List(c, metav1.ListOptions{})
	})
	if g.nodesErr = err; err == nil {
		g.nodes = nodes.Items
	} else {
		g.notObserved("nodes", err)
	}
	for _, f := range g.found {
		switch f.w.tier {
		case haReplicaSafe:
			g.judgeReplicaSafe(f)
		case haSingleton:
			g.judgeSingleton(f)
		case haQuorum:
			g.judgeQuorum(f)
		}
	}
	g.judgeZoneLabels()
	g.verdict()
}

// checkRenderedPlacement is the HA Guarantee row without an HA mode. Sizing,
// disruption budgets and zones are what a mode asks for, so none of them is
// graded. Each replica-safe Deployment is judged only on whether its Ready
// replicas all run on one node, as its own rendered terms ask: a required
// hostname pod anti-affinity term as mode enforced judges it, a failure, and
// a preferred term or a hostname spread as mode preferred does, a warning.
// One with neither is not assessed, and the row counts only those assessed:
// with none, it says it assessed none and a warning says why. The Tier-2 row
// judges the quorum StatefulSets this way.
func checkRenderedPlacement(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	printHeader(state.Log, "HA Guarantee (HA mode not supplied: rendered placement only)")
	g := &haGuarantee{log: state.Log, state: state}
	g.findWorkloads(ctx, client, haReplicaSafe)
	g.readPods(ctx, client)
	var unassessed []string
	for _, f := range g.found {
		sep := hostnameSeparation(f.tpl, f.namespace)
		if sep == separationNone {
			unassessed = append(unassessed, f.ref)
			continue
		}
		node, together := onOneNode(f.ready)
		switch {
		case !together:
		case sep == separationHard:
			g.find(haFails, haCoLocationAdvice, "%s: all %d Ready replicas run on node %s, which its required "+
				"hostname pod anti-affinity forbids (HA mode not supplied, judged as mode enforced)", f.ref,
				len(f.ready), node)
		default:
			g.find(haWarns, "", "%s: all %d Ready replicas run on node %s; its preferred hostname pod "+
				"anti-affinity or hostname spread asks for distinct nodes (best-effort; HA mode not supplied, "+
				"judged as mode preferred)", f.ref, len(f.ready), node)
		}
	}
	if len(unassessed) > 0 {
		printInfo(g.log, "  Placement not assessed (no hostname pod anti-affinity or spread selects their own "+
			"pods): "+strings.Join(unassessed, ", "))
	}
	state.HAPlacementAssessed, state.HAPlacementNotAssessed = len(g.found)-len(unassessed), len(unassessed)
	g.verdict()
}

// onOneNode reports whether two or more pods all run on one node, and which.
func onOneNode(pods []*corev1.Pod) (string, bool) {
	if len(pods) < 2 {
		return "", false
	}
	node := pods[0].Spec.NodeName
	return node, !slices.ContainsFunc(pods, func(p *corev1.Pod) bool { return p.Spec.NodeName != node })
}

// asks words what m asks for in a finding of sev: "mode enforced requires
// what" where the finding fails, and a best-effort ask where it warns.
func (m HAMode) asks(sev haSeverity, what string) string {
	if sev == haFailsEnforced && m != HAModeEnforced {
		return fmt.Sprintf("mode %s asks for %s (best-effort)", m, what)
	}
	return fmt.Sprintf("mode %s requires %s", m, what)
}

// find records a finding as a failure or a warning, per sev and the mode.
func (g *haGuarantee) find(sev haSeverity, advice, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if sev == haFails || (sev == haFailsEnforced && g.mode == HAModeEnforced) {
		g.failures = append(g.failures, msg)
		if advice != "" && !slices.Contains(g.advice, advice) {
			g.advice = append(g.advice, advice)
		}
		return
	}
	printWarning(g.log, msg)
	g.state.Warnings = append(g.state.Warnings, haGuaranteeLabel+": "+msg)
	g.warned++
}

// notObserved records that resource could not be read, so what it decides is
// unknown rather than met.
func (g *haGuarantee) notObserved(resource string, err error) {
	printWarning(g.log, readFailure(resource, err))
	g.state.Warnings = append(g.state.Warnings, unknownWarning(haGuaranteeLabel, resource, err))
	g.unobserved++
}

// findWorkloads lists the Deployments and StatefulSets of each namespace of
// the table's workloads of tiers once, and records each workload found. After
// install under an HA mode, an always-on workload not found is a failure; one
// in a namespace that could not be read is unknown.
func (g *haGuarantee) findWorkloads(ctx context.Context, client kubernetes.Interface, tiers ...haTier) {
	deploys, sets := map[string][]appsv1.Deployment{}, map[string][]appsv1.StatefulSet{}
	unread := map[string]bool{}
	var deployTiers, setTiers []haTier
	for _, t := range tiers {
		if t == haQuorum {
			setTiers = append(setTiers, t)
		} else {
			deployTiers = append(deployTiers, t)
		}
	}
	for _, ns := range haNamespaces(deployTiers...) {
		list, err := observe(ctx, func(c context.Context) (*appsv1.DeploymentList, error) {
			return client.AppsV1().Deployments(ns).List(c, metav1.ListOptions{})
		})
		if err != nil {
			g.notObserved("Deployments in "+ns, err)
			unread[haDeploymentKind+"/"+ns] = true
			continue
		}
		deploys[ns] = list.Items
	}
	for _, ns := range haNamespaces(setTiers...) {
		list, err := observe(ctx, func(c context.Context) (*appsv1.StatefulSetList, error) {
			return client.AppsV1().StatefulSets(ns).List(c, metav1.ListOptions{})
		})
		if err != nil {
			g.notObserved("StatefulSets in "+ns, err)
			unread[haStatefulSetKind+"/"+ns] = true
			continue
		}
		sets[ns] = list.Items
	}
	for _, w := range haWorkloads {
		ns := w.namespaceName()
		if !slices.Contains(tiers, w.tier) || unread[w.kind()+"/"+ns] {
			continue
		}
		before := len(g.found)
		for i := range deploys[ns] {
			if d := &deploys[ns][i]; w.kind() == haDeploymentKind && w.owns(&d.Spec.Template) {
				g.found = append(g.found, &haFound{w: w, ref: ns + "/" + d.Name, tpl: &d.Spec.Template, deploy: d,
					namespace: ns})
			}
		}
		for i := range sets[ns] {
			if s := &sets[ns][i]; w.kind() == haStatefulSetKind && w.owns(&s.Spec.Template) {
				g.found = append(g.found, &haFound{w: w, ref: ns + "/" + s.Name, tpl: &s.Spec.Template, sts: s,
					namespace: ns})
			}
		}
		if len(g.found) == before && g.mode.guaranteed() && g.state.PostInstall && !w.optional &&
			!g.external[w.component] {
			g.find(haFails, haMissingAdvice, "%s/%s: no %s with %s found after install, mode %s requires it",
				ns, w.release, w.kind(), w.selector().String(), g.mode)
		}
	}
}

// readPods lists the pods of each namespace holding a replica-safe or quorum
// workload once, and gives each such workload the pods it owns. Pods that
// share its labels but not its controller, such as a release's hook Job, are
// not its pods.
func (g *haGuarantee) readPods(ctx context.Context, client kubernetes.Interface) {
	pods, errs := map[string][]corev1.Pod{}, map[string]error{}
	for _, f := range g.found {
		if f.w.tier == haSingleton {
			continue
		}
		ns := f.namespace
		if _, read := pods[ns]; !read && errs[ns] == nil {
			list, err := observe(ctx, func(c context.Context) (*corev1.PodList, error) {
				return client.CoreV1().Pods(ns).List(c, metav1.ListOptions{})
			})
			if err != nil {
				errs[ns] = err
			} else {
				pods[ns] = list.Items
			}
		}
		if err := errs[ns]; err != nil {
			g.notObserved("the pods of "+f.ref, err)
			continue
		}
		f.podsRead = true
		for i := range pods[ns] {
			p := &pods[ns][i]
			if p.DeletionTimestamp != nil || !f.ownsPod(p) {
				continue
			}
			f.pods = append(f.pods, p)
			if isPodReady(p) {
				f.ready = append(f.ready, p)
			}
		}
	}
}

// ownsPod reports whether p is a pod of f's controller: for a StatefulSet one
// it controls, for a Deployment one a ReplicaSet of its controls, named after
// it and the pod's template hash as the Deployment controller names them.
func (f *haFound) ownsPod(p *corev1.Pod) bool {
	if f.sts != nil {
		return metav1.IsControlledBy(p, f.sts)
	}
	ref := metav1.GetControllerOf(p)
	hash := p.Labels[appsv1.DefaultDeploymentUniqueLabelKey]
	return ref != nil && ref.Kind == "ReplicaSet" && hash != "" && ref.Name == f.deploy.Name+"-"+hash
}

// tolerated reports whether a Deployment's readiness short of its target is a
// rollout the Tier-1 row tolerates.
func tolerated(r workloadReadiness) bool { return r.rolling && r.ready >= r.floor }

func (g *haGuarantee) judgeReplicaSafe(f *haFound) {
	r := deploymentReadiness(f.deploy, g.dates)
	switch {
	case r.want < 2:
		g.find(haFails, haSizingAdvice, "%s: spec.replicas %d, mode %s requires at least 2", f.ref, r.want, g.mode)
	case r.ready >= 2:
	case tolerated(r):
		printInfo(g.log, fmt.Sprintf("  %s: Ready %d during a rollout; judged again once it completes", f.ref,
			r.ready))
	default:
		g.find(haFails, haSizingAdvice, "%s: Ready %d, mode %s requires at least 2 Ready", f.ref, r.ready, g.mode)
	}
	g.judgePDB(f, int(max(r.want, 2)))
	g.judgeStrategy(f, r.want)
	g.judgeRendered(f)
	g.judgeObserved(f, "replicas")
}

func (g *haGuarantee) judgeSingleton(f *haFound) {
	r := deploymentReadiness(f.deploy, g.dates)
	switch {
	case r.want != 1:
		g.find(haFails, haSizingAdvice, "%s: spec.replicas %d, mode %s requires exactly 1, since its callbacks "+
			"are bound to one pod", f.ref, r.want, g.mode)
	case r.ready < 1 && !tolerated(r):
		g.find(haFails, haSizingAdvice, "%s: Ready 0, mode %s requires its replica Ready", f.ref, g.mode)
	}
	for _, p := range g.pdbsSelecting(f) {
		if floor, setting, known := pdbFloor(p, 1); known && floor >= 1 {
			g.find(haWarns, "", "%s: PodDisruptionBudget %s %s allows no voluntary disruption at 1 replica, so it "+
				"blocks node drains", f.ref, p.Name, setting)
		}
	}
}

func (g *haGuarantee) judgeQuorum(f *haFound) {
	want, ready := int32(1), f.sts.Status.ReadyReplicas
	if f.sts.Spec.Replicas != nil {
		want = *f.sts.Spec.Replicas
	}
	if f.podsRead {
		ready = 0
		for range f.ready {
			ready++
		}
	}
	switch {
	case want < minQuorumSize:
		g.find(haFails, haSizingAdvice, "%s: spec.replicas %d, mode %s requires at least 3 members", f.ref, want,
			g.mode)
	case ready >= want:
	case quorumRollingOneDown(f.sts, want, ready):
		printInfo(g.log, fmt.Sprintf("  %s: Ready %d of %d in a rolling update; the Tier-2 row judges whether it "+
			"can finish", f.ref, ready, want))
	default:
		g.find(haFails, haSizingAdvice, "%s: Ready %d of %d, mode %s requires every member Ready", f.ref, ready,
			want, g.mode)
	}
	if want > minQuorumSize {
		printInfo(g.log, fmt.Sprintf("  %s: %d members, above the floor of %d", f.ref, want, minQuorumSize))
	}
	g.judgePDB(f, int(max(want, minQuorumSize)))
	g.judgeRendered(f)
	g.judgeLagging(f, want)
	g.judgeObserved(f, "members")
}

// quorumRollingOneDown reports whether sts, with ready of want members Ready,
// is one member down in a rolling update, the steady state of any StatefulSet
// rollout.
func quorumRollingOneDown(sts *appsv1.StatefulSet, want, ready int32) bool {
	return sts.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType &&
		sts.Status.UpdateRevision != "" && sts.Status.CurrentRevision != sts.Status.UpdateRevision &&
		ready == want-1
}

// pdbsSelecting returns the PodDisruptionBudgets in f's namespace that select
// its pods, by name.
func (g *haGuarantee) pdbsSelecting(f *haFound) []*policyv1.PodDisruptionBudget {
	var out []*policyv1.PodDisruptionBudget
	for i := range g.pdbs {
		if p := &g.pdbs[i]; p.Namespace == f.namespace && selectsLabels(p.Spec.Selector, labels.Set(f.tpl.Labels)) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// judgePDB requires exactly one PodDisruptionBudget for f with the floor of
// its tier, judged at n pods: one that keeps a replica and lets a drain evict
// one for a replica-safe workload, one that allows exactly one voluntary
// disruption for a quorum.
func (g *haGuarantee) judgePDB(f *haFound, n int) {
	if g.pdbErr != nil {
		return
	}
	want, required := "one with minAvailable 1", "minAvailable 1"
	if f.w.tier == haQuorum {
		want, required = "one with maxUnavailable 1", "exactly one voluntary disruption (maxUnavailable 1)"
	}
	matching := g.pdbsSelecting(f)
	switch len(matching) {
	case 0:
		g.find(haFails, haPDBAdvice, "%s: no PodDisruptionBudget selects its pods, mode %s requires %s", f.ref,
			g.mode, want)
	case 1:
		if problem := pdbFloorProblem(matching[0], n, f.w.tier); problem != "" {
			g.find(haFails, haPDBAdvice, "%s: PodDisruptionBudget %s %s, mode %s requires %s", f.ref,
				matching[0].Name, problem, g.mode, required)
		}
	default:
		var names []string
		for _, p := range matching {
			names = append(names, p.Name)
		}
		g.find(haFails, haPDBAdvice, "%s: PodDisruptionBudgets %s all select its pods, mode %s requires exactly "+
			"one; the eviction API refuses a pod that more than one selects", f.ref, strings.Join(names, ", "), g.mode)
	}
}

// pdbFloor is how many of n pods p keeps available, rounded up as the
// disruption controller rounds a percentage, with the setting that says so.
// known is false when p sets no floor or one that does not parse.
func pdbFloor(p *policyv1.PodDisruptionBudget, n int) (floor int, setting string, known bool) {
	switch {
	case p.Spec.MinAvailable != nil:
		setting = "minAvailable " + p.Spec.MinAvailable.String()
		v, err := intstr.GetScaledValueFromIntOrPercent(p.Spec.MinAvailable, n, true)
		if err != nil {
			return 0, "has an unreadable " + setting, false
		}
		return v, setting, true
	case p.Spec.MaxUnavailable != nil:
		setting = "maxUnavailable " + p.Spec.MaxUnavailable.String()
		v, err := intstr.GetScaledValueFromIntOrPercent(p.Spec.MaxUnavailable, n, true)
		if err != nil {
			return 0, "has an unreadable " + setting, false
		}
		return max(n-v, 0), setting, true
	}
	return 0, "sets neither minAvailable nor maxUnavailable", false
}

// pdbFloorProblem says why p's floor at n pods does not fit tier, or "".
func pdbFloorProblem(p *policyv1.PodDisruptionBudget, n int, tier haTier) string {
	floor, setting, known := pdbFloor(p, n)
	if !known {
		return setting
	}
	unit, allowed := "replicas", n-floor
	if tier == haQuorum {
		unit = "members"
	}
	switch {
	case tier != haQuorum && floor < 1:
		return fmt.Sprintf("%s keeps no pod available at %d %s", setting, n, unit)
	case allowed < 1:
		return fmt.Sprintf("%s allows no voluntary disruption at %d %s", setting, n, unit)
	case tier == haQuorum && allowed > 1:
		return fmt.Sprintf("%s allows %d voluntary disruptions at %d %s", setting, allowed, n, unit)
	}
	return ""
}

// judgeStrategy warns about a rollout strategy that drops a replica-safe
// workload below its Ready count. The default 25%/25% surges one and keeps
// every replica at two, so it is judged by what it does at want replicas.
func (g *haGuarantee) judgeStrategy(f *haFound, want int32) {
	s := f.deploy.Spec.Strategy
	desc := string(appsv1.RecreateDeploymentStrategyType)
	if s.Type != appsv1.RecreateDeploymentStrategyType {
		maxUnavailable, maxSurge := intstr.FromString("25%"), intstr.FromString("25%")
		if ru := s.RollingUpdate; ru != nil {
			if ru.MaxUnavailable != nil {
				maxUnavailable = *ru.MaxUnavailable
			}
			if ru.MaxSurge != nil {
				maxSurge = *ru.MaxSurge
			}
		}
		unavailable, uErr := intstr.GetScaledValueFromIntOrPercent(&maxUnavailable, int(want), false)
		surge, sErr := intstr.GetScaledValueFromIntOrPercent(&maxSurge, int(want), true)
		if uErr == nil && sErr == nil && unavailable == 0 && surge == 1 {
			return
		}
		desc = fmt.Sprintf("RollingUpdate maxSurge %s, maxUnavailable %s", maxSurge.String(), maxUnavailable.String())
	}
	g.find(haWarns, "", "%s: rollout strategy %s, mode %s expects RollingUpdate maxSurge 1, maxUnavailable 0, which "+
		"keeps every replica Ready through a rollout", f.ref, desc, g.mode)
}

// judgeRendered judges the placement f's template renders: hostname pod
// anti-affinity and a zone spread, each hard under enforced. A hostname spread
// stands in for a preferred anti-affinity term, not a required one: it lets
// two pods share a node when there are fewer nodes than pods.
func (g *haGuarantee) judgeRendered(f *haFound) {
	enforced := g.mode == HAModeEnforced
	switch hostnameSeparation(f.tpl, f.namespace) {
	case separationNone:
		want := "a preferred or required term"
		if enforced {
			want = "a required term"
		}
		g.find(haFailsEnforced, haPlacementAdvice, "%s: no hostname pod anti-affinity selects its pods, %s",
			f.ref, g.mode.asks(haFailsEnforced, want))
	case separationSoft:
		if enforced {
			g.find(haFailsEnforced, haPlacementAdvice, "%s: hostname pod anti-affinity is preferred only or a "+
				"spread, which lets pods share a node, mode enforced requires a required term", f.ref)
		}
	}
	switch zoneSpread(f.tpl) {
	case separationNone:
		want := "one (ScheduleAnyway or DoNotSchedule)"
		if enforced {
			want = "one with whenUnsatisfiable DoNotSchedule"
		}
		g.find(haFailsEnforced, haPlacementAdvice, "%s: no %s spread selects its pods, %s", f.ref,
			corev1.LabelTopologyZone, g.mode.asks(haFailsEnforced, want))
	case separationSoft:
		if enforced {
			g.find(haFailsEnforced, haPlacementAdvice, "%s: %s spread is ScheduleAnyway, mode enforced requires "+
				"DoNotSchedule", f.ref, corev1.LabelTopologyZone)
		}
	}
}

// judgeObserved judges where f's Ready pods run: no two quorum members on one
// node, a replica-safe workload not all on one node, neither all in one zone
// of a cluster with several, and a quorum not crowded into one zone.
func (g *haGuarantee) judgeObserved(f *haFound, unit string) {
	if !f.podsRead {
		return
	}
	byNode := map[string][]string{}
	for _, p := range f.ready {
		byNode[p.Spec.NodeName] = append(byNode[p.Spec.NodeName], p.Name)
	}
	nodes := slices.Sorted(maps.Keys(byNode))
	if f.w.tier == haQuorum {
		for _, n := range nodes {
			if pods := byNode[n]; len(pods) > 1 {
				g.find(haFailsEnforced, haCoLocationAdvice, "%s: Ready members %s share node %s, %s", f.ref,
					joinAnd(pods), n, g.mode.asks(haFailsEnforced, "one member per node"))
			}
		}
	} else if len(f.ready) > 1 && len(nodes) == 1 {
		g.find(haFailsEnforced, haCoLocationAdvice, "%s: all %d Ready replicas run on node %s, %s", f.ref,
			len(f.ready), nodes[0], g.mode.asks(haFailsEnforced, "them on distinct nodes"))
		return
	}
	g.judgeZoneConfinement(f, unit)
}

// judgeZoneConfinement reports f's Ready pods all in one zone while the
// cluster has several, and a quorum's crowded into one zone (judgeZoneSkew).
// A pod on a node whose zone is unknown leaves it unjudged: the zone-label
// finding covers that node.
func (g *haGuarantee) judgeZoneConfinement(f *haFound, unit string) {
	zones := g.clusterZones()
	if g.nodesErr != nil || len(f.ready) < 2 || len(zones) < 2 {
		return
	}
	zoneOf := map[string]string{}
	for i := range g.nodes {
		zoneOf[g.nodes[i].Name] = g.nodes[i].Labels[corev1.LabelTopologyZone]
	}
	perZone := map[string]int{}
	for _, p := range f.ready {
		z := zoneOf[p.Spec.NodeName]
		if z == "" {
			return
		}
		perZone[z]++
	}
	if len(perZone) == 1 {
		used := slices.Collect(maps.Keys(perZone))
		g.find(haFailsEnforced, haCoLocationAdvice, "%s: all %d Ready %s run in zone %s, one of the cluster's %d "+
			"zones, %s", f.ref, len(f.ready), unit, used[0], len(zones),
			g.mode.asks(haFailsEnforced, "them across zones"))
		return
	}
	if f.w.tier == haQuorum {
		g.judgeZoneSkew(f, perZone)
	}
}

// judgeZoneSkew reports a quorum whose Ready members crowd one zone: more than
// one more there than in another zone of the nodes it can run on, which the
// DoNotSchedule zone spread of maxSkew 1 that enforced renders forbids, and
// which loses the quorum with the crowded zone. A member down leaves its zone
// short until it is back, so the skew is judged with every member Ready; the
// readiness finding reports the member down.
//
// Replica-safe Deployments are left to the one-node and one-zone rules: a
// rollout or a scale-down leaves a skew among their replicas that the next
// rollout evens out, and any one zone loss still leaves them a replica.
func (g *haGuarantee) judgeZoneSkew(f *haFound, perZone map[string]int) {
	if f.sts.Spec.Replicas == nil || len(f.ready) < int(*f.sts.Spec.Replicas) || len(f.ready) < len(f.pods) {
		return
	}
	counts := maps.Clone(perZone)
	for i := range g.nodes {
		n := &g.nodes[i]
		z := n.Labels[corev1.LabelTopologyZone]
		if _, seen := counts[z]; !seen && z != "" && schedulableFor(n, f.tpl) {
			counts[z] = 0
		}
	}
	zones := slices.Sorted(maps.Keys(counts))
	most, least := counts[zones[0]], counts[zones[0]]
	for _, z := range zones {
		most, least = max(most, counts[z]), min(least, counts[z])
	}
	if most-least <= 1 {
		return
	}
	var listed, crowded, short []string
	for _, z := range zones {
		listed = append(listed, fmt.Sprintf("%s %d", z, counts[z]))
		switch counts[z] {
		case most:
			crowded = append(crowded, z)
		case least:
			short = append(short, z)
		}
	}
	g.find(haFailsEnforced, haZoneSkewAdvice, "%s: Ready members per zone %s, crowding %s over %s, %s", f.ref,
		strings.Join(listed, ", "), joinAnd(crowded), joinAnd(short), g.mode.asks(haFailsEnforced,
			"a zone spread of maxSkew 1: at most one more member in any zone than in another"))
}

// clusterZones are the zones of the cluster's Ready, schedulable nodes.
func (g *haGuarantee) clusterZones() []string {
	var zones []string
	for i := range g.nodes {
		n := &g.nodes[i]
		if z := n.Labels[corev1.LabelTopologyZone]; z != "" && isNodeReady(n) && !n.Spec.Unschedulable &&
			!slices.Contains(zones, z) {
			zones = append(zones, z)
		}
	}
	return zones
}

// judgeZoneLabels requires a zone label on every node that runs a replica-safe
// or quorum pod, or that one of them could be placed on.
func (g *haGuarantee) judgeZoneLabels() {
	if g.nodesErr != nil {
		return
	}
	hosts := map[string]bool{}
	var placed []*haFound
	for _, f := range g.found {
		if f.w.tier == haSingleton {
			continue
		}
		placed = append(placed, f)
		for _, p := range f.pods {
			hosts[p.Spec.NodeName] = true
		}
	}
	var unlabelled []string
	for i := range g.nodes {
		n := &g.nodes[i]
		if n.Labels[corev1.LabelTopologyZone] != "" {
			continue
		}
		if hosts[n.Name] || slices.ContainsFunc(placed, func(f *haFound) bool { return schedulableFor(n, f.tpl) }) {
			unlabelled = append(unlabelled, n.Name)
		}
	}
	if len(unlabelled) == 0 {
		return
	}
	sort.Strings(unlabelled)
	g.find(haFailsEnforced, haZoneLabelAdvice, "nodes %s have no %s label though they run or can run the HA "+
		"workloads, %s", strings.Join(unlabelled, ", "), corev1.LabelTopologyZone,
		g.mode.asks(haFailsEnforced, "every such node labelled"))
}

// joinAnd joins names as "a and b" or "a, b and c".
func joinAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func (g *haGuarantee) setOK(ok bool) { g.state.HAGuaranteeOK = &ok }

func (g *haGuarantee) verdict() {
	against := "mode " + string(g.mode)
	if g.mode == "" {
		against = "their rendered placement"
	}
	switch {
	case len(g.failures) > 0:
		printError(g.log, fmt.Sprintf("HA Guarantee findings (%d), each short of what %s requires:",
			len(g.failures), against))
		for _, f := range g.failures {
			printInfo(g.log, "  "+f)
		}
		g.state.Recommendations = append(g.state.Recommendations, g.advice...)
		g.setOK(false)
	case g.unobserved > 0:
		printWarning(g.log, "The HA guarantee could not be fully checked; see the warnings")
	case len(g.found) == 0:
		printInfo(g.log, "  No HA workload found (pre-install state)")
		if g.mode == "" {
			// Its summary key is left out, so a warning says why.
			g.find(haWarns, "", "rendered placement not assessed: no HA workload was found, and no HA mode was "+
				"supplied to judge one against")
		}
		g.setOK(true)
	case g.mode == "" && g.state.HAPlacementAssessed == 0:
		// Its summary key is left out, so a warning says why.
		g.find(haWarns, "", "rendered placement not assessed: none of the %d HA workload(s) renders hostname pod "+
			"anti-affinity or a hostname spread for its own pods, and no HA mode was supplied to judge them "+
			"against", len(g.found))
		g.setOK(true)
	default:
		msg := fmt.Sprintf("All %d HA workload(s) meet %s", len(g.found), against)
		if g.mode == "" {
			msg = fmt.Sprintf("All %d assessed HA workload(s) meet %s", g.state.HAPlacementAssessed, against)
			if n := g.state.HAPlacementNotAssessed; n > 0 {
				msg += fmt.Sprintf(" (%d not assessed: no rendered terms)", n)
			}
		}
		if g.warned > 0 {
			msg += fmt.Sprintf(", with %d warning(s)", g.warned)
		}
		printSuccess(g.log, msg)
		g.setOK(true)
	}
}

// haPlacementUnassessed reports a run without an HA mode whose HA Guarantee
// row passed having judged no workload: none renders terms to judge it by.
func haPlacementUnassessed(state *ValidationState) bool {
	return state.HAMode == "" && state.HAGuaranteeOK != nil && *state.HAGuaranteeOK && state.HAPlacementAssessed == 0
}

// haGuaranteeRow is the summary row of the HA Guarantee check. Without an HA
// mode it judges the rendered placement only, says how many workloads that
// covered, and that none was assessed when none renders such terms.
func haGuaranteeRow(state *ValidationState) summaryRow {
	if state.HAMode == HAModeNone {
		return haModeAbsentRow(haGuaranteeLabel, state.HAMode)
	}
	if state.HAGuaranteeOK == nil {
		return summaryRow{Critical: true, Unknown: true, UnknownMsg: haGuaranteeLabel + notFullyObserved}
	}
	if haPlacementUnassessed(state) {
		return summaryRow{Passed: true, NotApplicable: true,
			NAMsg: haGuaranteeLabel + ": Rendered Placement Not Assessed (HA mode not supplied)"}
	}
	pass, fail := "Met for mode "+string(state.HAMode), "Not Met for mode "+string(state.HAMode)
	if state.HAMode == "" {
		pass = fmt.Sprintf("Rendered Placement Met (%d assessed", state.HAPlacementAssessed)
		if n := state.HAPlacementNotAssessed; n > 0 {
			pass += fmt.Sprintf(", %d not assessed: no rendered terms", n)
		}
		pass, fail = pass+")", "Rendered Placement Not Met (HA mode not supplied)"
	}
	if slices.ContainsFunc(state.Warnings, func(w string) bool { return strings.HasPrefix(w, haGuaranteeLabel+": ") }) {
		pass += " (with warnings)"
	}
	return summaryRow{Passed: *state.HAGuaranteeOK, PassMsg: haGuaranteeLabel + ": " + pass,
		FailMsg: haGuaranteeLabel + ": " + fail, Critical: true}
}

// haGuaranteeCheck is the HA Guarantee check of the control-plane set. It is
// critical unless the mode is none, the only one it does not run under. It
// judges the workloads the Tier-1 and Tier-2 checks judge, so a recheck runs
// the three together.
func (su *suite) haGuaranteeCheck() check {
	return check{
		name:   haGuaranteeLabel,
		linked: []string{tier1CheckName, tier2CheckName},
		run:    func(ctx context.Context, s *ValidationState) { checkHAGuarantee(ctx, su.client, s) },
		adopt: func(dst, src *ValidationState) {
			dst.HAGuaranteeOK = src.HAGuaranteeOK
			dst.HAPlacementAssessed, dst.HAPlacementNotAssessed = src.HAPlacementAssessed, src.HAPlacementNotAssessed
		},
		critical: func(s *ValidationState) outcome {
			if s.HAMode == HAModeNone {
				return passed
			}
			return rowOutcome(s.HAGuaranteeOK)
		},
	}
}
