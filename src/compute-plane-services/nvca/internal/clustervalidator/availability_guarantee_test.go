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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

var haModes = []HAMode{HAModePreferred, HAModeEnforced}

// haAsk is how a placement or zone finding words what mode asks for: a
// requirement under enforced, where it fails, and a best-effort ask under
// preferred, where it only warns.
func haAsk(mode HAMode, what string) string {
	if mode == HAModeEnforced {
		return "mode enforced requires " + what
	}
	return "mode " + string(mode) + " asks for " + what + " (best-effort)"
}

// haOutcome asserts the row's result, and that its findings and its warnings
// are exactly those listed, each named by a prefix.
func haOutcome(t *testing.T, name string, state *ValidationState, log string, ok bool,
	failures, warnings []string) {
	t.Helper()
	require.NotNil(t, state.HAGuaranteeOK, name)
	assert.Equal(t, ok, *state.HAGuaranteeOK, name)
	matchPrefixes(t, name+": findings", failures, haFindings(log))
	var got []string
	for _, w := range state.Warnings {
		if rest, found := strings.CutPrefix(w, haGuaranteeLabel+": "); found {
			got = append(got, rest)
		}
	}
	matchPrefixes(t, name+": warnings", warnings, got)
	if ok {
		assert.Empty(t, state.Recommendations, name)
	}
}

// haFindings are the findings the row lists under its failure heading.
func haFindings(log string) []string {
	_, rest, found := strings.Cut(log, "HA Guarantee findings (")
	if !found {
		return nil
	}
	var out []string
	for _, line := range strings.Split(rest, "\n")[1:] {
		if f, ok := strings.CutPrefix(line, iconInfo+"    "); ok {
			out = append(out, f)
		}
	}
	return out
}

// matchPrefixes requires got to hold one entry starting with each of want,
// and nothing else.
func matchPrefixes(t *testing.T, name string, want, got []string) {
	t.Helper()
	rest := slices.Clone(got)
	for _, w := range want {
		i := slices.IndexFunc(rest, func(g string) bool { return strings.HasPrefix(g, w) })
		if assert.GreaterOrEqual(t, i, 0, "%s: no %q among %q", name, w, got) {
			rest = slices.Delete(rest, i, i+1)
		}
	}
	assert.Empty(t, rest, "%s: not expected", name)
}

func TestCheckHAGuarantee_HealthyInstallPasses(t *testing.T) {
	for _, mode := range haModes {
		state, log := newHAFixture(t, mode).run(t, false, nil)
		haOutcome(t, string(mode), state, log, true, nil, nil)
		assert.Empty(t, state.Warnings, mode)
		assert.Contains(t, log, fmt.Sprintf("%d HA workload(s) meet mode %s", len(haWorkloads), mode))
	}
}

// Under mode none the row is not run at all: it reads nothing, so an older
// launcher's RBAC without PodDisruptionBudgets cannot turn it unknown.
func TestCheckHAGuarantee_DoesNotRunUnderModeNone(t *testing.T) {
	f := newHAFixture(t, HAModeEnforced)
	f.mode = HAModeNone
	var client *fake.Clientset
	state, _ := f.run(t, false, func(c *fake.Clientset) { client = c })
	assert.Nil(t, state.HAGuaranteeOK)
	assert.Empty(t, state.Warnings)
	assert.Empty(t, client.Actions(), "no read under mode none")
}

// Without an HA mode the row judges only where each replica-safe Deployment's
// Ready replicas run, as its own rendered terms ask: a required hostname term
// as enforced, a failure; a preferred term or a hostname spread as preferred,
// a warning; neither, not assessed. Sizing, budgets, zones, a missing release
// and the quorum StatefulSets, which the Tier-2 row judges, are not graded,
// and it reads only Deployments and pods, which every launcher grants.
func TestCheckHAGuarantee_WithoutAModeJudgesRenderedPlacementOnly(t *testing.T) {
	notSupplied := func(rendered HAMode) *haFixture {
		f := newHAFixture(t, rendered)
		f.mode = ""
		return f
	}

	f := notSupplied(HAModeEnforced)
	*f.deploys["reval"].Spec.Replicas = 1
	f.remove("notary-service")
	f.pdbs = nil
	f.template("ess-api").Spec.TopologySpreadConstraints = nil
	f.place("nats", "node-a", "node-a", "node-b")
	f.nodes = append(f.nodes, haNode("node-d", ""))
	var client *fake.Clientset
	state, log := f.run(t, false, func(c *fake.Clientset) { client = c })
	haOutcome(t, "weakened but spread", state, log, true, nil, nil)
	assert.Contains(t, log, "All 10 assessed HA workload(s) meet their rendered placement\x1b")
	assert.Equal(t, [2]int{10, 0}, [2]int{state.HAPlacementAssessed, state.HAPlacementNotAssessed})
	for _, a := range client.Actions() {
		assert.Contains(t, []string{"deployments", "pods"}, a.GetResource().Resource, "reads only what it judges")
		assert.Equal(t, "list", a.GetVerb())
	}

	f = notSupplied(HAModeEnforced)
	f.place("api", "node-a", "node-a")
	state, log = f.run(t, false, nil)
	haOutcome(t, "required term", state, log, false, []string{"nvcf/nvcf-api: all 2 Ready replicas run on node " +
		"node-a, which its required hostname pod anti-affinity forbids (HA mode not supplied, judged as mode " +
		"enforced)"}, nil)
	assert.Contains(t, log, "each short of what their rendered placement requires")
	assert.Equal(t, []string{haCoLocationAdvice}, state.Recommendations)

	f = notSupplied(HAModePreferred)
	f.place("api", "node-a", "node-a")
	state, log = f.run(t, false, nil)
	haOutcome(t, "preferred term", state, log, true, nil, []string{"nvcf/nvcf-api: all 2 Ready replicas run on " +
		"node node-a; its preferred hostname pod anti-affinity or hostname spread asks for distinct nodes " +
		"(best-effort; HA mode not supplied, judged as mode preferred)"})

	f = notSupplied(HAModePreferred)
	f.template("api").Spec.Affinity = nil
	f.place("api", "node-a", "node-a")
	state, log = f.run(t, false, nil)
	haOutcome(t, "no term", state, log, true, nil, nil)
	assert.Contains(t, log, "Placement not assessed (no hostname pod anti-affinity or spread selects their own "+
		"pods): nvcf/nvcf-api")
	assert.Contains(t, log, "All 10 assessed HA workload(s) meet their rendered placement (1 not assessed: no "+
		"rendered terms)")
	assert.Equal(t, [2]int{10, 1}, [2]int{state.HAPlacementAssessed, state.HAPlacementNotAssessed})

	f = notSupplied(HAModePreferred)
	for _, d := range f.deploys {
		d.Spec.Template.Spec.Affinity, d.Spec.Template.Spec.TopologySpreadConstraints = nil, nil
	}
	state, log = f.run(t, false, nil)
	haOutcome(t, "no workload renders terms", state, log, true, nil, []string{"rendered placement not assessed: " +
		"none of the 11 HA workload(s) renders hostname pod anti-affinity or a hostname spread for its own pods, " +
		"and no HA mode was supplied to judge them against"})
	assert.NotContains(t, log, "meet their rendered placement")
	assert.Equal(t, [2]int{0, 11}, [2]int{state.HAPlacementAssessed, state.HAPlacementNotAssessed})

	// With no HA workload found the summary has no ha_guarantee key either, so
	// a warning on its row says why.
	f = notSupplied(HAModePreferred)
	for _, w := range haWorkloads {
		f.remove(w.release)
	}
	state, log = f.run(t, false, nil)
	haOutcome(t, "no workload found", state, log, true, nil, []string{"rendered placement not assessed: no HA " +
		"workload was found, and no HA mode was supplied to judge one against"})
	_, present := buildSummary(state, time.Now(), true, VerdictReady).Checks[CheckKeyHAGuarantee]
	assert.False(t, present)

	f = notSupplied(HAModeEnforced)
	f.place("grpc-proxy", "node-a")
	state, log = f.run(t, false, func(c *fake.Clientset) {
		c.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
		})
	})
	assert.Nil(t, state.HAGuaranteeOK, "pods it could not read leave the placement unknown")
	assert.NotEmpty(t, state.Warnings)
	assert.Contains(t, log, "HA Guarantee (HA mode not supplied: rendered placement only)")
}

// Sizing fails under both HA modes, and each finding names the workload, what
// it has and what the mode requires.
func TestCheckHAGuarantee_SizingFailsUnderBothModes(t *testing.T) {
	cases := map[string]struct {
		change func(*haFixture)
		want   string
		// placement are the findings that fail under enforced and warn under
		// preferred beside it.
		placement []string
	}{
		"replica-safe below two": {change: func(f *haFixture) {
			one := int32(1)
			d := f.deploys["api"]
			d.Spec.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.Replicas = &one, 1, 1, 1
			f.pods["api"] = f.pods["api"][:1]
		}, want: "nvcf/nvcf-api: spec.replicas 1, mode %s requires at least 2"},
		"replica-safe with one Ready": {change: func(f *haFixture) {
			f.deploys["ess-api"].Status.ReadyReplicas = 1
			f.pods["ess-api"][1].Status = readyPodStatus(false)
		}, want: "ess/ess-api: Ready 1, mode %s requires at least 2 Ready"},
		"named exception scaled out": {change: func(f *haFixture) {
			two := int32(2)
			d := f.deploys["grpc-proxy"]
			d.Spec.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas = &two, 2, 2
		}, want: "nvcf/grpc-proxy: spec.replicas 2, mode %s requires exactly 1, since its callbacks are bound to " +
			"one pod"},
		"named exception down": {change: func(f *haFixture) {
			f.deploys["invocation-service"].Status.ReadyReplicas = 0
			f.pods["invocation-service"][0].Status = readyPodStatus(false)
		}, want: "nvcf/invocation-service: Ready 0, mode %s requires its replica Ready"},
		"quorum below three, without anti-affinity": {change: func(f *haFixture) {
			one := int32(1)
			s := f.sets["cassandra"]
			s.Spec.Replicas, s.Status.ReadyReplicas, s.Status.Replicas = &one, 1, 1
			s.Spec.Template.Spec.Affinity, s.Spec.Template.Spec.TopologySpreadConstraints = nil, nil
			f.pods["cassandra"] = f.pods["cassandra"][:1]
		}, want: "cassandra-system/cassandra: spec.replicas 1, mode %s requires at least 3 members",
			placement: []string{"cassandra-system/cassandra: no hostname pod anti-affinity selects its pods",
				"cassandra-system/cassandra: no topology.kubernetes.io/zone spread selects its pods"}},
		"quorum member down": {change: func(f *haFixture) {
			f.sets["nats"].Status.ReadyReplicas = 2
			f.pods["nats"][2].Status = readyPodStatus(false)
		}, want: "nats-system/nats: Ready 2 of 3, mode %s requires every member Ready"},
	}
	for name, tc := range cases {
		for _, mode := range haModes {
			f := newHAFixture(t, mode)
			tc.change(f)
			state, log := f.run(t, false, nil)
			failures, warnings := []string{fmt.Sprintf(tc.want, mode)}, tc.placement
			if mode == HAModeEnforced {
				failures, warnings = append(failures, tc.placement...), nil
			}
			haOutcome(t, name+"/"+string(mode), state, log, false, failures, warnings)
			assert.Contains(t, state.Recommendations, haSizingAdvice, name)
		}
	}
}

// A rollout the Tier-1 or Tier-2 row tolerates is not a sizing failure here,
// and a quorum deliberately larger than three is not one either.
func TestCheckHAGuarantee_SizingToleratesRolloutsAndLargerQuorums(t *testing.T) {
	f := newHAFixture(t, HAModePreferred)
	d := f.deploys["api"]
	one := intstr.FromInt32(1)
	d.Spec.Strategy.RollingUpdate.MaxUnavailable = &one
	d.Generation, d.Status.ObservedGeneration, d.Status.ReadyReplicas, d.Status.UpdatedReplicas = 2, 2, 1, 1
	d.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing,
		Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"}}
	f.pods["api"][1].Status = readyPodStatus(false)
	s := f.sets["nats"]
	s.Status.UpdateRevision, s.Status.ReadyReplicas = "nats-r2", 2
	f.pods["nats"][2].Status = readyPodStatus(false)
	five := int32(5)
	c := f.sets["cassandra"]
	c.Spec.Replicas, c.Status.ReadyReplicas, c.Status.Replicas = &five, 5, 5
	controller := true
	for i := 3; i < 5; i++ {
		f.pods["cassandra"] = append(f.pods["cassandra"], &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cassandra-%d", i), Namespace: "cassandra-system",
				Labels: c.Spec.Template.Labels, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1",
					Kind: "StatefulSet", Name: "cassandra", UID: c.UID, Controller: &controller}}},
			Spec: corev1.PodSpec{NodeName: fmt.Sprintf("node-%d", i)}, Status: readyPodStatus(true),
		})
		f.nodes = append(f.nodes, haNode(fmt.Sprintf("node-%d", i), fmt.Sprintf("zone-%d", i)))
	}

	state, log := f.run(t, false, nil)
	haOutcome(t, "rollouts", state, log, true, nil, []string{"nvcf/nvcf-api: rollout strategy RollingUpdate " +
		"maxSurge 1, maxUnavailable 1, mode preferred expects"})
	assert.NotContains(t, log, "requires at least 2 Ready")
	assert.NotContains(t, log, "requires every member Ready")
	assert.Contains(t, log, "cassandra-system/cassandra: 5 members, above the floor of 3")
}

// PodDisruptionBudgets fail under both modes when one is missing, when several
// select the same pods, or when the floor is wrong for the tier.
func TestCheckHAGuarantee_PodDisruptionBudgets(t *testing.T) {
	int32p := func(v int32) *intstr.IntOrString { i := intstr.FromInt32(v); return &i }
	cases := map[string]struct {
		change func(*haFixture)
		want   string
	}{
		"replica-safe without a PDB": {func(f *haFixture) { delete(f.pdbs, "api") },
			"nvcf/nvcf-api: no PodDisruptionBudget selects its pods, mode %s requires one with minAvailable 1"},
		"quorum without a PDB": {func(f *haFixture) { delete(f.pdbs, "cassandra") },
			"cassandra-system/cassandra: no PodDisruptionBudget selects its pods, mode %s requires one with " +
				"maxUnavailable 1"},
		"PDB in another namespace": {func(f *haFixture) { f.pdbs["api"].Namespace = "ess" },
			"nvcf/nvcf-api: no PodDisruptionBudget selects its pods"},
		"PDB selecting only a look-alike": {func(f *haFixture) {
			f.pdbs["nats"].Spec.Selector.MatchLabels["app.kubernetes.io/component"] = "nats-box"
		}, "nats-system/nats: no PodDisruptionBudget selects its pods"},
		"two PDBs": {func(f *haFixture) {
			extra := f.pdbs["reval"].DeepCopy()
			extra.Name = "reval-extra"
			f.extra = append(f.extra, extra)
		}, "nvcf/reval: PodDisruptionBudgets reval, reval-extra all select its pods, mode %s requires exactly one"},
		"replica-safe floor too high": {func(f *haFixture) { f.pdbs["api"].Spec.MinAvailable = int32p(2) },
			"nvcf/nvcf-api: PodDisruptionBudget nvcf-api minAvailable 2 allows no voluntary disruption at 2 " +
				"replicas, mode %s requires minAvailable 1"},
		"replica-safe floor of zero": {func(f *haFixture) {
			f.pdbs["api"].Spec.MinAvailable, f.pdbs["api"].Spec.MaxUnavailable = nil, int32p(2)
		}, "nvcf/nvcf-api: PodDisruptionBudget nvcf-api maxUnavailable 2 keeps no pod available at 2 replicas"},
		"PDB with no floor": {func(f *haFixture) { f.pdbs["api"].Spec.MinAvailable = nil },
			"nvcf/nvcf-api: PodDisruptionBudget nvcf-api sets neither minAvailable nor maxUnavailable"},
		"quorum allows two": {func(f *haFixture) { f.pdbs["cassandra"].Spec.MaxUnavailable = int32p(2) },
			"cassandra-system/cassandra: PodDisruptionBudget cassandra maxUnavailable 2 allows 2 voluntary " +
				"disruptions at 3 members, mode %s requires exactly one voluntary disruption (maxUnavailable 1)"},
		"quorum allows none": {func(f *haFixture) {
			f.pdbs["cassandra"].Spec.MaxUnavailable, f.pdbs["cassandra"].Spec.MinAvailable = nil, int32p(3)
		}, "cassandra-system/cassandra: PodDisruptionBudget cassandra minAvailable 3 allows no voluntary " +
			"disruption at 3 members"},
	}
	for name, tc := range cases {
		for _, mode := range haModes {
			f := newHAFixture(t, mode)
			tc.change(f)
			state, log := f.run(t, false, nil)
			want := tc.want
			if strings.Contains(want, "%s") {
				want = fmt.Sprintf(want, mode)
			}
			haOutcome(t, name+"/"+string(mode), state, log, false, []string{want}, nil)
			assert.Contains(t, state.Recommendations, haPDBAdvice, name)
		}
	}

	// Equivalent floors pass: maxUnavailable 1 on two replicas, and
	// minAvailable 2 on a three-member quorum.
	f := newHAFixture(t, HAModeEnforced)
	f.pdbs["api"].Spec.MinAvailable, f.pdbs["api"].Spec.MaxUnavailable = nil, int32p(1)
	f.pdbs["cassandra"].Spec.MaxUnavailable, f.pdbs["cassandra"].Spec.MinAvailable = nil, int32p(2)
	state, log := f.run(t, false, nil)
	haOutcome(t, "equivalent floors", state, log, true, nil, nil)
}

// A named singleton's PDB that allows no disruption blocks every node drain:
// a warning, since the singleton has no second replica to protect.
func TestCheckHAGuarantee_SingletonPDBThatBlocksDrainsWarns(t *testing.T) {
	for _, mode := range haModes {
		f := newHAFixture(t, mode)
		one := intstr.FromInt32(1)
		labels := f.deploys["grpc-proxy"].Spec.Template.Labels
		f.extra = append(f.extra, &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "grpc-proxy", Namespace: "nvcf"},
			Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &one,
				Selector: &metav1.LabelSelector{MatchLabels: labels}},
		})
		state, log := f.run(t, false, nil)
		haOutcome(t, string(mode), state, log, true, nil, []string{"nvcf/grpc-proxy: PodDisruptionBudget " +
			"grpc-proxy minAvailable 1 allows no voluntary disruption at 1 replica, so it blocks node drains"})
	}
}

// A PodDisruptionBudget list that fails is not a pass: the row is unknown with
// a warning, unless a finding it did observe already fails it.
func TestCheckHAGuarantee_UnreadablePDBsAreUnknown(t *testing.T) {
	deny := func(c *fake.Clientset) {
		c.PrependReactor("list", "poddisruptionbudgets", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "policy", Resource: "poddisruptionbudgets"}, "", fmt.Errorf("denied"))
		})
	}
	state, _ := newHAFixture(t, HAModePreferred).run(t, false, deny)
	assert.Nil(t, state.HAGuaranteeOK, "a denied PDB list must not reach a pass")
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"HA Guarantee: status unknown (could not read PodDisruptionBudgets: ")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "grant the cluster-validator ServiceAccount")

	f := newHAFixture(t, HAModePreferred)
	two := int32(2)
	f.deploys["grpc-proxy"].Spec.Replicas = &two
	state, _ = f.run(t, false, deny)
	require.NotNil(t, state.HAGuaranteeOK)
	assert.False(t, *state.HAGuaranteeOK, "an observed sizing failure stands beside the unread PDBs")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "could not read PodDisruptionBudgets")
}

// Rendered placement warns under preferred and fails under enforced: missing
// anti-affinity or zone spread, or, under enforced, a soft one.
func TestCheckHAGuarantee_RenderedPlacement(t *testing.T) {
	soften := func(f *haFixture, release string) {
		affinity, spread := haPlacement(HAModePreferred, map[string]string{haInstanceLabel: release})
		f.template(release).Spec.Affinity, f.template(release).Spec.TopologySpreadConstraints = affinity, spread
	}
	strip := func(f *haFixture, release string) {
		f.template(release).Spec.Affinity, f.template(release).Spec.TopologySpreadConstraints = nil, nil
	}

	f := newHAFixture(t, HAModePreferred)
	strip(f, "api")
	strip(f, "openbao-server")
	state, log := f.run(t, false, nil)
	haOutcome(t, "preferred, stripped", state, log, true, nil, []string{
		"nvcf/nvcf-api: no hostname pod anti-affinity selects its pods, mode preferred asks for a preferred " +
			"or required term (best-effort)",
		"nvcf/nvcf-api: no topology.kubernetes.io/zone spread selects its pods, mode preferred asks for one " +
			"(ScheduleAnyway or DoNotSchedule) (best-effort)",
		"vault-system/openbao-server: no hostname pod anti-affinity selects its pods, mode preferred " +
			"asks for a preferred or required term (best-effort)",
		"vault-system/openbao-server: no topology.kubernetes.io/zone spread selects its pods",
	})

	f = newHAFixture(t, HAModeEnforced)
	soften(f, "api")
	strip(f, "nats")
	state, log = f.run(t, false, nil)
	haOutcome(t, "enforced", state, log, false, []string{
		"nvcf/nvcf-api: hostname pod anti-affinity is preferred only or a spread, which lets pods share a node, " +
			"mode enforced requires a required term",
		"nvcf/nvcf-api: topology.kubernetes.io/zone spread is ScheduleAnyway, mode enforced requires DoNotSchedule",
		"nats-system/nats: no hostname pod anti-affinity selects its pods, mode enforced requires a required term",
		"nats-system/nats: no topology.kubernetes.io/zone spread selects its pods, mode enforced requires one " +
			"with whenUnsatisfiable DoNotSchedule",
	}, nil)
	assert.Contains(t, state.Recommendations, haPlacementAdvice)

	// Named singletons are not judged on placement.
	for _, mode := range haModes {
		f = newHAFixture(t, mode)
		strip(f, "grpc-proxy")
		strip(f, "invocation-service")
		state, log = f.run(t, false, nil)
		haOutcome(t, "singletons/"+string(mode), state, log, true, nil, nil)
		assert.Empty(t, state.Warnings, mode)
	}
}

// Observed placement: quorum peers sharing a node, a replica-safe workload on
// one node, or one zone of several. A warning under preferred, a failure under
// enforced, which is the Tier-2 co-location this row now judges.
func TestCheckHAGuarantee_ObservedPlacement(t *testing.T) {
	cases := map[string]struct {
		change      func(*haFixture)
		found, asks string
		// also is a finding that follows from the same placement.
		also string
	}{
		"quorum peers share a node": {func(f *haFixture) { f.place("nats", "node-a", "node-a", "node-b") },
			"nats-system/nats: Ready members nats-0 and nats-1 share node node-a", "one member per node",
			"nats-system/nats: Ready members per zone zone-a 2, zone-b 1, zone-c 0, crowding zone-a over zone-c"},
		"replica-safe on one node": {func(f *haFixture) { f.place("api", "node-a", "node-a") },
			"nvcf/nvcf-api: all 2 Ready replicas run on node node-a", "them on distinct nodes", ""},
		"replica-safe in one zone": {func(f *haFixture) {
			f.nodes = append(f.nodes, haNode("node-a2", "zone-a"))
			f.place("reval", "node-a", "node-a2")
		}, "nvcf/reval: all 2 Ready replicas run in zone zone-a, one of the cluster's 3 zones", "them across zones",
			""},
		"quorum in one zone": {func(f *haFixture) {
			f.nodes = append(f.nodes, haNode("node-a2", "zone-a"), haNode("node-a3", "zone-a"))
			f.place("cassandra", "node-a", "node-a2", "node-a3")
		}, "cassandra-system/cassandra: all 3 Ready members run in zone zone-a, one of the cluster's 3 zones",
			"them across zones", ""},
	}
	for name, tc := range cases {
		for _, mode := range haModes {
			f := newHAFixture(t, mode)
			tc.change(f)
			state, log := f.run(t, false, nil)
			want := []string{tc.found + ", " + haAsk(mode, tc.asks)}
			if tc.also != "" {
				want = append(want, tc.also)
			}
			if mode == HAModeEnforced {
				haOutcome(t, name+"/enforced", state, log, false, want, nil)
				assert.Contains(t, state.Recommendations, haCoLocationAdvice, name)
				continue
			}
			haOutcome(t, name+"/preferred", state, log, true, nil, want)
			for _, marker := range cliRolloutMarkers {
				assert.NotContains(t, strings.Join(state.Warnings, "; "), marker, "%s: not a rollout", name)
			}
		}
	}

	// One zone in the whole cluster is no zone finding: the HA Topology row
	// reports a cluster short of zones.
	f := newHAFixture(t, HAModeEnforced)
	for _, n := range f.nodes {
		n.Labels[corev1.LabelTopologyZone] = "zone-a"
	}
	state, log := f.run(t, false, nil)
	haOutcome(t, "single zone", state, log, true, nil, nil)
}

// addMembers grows release's quorum by one Ready member on each of nodes.
func (f *haFixture) addMembers(release string, nodes ...string) {
	s := f.sets[release]
	n := *s.Spec.Replicas + int32(len(nodes))
	s.Spec.Replicas, s.Status.Replicas, s.Status.ReadyReplicas, s.Status.UpdatedReplicas = &n, n, n, n
	for _, node := range nodes {
		p := f.pods[release][0].DeepCopy()
		p.Name, p.Spec.NodeName = fmt.Sprintf("%s-%d", s.Name, len(f.pods[release])), node
		f.pods[release] = append(f.pods[release], p)
	}
}

// A quorum crowded into one zone, more than one member more there than in
// another zone its nodes span, breaks the DoNotSchedule maxSkew 1 zone spread
// enforced renders: losing the crowded zone loses the quorum. It fails under
// enforced and warns under preferred, naming the crowded and the short zones.
// Zones the quorum cannot be placed in do not count, a member down leaves the
// skew unjudged until it is back, and replica-safe Deployments keep the
// one-node and one-zone rules: a Deployment rollout or scale-down leaves a
// skew the next rollout evens out, and any one zone loss leaves it a replica.
func TestCheckHAGuarantee_QuorumZoneSkew(t *testing.T) {
	const skewAsk = "a zone spread of maxSkew 1: at most one more member in any zone than in another"
	for _, mode := range haModes {
		f := newHAFixture(t, mode)
		f.nodes = append(f.nodes, haNode("node-b2", "zone-b"))
		f.place("openbao-server", "node-b", "node-a", "node-b2")
		state, log := f.run(t, false, nil)
		want := "vault-system/openbao-server: Ready members per zone zone-a 1, zone-b 2, zone-c 0, crowding " +
			"zone-b over zone-c, " + haAsk(mode, skewAsk)
		if mode == HAModeEnforced {
			haOutcome(t, "skew/enforced", state, log, false, []string{want}, nil)
			assert.Contains(t, state.Recommendations, haZoneSkewAdvice)
			continue
		}
		haOutcome(t, "skew/preferred", state, log, true, nil, []string{want})
	}

	f := newHAFixture(t, HAModeEnforced)
	f.nodes = append(f.nodes, haNode("node-a2", "zone-a"), haNode("node-b2", "zone-b"), haNode("node-d", "zone-d"))
	f.addMembers("cassandra", "node-a2", "node-b2")
	state, log := f.run(t, false, nil)
	haOutcome(t, "5 members over 4 zones", state, log, false, []string{"cassandra-system/cassandra: Ready members " +
		"per zone zone-a 2, zone-b 2, zone-c 1, zone-d 0, crowding zone-a and zone-b over zone-d, "}, nil)

	f = newHAFixture(t, HAModeEnforced)
	f.nodes = append(f.nodes, haNode("node-a2", "zone-a"), haNode("node-b2", "zone-b"))
	f.addMembers("cassandra", "node-a2", "node-b2")
	state, log = f.run(t, false, nil)
	haOutcome(t, "5 members at 2/2/1", state, log, true, nil, nil)

	// zone-c holds only a node the NATS servers' selector keeps them off, so
	// 1/2 over zone-a and zone-b is within the spread.
	f = newHAFixture(t, HAModeEnforced)
	f.nodes = append(f.nodes, haNode("node-b2", "zone-b"))
	for _, n := range f.nodes {
		if n.Name != "node-c" {
			n.Labels["pool"] = "nats"
		}
	}
	f.template("nats").Spec.NodeSelector = map[string]string{"pool": "nats"}
	f.place("nats", "node-a", "node-b", "node-b2")
	state, log = f.run(t, false, nil)
	haOutcome(t, "a zone it cannot use", state, log, true, nil, nil)

	f = newHAFixture(t, HAModeEnforced)
	f.nodes = append(f.nodes, haNode("node-a2", "zone-a"), haNode("node-b2", "zone-b"))
	f.addMembers("cassandra", "node-a2", "node-b2")
	f.pods["cassandra"][2].Status = readyPodStatus(false)
	f.sets["cassandra"].Status.ReadyReplicas = 4
	state, log = f.run(t, false, nil)
	haOutcome(t, "a member down", state, log, false, []string{"cassandra-system/cassandra: Ready 4 of 5"}, nil)

	f = newHAFixture(t, HAModeEnforced)
	f.nodes = append(f.nodes, haNode("node-b2", "zone-b"))
	three := int32(3)
	d := f.deploys["api"]
	d.Spec.Replicas, d.Status.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas = &three, 3, 3, 3
	third := f.pods["api"][1].DeepCopy()
	third.Name = "nvcf-api-abc12-2"
	f.pods["api"] = append(f.pods["api"], third)
	f.place("api", "node-a", "node-b", "node-b2")
	state, log = f.run(t, false, nil)
	haOutcome(t, "replica-safe skew", state, log, true, nil, nil)
}

// Pods that share a workload's labels but not its controller, such as the api
// account-bootstrap hook pod, nats-box and the OpenBao agent injector, are
// neither counted as its replicas nor judged as workloads of the table.
func TestCheckHAGuarantee_IgnoresLookAlikePods(t *testing.T) {
	f := newHAFixture(t, HAModeEnforced)
	f.place("api", "node-a", "node-a")
	controller := true
	hookLabels := map[string]string{"app.kubernetes.io/name": "helm-api", haInstanceLabel: "api",
		"app.kubernetes.io/component": "account-bootstrap"}
	f.extra = append(f.extra, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "nvcf-api-account-bootstrap-x", Namespace: "nvcf", Labels: hookLabels,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job",
				Name: "nvcf-api-account-bootstrap", Controller: &controller}}},
		Spec: corev1.PodSpec{NodeName: "node-b"}, Status: readyPodStatus(true),
	})
	one := int32(1)
	for _, lookAlike := range []struct{ ns, name, component string }{
		{"nats-system", "nats-box", "nats-box"},
		{"vault-system", "openbao-server-agent-injector", "webhook"},
	} {
		labels := map[string]string{haInstanceLabel: strings.TrimSuffix(lookAlike.name, "-agent-injector"),
			"app.kubernetes.io/component": lookAlike.component}
		if lookAlike.ns == "nats-system" {
			labels[haInstanceLabel] = "nats"
		}
		f.extra = append(f.extra, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: lookAlike.name, Namespace: lookAlike.ns},
			Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		}, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: lookAlike.name + "-x", Namespace: lookAlike.ns, Labels: labels},
			Spec:       corev1.PodSpec{NodeName: "node-c"}, Status: readyPodStatus(true),
		})
	}
	state, log := f.run(t, false, nil)
	haOutcome(t, "look-alikes", state, log, false, []string{
		"nvcf/nvcf-api: all 2 Ready replicas run on node node-a, mode enforced requires them on distinct nodes",
	}, nil)
	assert.NotContains(t, log, "nats-box:")
	assert.NotContains(t, log, "agent-injector:")
}

// A node that runs or can run the HA workloads needs a zone label: a warning
// under preferred, a failure under enforced. Nodes the workloads cannot use
// (cordoned, tainted, outside their selector) do not.
func TestCheckHAGuarantee_ZoneLabels(t *testing.T) {
	for _, mode := range haModes {
		f := newHAFixture(t, mode)
		f.nodes = append(f.nodes, haNode("node-d", ""))
		state, log := f.run(t, false, nil)
		want := "nodes node-d have no topology.kubernetes.io/zone label though they run or can run the HA " +
			"workloads, " + haAsk(mode, "every such node labelled")
		if mode == HAModeEnforced {
			haOutcome(t, "unlabelled/enforced", state, log, false, []string{want}, nil)
			assert.Contains(t, state.Recommendations, haZoneLabelAdvice)
		} else {
			haOutcome(t, "unlabelled/preferred", state, log, true, nil, []string{want})
		}
	}

	ineligible := map[string]func(*haFixture, *corev1.Node){
		"cordoned": func(_ *haFixture, n *corev1.Node) { n.Spec.Unschedulable = true },
		"NotReady": func(_ *haFixture, n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionFalse },
		"tainted": func(_ *haFixture, n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "node-role.kubernetes.io/control-plane",
				Effect: corev1.TaintEffectNoSchedule}}
		},
		"outside every node selector": func(f *haFixture, d *corev1.Node) {
			for _, w := range haWorkloads {
				f.template(w.release).Spec.NodeSelector = map[string]string{"pool": "nvcf"}
			}
			for _, n := range f.nodes {
				if n != d {
					n.Labels["pool"] = "nvcf"
				}
			}
		},
		"outside every required node affinity": func(f *haFixture, _ *corev1.Node) {
			zoned := corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpExists}}}
			for _, w := range haWorkloads {
				f.template(w.release).Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{zoned}}}
			}
		},
	}
	for name, change := range ineligible {
		f := newHAFixture(t, HAModeEnforced)
		d := haNode("node-d", "")
		f.nodes = append(f.nodes, d)
		change(f, d)
		state, log := f.run(t, false, nil)
		haOutcome(t, name, state, log, true, nil, nil)
	}

	// A tolerated taint leaves the node eligible, and one that hosts an HA pod
	// counts whatever its state.
	f := newHAFixture(t, HAModeEnforced)
	d := haNode("node-d", "")
	d.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "nvcf", Effect: corev1.TaintEffectNoSchedule}}
	f.nodes = append(f.nodes, d)
	for _, w := range haWorkloads {
		f.template(w.release).Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Value: "nvcf",
			Effect: corev1.TaintEffectNoSchedule}}
	}
	state, log := f.run(t, false, nil)
	haOutcome(t, "tolerated taint", state, log, false, []string{"nodes node-d have no topology.kubernetes.io/zone"},
		nil)

	f = newHAFixture(t, HAModeEnforced)
	d = haNode("node-d", "")
	d.Spec.Unschedulable = true
	f.nodes = append(f.nodes, d)
	f.place("api", "node-a", "node-d")
	state, log = f.run(t, false, nil)
	haOutcome(t, "hosts a pod", state, log, false, []string{"nodes node-d have no topology.kubernetes.io/zone"}, nil)
}

// A rollout strategy that drops a replica-safe workload below its Ready count
// is a warning under both modes.
func TestCheckHAGuarantee_RolloutStrategyWarns(t *testing.T) {
	for _, mode := range haModes {
		f := newHAFixture(t, mode)
		one := intstr.FromInt32(1)
		f.deploys["notary-service"].Spec.Strategy.RollingUpdate.MaxUnavailable = &one
		f.deploys["api-keys"].Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
		state, log := f.run(t, false, nil)
		expects := ", mode " + string(mode) + " expects RollingUpdate maxSurge 1, maxUnavailable 0, which keeps " +
			"every replica Ready through a rollout"
		haOutcome(t, string(mode), state, log, true, nil, []string{
			"nvcf/notary-service: rollout strategy RollingUpdate maxSurge 1, maxUnavailable 1" + expects,
			"api-keys/api-keys: rollout strategy Recreate" + expects,
		})
	}

	// The default strategy at two replicas surges one and keeps both.
	f := newHAFixture(t, HAModeEnforced)
	f.deploys["reval"].Spec.Strategy = appsv1.DeploymentStrategy{}
	state, log := f.run(t, false, nil)
	haOutcome(t, "defaulted", state, log, true, nil, nil)
}

// After install each always-on workload must be found; optional ones, and
// quorum components declared external, are judged only when present. Before
// install nothing is expected.
func TestCheckHAGuarantee_Membership(t *testing.T) {
	f := newHAFixture(t, HAModePreferred)
	f.remove("api")
	f.remove("nats")
	f.remove("ratelimiter")
	f.remove("llm-api-gateway")
	f.remove("sis")
	state, log := f.run(t, false, nil)
	haOutcome(t, "after install", state, log, false, []string{
		"nvcf/api: no Deployment with app.kubernetes.io/instance=api found after install, mode preferred " +
			"requires it",
		"nats-system/nats: no StatefulSet with app.kubernetes.io/component=nats,app.kubernetes.io/instance=nats " +
			"found after install, mode preferred requires it",
	}, nil)
	assert.Contains(t, state.Recommendations, haMissingAdvice)
	for _, optional := range []string{"ratelimiter", "llm-api-gateway", "sis"} {
		assert.NotContains(t, log, "instance="+optional+" found", optional)
	}

	f = newHAFixture(t, HAModePreferred)
	t.Setenv(externalComponentsEnv, "nats")
	f.remove("nats")
	state, log = f.run(t, false, nil)
	haOutcome(t, "external", state, log, true, nil, nil)

	f = newHAFixture(t, HAModeEnforced)
	for _, w := range haWorkloads {
		f.remove(w.release)
	}
	state, log = f.run(t, true, nil)
	haOutcome(t, "before install", state, log, true, nil, nil)
	assert.Contains(t, log, "No HA workload found (pre-install state)")

	f = newHAFixture(t, HAModeEnforced)
	f.remove("api")
	state, log = f.run(t, true, nil)
	haOutcome(t, "partial before install", state, log, true, nil, nil)
}

// An unreadable namespace leaves its workloads unjudged: unknown, with a
// warning, and none of them reported missing.
func TestCheckHAGuarantee_UnreadableNamespaceIsUnknown(t *testing.T) {
	f := newHAFixture(t, HAModeEnforced)
	state, _ := f.run(t, false, func(c *fake.Clientset) {
		c.PrependReactor("list", "deployments", func(a ktesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() != "nvcf" {
				return false, nil, nil
			}
			return true, nil, apierrors.NewForbidden(
				schema.GroupResource{Group: "apps", Resource: "deployments"}, "", fmt.Errorf("denied"))
		})
	})
	assert.Nil(t, state.HAGuaranteeOK)
	joined := strings.Join(state.Warnings, "; ")
	assert.Contains(t, joined, "HA Guarantee: status unknown (could not read Deployments in nvcf: ")
	assert.NotContains(t, joined, "found after install")
}

// Pods that cannot be read leave that workload's observed placement unjudged.
func TestCheckHAGuarantee_UnreadablePodsAreUnknown(t *testing.T) {
	state, _ := newHAFixture(t, HAModePreferred).run(t, false, func(c *fake.Clientset) {
		c.PrependReactor("list", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() != "cassandra-system" {
				return false, nil, nil
			}
			return true, nil, apierrors.NewServiceUnavailable("throttled")
		})
	})
	assert.Nil(t, state.HAGuaranteeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"HA Guarantee: status unknown (could not read the pods of cassandra-system/cassandra: ")
}

// Nodes that cannot be read leave the zone checks unjudged.
func TestCheckHAGuarantee_UnreadableNodesAreUnknown(t *testing.T) {
	state, _ := newHAFixture(t, HAModeEnforced).run(t, false, func(c *fake.Clientset) {
		c.PrependReactor("list", "nodes", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", fmt.Errorf("x"))
		})
	})
	assert.Nil(t, state.HAGuaranteeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "HA Guarantee: status unknown (could not read nodes: ")
}

// A relocated OpenBao is looked for where NVCF_OPENBAO_NAMESPACE says.
func TestCheckHAGuarantee_FollowsTheOpenBaoNamespace(t *testing.T) {
	f := newHAFixture(t, HAModePreferred)
	t.Setenv(openBaoNamespaceEnv, "openbao")
	f.sets["openbao-server"].Namespace, f.pdbs["openbao-server"].Namespace = "openbao", "openbao"
	for _, p := range f.pods["openbao-server"] {
		p.Namespace = "openbao"
	}
	state, log := f.run(t, false, nil)
	haOutcome(t, "relocated", state, log, true, nil, nil)

	f = newHAFixture(t, HAModePreferred)
	t.Setenv(openBaoNamespaceEnv, "openbao")
	state, log = f.run(t, false, nil)
	haOutcome(t, "left behind", state, log, false, []string{"openbao/openbao-server: no StatefulSet with "}, nil)
}

// The row makes each read once: workloads and pods per namespace, PDBs and
// nodes cluster-wide, so its cost does not grow with the number of workloads.
func TestCheckHAGuarantee_ReadsAreBounded(t *testing.T) {
	var client *fake.Clientset
	_, _ = newHAFixture(t, HAModeEnforced).run(t, false, func(c *fake.Clientset) { client = c })
	count := map[string]int{}
	for _, a := range client.Actions() {
		count[a.GetVerb()+" "+a.GetResource().Resource+" "+a.GetNamespace()]++
	}
	for key, n := range count {
		assert.Equal(t, 1, n, "%s is read once", key)
	}
	assert.Equal(t, 1, count["list poddisruptionbudgets "])
	assert.Equal(t, 1, count["list nodes "])
	assert.Equal(t, 1, count["list pods nvcf"])
}

// An escape-hatch override reads as a degraded guarantee: each weakened object
// is named with what the mode requires.
func TestCheckHAGuarantee_ReportsEscapeHatchWeakening(t *testing.T) {
	f := newHAFixture(t, HAModeEnforced)
	one := int32(1)
	d := f.deploys["api"]
	d.Spec.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Status.Replicas = &one, 1, 1, 1
	f.pods["api"] = f.pods["api"][:1]
	delete(f.pdbs, "api")
	d.Spec.Template.Spec.Affinity = &corev1.Affinity{}
	state, log := f.run(t, false, nil)
	haOutcome(t, "weakened", state, log, false, []string{
		"nvcf/nvcf-api: spec.replicas 1, mode enforced requires at least 2",
		"nvcf/nvcf-api: no PodDisruptionBudget selects its pods, mode enforced requires one with minAvailable 1",
		"nvcf/nvcf-api: no hostname pod anti-affinity selects its pods, mode enforced requires a required term",
	}, nil)
	assert.Contains(t, log, "HA Guarantee findings (3)")
}

// The HA Guarantee row is critical unless the mode is none, where it says it
// does not apply and blocks nothing. Without a mode it names what it judged.
// A pass that added warnings says so, as the HA Topology row does.
func TestPrintSummary_HAGuaranteeRow(t *testing.T) {
	const warning = haGuaranteeLabel + ": nvcf/nvcf-api: rollout strategy Recreate"
	for _, tc := range []struct {
		mode     HAMode
		ok       *bool
		warnings []string
		// assessed and unassessed count the workloads whose rendered
		// placement was and was not judged without a mode.
		assessed, unassessed int
		want                 string
		blocked              bool
	}{
		{HAModePreferred, cpRow(true), nil, 0, 0, "HA Guarantee: Met for mode preferred", false},
		{HAModePreferred, cpRow(true), []string{warning}, 0, 0, "HA Guarantee: Met for mode preferred (with " +
			"warnings)", false},
		{HAModeEnforced, cpRow(false), nil, 0, 0, "HA Guarantee: Not Met for mode enforced", true},
		{HAModeEnforced, nil, nil, 0, 0, "HA Guarantee: Status Unknown (not fully observed; see warnings)", true},
		{HAModeNone, nil, nil, 0, 0, "HA Guarantee: Not Applicable (HA mode none)", false},
		{HAModeNone, cpRow(false), nil, 0, 0, "HA Guarantee: Not Applicable (HA mode none)", false},
		{"", cpRow(true), nil, 2, 9, "HA Guarantee: Rendered Placement Met (2 assessed, 9 not assessed: no " +
			"rendered terms)", false},
		{"", cpRow(true), nil, 11, 0, "HA Guarantee: Rendered Placement Met (11 assessed)", false},
		{"", cpRow(true), []string{warning}, 2, 9, "HA Guarantee: Rendered Placement Met (2 assessed, 9 not " +
			"assessed: no rendered terms) (with warnings)", false},
		{"", cpRow(true), nil, 0, 11, "HA Guarantee: Rendered Placement Not Assessed (HA mode not supplied)", false},
		{"", cpRow(false), nil, 1, 10, "HA Guarantee: Rendered Placement Not Met (HA mode not supplied)", true},
		{"", nil, nil, 0, 0, "HA Guarantee: Status Unknown (not fully observed; see warnings)", true},
	} {
		out, err := printControlPlaneSummary(func(s *ValidationState) {
			s.HAMode, s.HAGuaranteeOK, s.Warnings = tc.mode, tc.ok, tc.warnings
			s.HAPlacementAssessed, s.HAPlacementNotAssessed = tc.assessed, tc.unassessed
		})
		assert.Contains(t, out, tc.want, tc.mode)
		assert.Equal(t, tc.blocked, err != nil, tc.want)
		if !tc.blocked && len(tc.warnings) == 0 {
			assert.NotContains(t, out, VerdictReadyWithWarnings, "%s: a row that passed or does not apply is no "+
				"warning on an otherwise clean run", tc.want)
		}
		if !strings.HasSuffix(tc.want, "(with warnings)") {
			assert.NotContains(t, out, tc.want+" (with warnings)", tc.want)
		}
	}
}

// printControlPlaneSummary prints the summary of a healthy control-plane run
// changed by set, and returns it with printSummary's error.
func printControlPlaneSummary(set func(*ValidationState)) (string, error) {
	var out strings.Builder
	state := healthyControlPlaneState()
	state.Log.Logger.SetOutput(&out)
	set(state)
	err := printSummary(state)
	return out.String(), err
}

// The summary publishes ha_guarantee only when the row has a result, which a
// run without a mode has from the rendered placement of the workloads it
// assessed. With none assessed it has no result.
func TestBuildSummary_HAGuaranteeKey(t *testing.T) {
	for _, tc := range []struct {
		mode     HAMode
		ok       *bool
		assessed int
		present  bool
	}{{HAModePreferred, cpRow(true), 0, true}, {HAModeEnforced, cpRow(false), 0, true},
		{HAModeEnforced, nil, 0, false}, {"", nil, 0, false}, {"", cpRow(false), 1, true},
		{"", cpRow(true), 3, true}, {"", cpRow(true), 0, false}} {
		state := healthyControlPlaneState()
		state.HAMode, state.HAGuaranteeOK, state.HAPlacementAssessed = tc.mode, tc.ok, tc.assessed
		s := buildSummary(state, time.Now(), tc.ok == nil || *tc.ok, VerdictReady)
		got, present := s.Checks[CheckKeyHAGuarantee]
		assert.Equal(t, tc.present, present, "%s %d", tc.mode, tc.assessed)
		if tc.present {
			assert.Equal(t, *tc.ok, got, tc.mode)
		}
	}
	assert.Equal(t, "ha_guarantee", CheckKeyHAGuarantee)
}

// A Run under an HA mode reaches the row, and the summary carries its key. One
// node keeps the overlay probe out of the run; under preferred the peers it
// puts together are warnings.
func TestRun_HAGuaranteeUnderAnHAMode(t *testing.T) {
	f := newHAFixture(t, HAModePreferred)
	f.nodes = f.nodes[:1]
	for _, w := range haWorkloads {
		for _, p := range f.pods[w.release] {
			p.Spec.NodeName = "node-a"
		}
	}
	t.Setenv(HAModeEnv, "preferred")
	t.Setenv(PostInstallEnv, "true")
	client := fake.NewSimpleClientset(f.objects()...)
	_ = Run(context.Background(), client, nil, "", "", "nvca-system", true, RoleControlPlane)
	s := readSummary(t, client, "nvca-system")
	assert.Contains(t, s.Checks, CheckKeyHAGuarantee)
	assert.True(t, s.Checks[CheckKeyHAGuarantee], "warnings: %q", s.Warnings)
}
