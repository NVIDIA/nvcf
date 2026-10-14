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
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// haTopologyLabel names the HA Topology section, its summary row and the
// warnings it adds.
const haTopologyLabel = "HA Topology"

// haMinZones is the fewest zones that hold one member of a three-member
// quorum each.
const haMinZones = 3

// The advice for each kind of topology finding.
const (
	haNodesAdvice = "Give the quorum services (NATS, OpenBao, Cassandra) at least 3 nodes: Ready, uncordoned " +
		"nodes whose taints they tolerate and whose labels match their node selectors (global.nodeSelectors, " +
		"global.tolerations), or install with highAvailability.mode none."
	haZonesAdvice = "Spread the quorum services' nodes over at least 3 zones and label each node with " +
		corev1.LabelTopologyZone + ": a 3-member quorum across 2 zones splits 2+1 and does not survive the loss " +
		"of the zone that holds 2 members."
	haCapacityAdvice = "Add nodes to the pool a workload is held to, or widen its node selector, node affinity or " +
		"tolerations, so each quorum service has 3 eligible nodes and each Tier-1 Deployment one more than its " +
		"replicas."
	haStorageAdvice = "Set global.storageClass and clusterValidator.storageClass to a StorageClass with " +
		"volumeBindingMode WaitForFirstConsumer."
)

// checkHATopology checks that the cluster can hold the placement the HA mode
// asks of the control plane: at least 3 nodes the quorum services can run on,
// spread over at least 3 zones; 3 eligible nodes for each quorum service, one
// per member, and one more than its replicas for each Tier-1 Deployment, for a
// rollout's surge pod; quorum members whose node-local volumes pin them to
// nodes where their placement still holds; and a StorageClass that provisions
// a volume only once its pod is placed, and one that can attach it on another
// node. An installed workload is judged by the tolerations, node selector and
// required node affinity of its live pod template, and one not installed by
// the nodes a pod with none of them can use.
//
// Only an HA mode (NVCF_HA_MODE preferred or enforced) asks for any of this,
// so the row does not apply otherwise. Fewer than 3 nodes for the quorum fails
// under either mode; too few zones, too little room for a workload, pinned
// members against their placement and an Immediate StorageClass fail under
// enforced and warn under preferred; a node-local StorageClass warns under
// both.
//
// Critical: an HA mode on a cluster that cannot place it has no node or zone
// to fail over to.
func checkHATopology(ctx context.Context, client kubernetes.Interface, state *ValidationState) {
	printHeader(state.Log, haTopologyLabel)
	if !state.HAMode.guaranteed() {
		printInfo(state.Log, "Not applicable: "+haModeAbsence(state.HAMode))
		return
	}
	nodes, err := observe(ctx, func(c context.Context) (*corev1.NodeList, error) {
		return client.CoreV1().Nodes().List(c, metav1.ListOptions{})
	})
	if err != nil {
		printWarning(state.Log, readFailure("nodes", err))
		state.Warnings = append(state.Warnings, unknownWarning(haTopologyLabel, "nodes", err))
		return
	}
	s := &haTopologyScan{log: state.Log, state: state, mode: state.HAMode}
	printInfo(s.log, fmt.Sprintf("HA mode %s: fewer than 3 nodes for the quorum services fails; too few zones, "+
		"too little room for a workload, members pinned against their placement by node-local volumes and an "+
		"Immediate StorageClass %s; a node-local StorageClass warns", s.mode, s.byModeVerb()))
	s.nodes = nodes.Items
	s.usable = s.usableNodes(nodes.Items)
	services := s.quorumServices(ctx, client)
	s.judgeQuorum(services, s.memberPins(ctx, client, services))
	s.judgeSurge(s.tier1Deployments(ctx, client))
	s.judgeStorage(ctx, client)
	s.verdict()
}

// haModeAbsence says why mode asks for no HA guarantee.
func haModeAbsence(mode HAMode) string {
	if mode == "" {
		return "HA mode not supplied"
	}
	return "HA mode " + string(mode)
}

// haTopologyScan collects what HA Topology saw.
type haTopologyScan struct {
	log    *logrus.Entry
	state  *ValidationState
	mode   HAMode
	nodes  []corev1.Node
	usable []*corev1.Node
	// volumes are the PersistentVolumes, read once when first needed.
	volumes     []corev1.PersistentVolume
	volumesRead bool
	volumesErr  error

	failures []string
	advice   []string
	// unread holds each place that could not be read, with the cause, and
	// unreadErrs the errors themselves.
	unread     []string
	unreadErrs []error
}

func (s *haTopologyScan) byModeVerb() string {
	if s.mode == HAModeEnforced {
		return "fail"
	}
	return "warn"
}

// finding records what the run found short of the mode, as a failure or a
// warning by sev, with the advice for its kind.
func (s *haTopologyScan) finding(sev haSeverity, advice, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if sev == haFails || (sev == haFailsEnforced && s.mode == HAModeEnforced) {
		printError(s.log, msg)
		s.failures = append(s.failures, msg)
	} else {
		printWarning(s.log, msg)
		s.state.Warnings = append(s.state.Warnings, haTopologyLabel+": "+msg)
	}
	if !slices.Contains(s.advice, advice) {
		s.advice = append(s.advice, advice)
	}
}

func (s *haTopologyScan) unreadable(what string, err error) {
	printWarning(s.log, readFailure(what, err))
	s.unread = append(s.unread, fmt.Sprintf("%s (%v)", what, err))
	s.unreadErrs = append(s.unreadErrs, err)
}

// usableNodes returns the nodes that can take a control-plane pod: Ready, not
// cordoned or fenced, and running a Linux kubelet of their own.
func (s *haTopologyScan) usableNodes(all []corev1.Node) []*corev1.Node {
	var usable []*corev1.Node
	var left []string
	for i := range all {
		n := &all[i]
		why := nodeUnsupported(n)
		if why == "" {
			why = nodeIneligible(n)
		}
		if why != "" {
			left = append(left, n.Name+" ("+why+")")
			continue
		}
		usable = append(usable, n)
	}
	msg := fmt.Sprintf("%d of %d node(s) Ready, schedulable and not fenced", len(usable), len(all))
	if len(left) > 0 {
		msg += "; left out: " + nameList(left)
	}
	printInfo(s.log, msg)
	return usable
}

// nodePlacement is what decides the nodes a workload's pods may run on. A
// workload not installed (known false) has no pod template to read, so only a
// node without scheduling taints counts for it. Of the tainted nodes, one
// tainted for the Kubernetes control plane, a taint the stack gives no release
// a toleration for by default, is left out; any other is named as one the
// tolerations it is yet to be given (global.tolerations) may admit.
type nodePlacement struct {
	known        bool
	tolerations  []corev1.Toleration
	nodeSelector map[string]string
	affinity     *corev1.NodeSelector
}

func templatePlacement(spec *corev1.PodSpec) nodePlacement {
	return nodePlacement{known: true, tolerations: spec.Tolerations, nodeSelector: spec.NodeSelector,
		affinity: requiredNodeAffinity(spec)}
}

func (p nodePlacement) String() string {
	if !p.known {
		return "no pod template read, so judged by the nodes a pod with no tolerations or node selector can use"
	}
	var parts []string
	if len(p.nodeSelector) > 0 {
		parts = append(parts, "nodeSelector "+labels.Set(p.nodeSelector).String())
	}
	if p.affinity != nil {
		parts = append(parts, "required node affinity")
	}
	if n := len(p.tolerations); n > 0 {
		parts = append(parts, fmt.Sprintf("%d toleration(s)", n))
	}
	if len(parts) == 0 {
		return "no node selector, node affinity or tolerations"
	}
	return strings.Join(parts, ", ")
}

// nodeFit is whether a pod can run on a node: maybe is a node an uninstalled
// workload may yet be given tolerations for, which is not counted.
type nodeFit int

const (
	fitsNot nodeFit = iota
	fitsMaybe
	fits
)

// controlPlaneRoleTaints keep pods off the Kubernetes control-plane nodes.
var controlPlaneRoleTaints = map[string]bool{
	"node-role.kubernetes.io/control-plane": true,
	"node-role.kubernetes.io/master":        true,
}

func (p nodePlacement) fit(n *corev1.Node) nodeFit {
	var taints []corev1.Taint
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
			taints = append(taints, t)
		}
	}
	if !p.known {
		switch {
		case len(taints) == 0:
			return fits
		case slices.ContainsFunc(taints, func(t corev1.Taint) bool { return controlPlaneRoleTaints[t.Key] }):
			return fitsNot
		default:
			return fitsMaybe
		}
	}
	if nodeAdmits(n, p.tolerations, p.nodeSelector, p.affinity) {
		return fits
	}
	return fitsNot
}

// haNodes are the usable nodes a workload's pods can run on, sure, and those
// it may yet be given tolerations for, maybe.
type haNodes struct{ sure, maybe []*corev1.Node }

func (s *haTopologyScan) nodesFor(p nodePlacement) haNodes {
	var out haNodes
	for _, n := range s.usable {
		switch p.fit(n) {
		case fits:
			out.sure = append(out.sure, n)
		case fitsMaybe:
			out.maybe = append(out.maybe, n)
		}
	}
	return out
}

// union adds o's nodes to h; a node sure for either is sure.
func (h haNodes) union(o haNodes) haNodes {
	sure := map[string]bool{}
	out := haNodes{}
	for _, n := range slices.Concat(h.sure, o.sure) {
		if !sure[n.Name] {
			sure[n.Name] = true
			out.sure = append(out.sure, n)
		}
	}
	maybe := map[string]bool{}
	for _, n := range slices.Concat(h.maybe, o.maybe) {
		if !sure[n.Name] && !maybe[n.Name] {
			maybe[n.Name] = true
			out.maybe = append(out.maybe, n)
		}
	}
	return out
}

// uncounted names the tainted nodes left out of a count, which tolerations a
// service not yet installed is given at install may admit, or "" for none.
func (h haNodes) uncounted() string {
	if len(h.maybe) == 0 {
		return ""
	}
	return fmt.Sprintf(". Not counted: %d node(s) with NoSchedule or NoExecute taints (%s), which tolerations "+
		"the services are given at install (global.tolerations) may admit; re-run after install, when they can "+
		"be read", len(h.maybe), nodeNames(h.maybe, "", ""))
}

// haWorkloadPlacement is a workload found, or not, with what it is judged by.
type haWorkloadPlacement struct {
	name      string
	placement nodePlacement
	replicas  int32
	// sts is a quorum service's StatefulSet, nil when it is not installed.
	sts *appsv1.StatefulSet
}

// quorumServices returns each quorum service not declared external, by its
// live StatefulSet when there is one and as not installed otherwise.
func (s *haTopologyScan) quorumServices(ctx context.Context, client kubernetes.Interface) []haWorkloadPlacement {
	external, _ := externalComponents()
	sets := map[string][]appsv1.StatefulSet{}
	var out []haWorkloadPlacement
	for _, w := range haWorkloads {
		if w.tier != haQuorum || external[w.component] {
			continue
		}
		ns := w.namespaceName()
		items, read := sets[ns]
		if !read {
			list, err := observe(ctx, func(c context.Context) (*appsv1.StatefulSetList, error) {
				return client.AppsV1().StatefulSets(ns).List(c, metav1.ListOptions{})
			})
			if err != nil {
				s.unreadable("StatefulSets in "+ns, err)
			} else {
				items = list.Items
			}
			sets[ns] = items
		}
		found := false
		for i := range items {
			if sts := &items[i]; w.owns(&sts.Spec.Template) {
				replicas := int32(1)
				if sts.Spec.Replicas != nil {
					replicas = *sts.Spec.Replicas
				}
				out = append(out, haWorkloadPlacement{name: ns + "/" + sts.Name,
					placement: templatePlacement(&sts.Spec.Template.Spec), replicas: replicas, sts: sts})
				found = true
			}
		}
		if !found {
			out = append(out, haWorkloadPlacement{name: ns + "/" + w.release})
		}
	}
	return out
}

// tier1Deployments returns each Tier-1 Deployment the HA modes scale that runs.
func (s *haTopologyScan) tier1Deployments(ctx context.Context, client kubernetes.Interface) []haWorkloadPlacement {
	deploys := map[string][]appsv1.Deployment{}
	var out []haWorkloadPlacement
	for _, w := range haWorkloads {
		if w.tier != haReplicaSafe {
			continue
		}
		ns := w.namespaceName()
		items, read := deploys[ns]
		if !read {
			list, err := observe(ctx, func(c context.Context) (*appsv1.DeploymentList, error) {
				return client.AppsV1().Deployments(ns).List(c, metav1.ListOptions{})
			})
			if err != nil {
				s.unreadable("Deployments in "+ns, err)
			} else {
				items = list.Items
			}
			deploys[ns] = items
		}
		for i := range items {
			d := &items[i]
			if !w.owns(&d.Spec.Template) {
				continue
			}
			replicas := int32(1)
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			out = append(out, haWorkloadPlacement{name: ns + "/" + d.Name,
				placement: templatePlacement(&d.Spec.Template.Spec), replicas: replicas})
		}
	}
	return out
}

// judgeQuorum judges the nodes the quorum services can run on together, the
// zones those span, the nodes each installed service can run on, and the
// members its node-local volumes pin to nodes.
func (s *haTopologyScan) judgeQuorum(services []haWorkloadPlacement, pins map[string][]memberPin) {
	if len(services) == 0 {
		printInfo(s.log, "Every quorum service runs outside this cluster ("+externalComponentsEnv+"); their nodes "+
			"and zones are not judged")
		return
	}
	each := make([]haNodes, len(services))
	var union haNodes
	for i, svc := range services {
		each[i] = s.nodesFor(svc.placement)
		union = union.union(each[i])
		maybe := ""
		if n := len(each[i].maybe); n > 0 {
			maybe = fmt.Sprintf(" and %d tainted node(s) it may tolerate", n)
		}
		printInfo(s.log, fmt.Sprintf("  %s: %d eligible node(s)%s (%s)", svc.name, len(each[i].sure), maybe,
			svc.placement))
	}

	if len(union.sure) < int(minQuorumSize) {
		s.finding(haFails, haNodesAdvice, "quorum services: %d node(s) can run them%s; mode %s requires at least "+
			"%d, one for each member of a %d-member quorum%s", len(union.sure), nodeNames(union.sure, " (", ")"),
			s.mode, minQuorumSize, minQuorumSize, union.uncounted())
		printInfo(s.log, "  Zones and per-service room are not judged on fewer nodes than a quorum needs")
		return
	}
	printSuccess(s.log, fmt.Sprintf("Quorum services can run on %d node(s)", len(union.sure)))
	s.judgeZones(union)

	for i, svc := range services {
		if svc.placement.known && len(each[i].sure) < int(minQuorumSize) {
			s.finding(haFailsEnforced, haCapacityAdvice, "%s: %d eligible node(s)%s; %s, so each member of the "+
				"quorum runs on its own node (placement: %s)", svc.name, len(each[i].sure),
				nodeNames(each[i].sure, " (", ")"),
				s.mode.asks(haFailsEnforced, fmt.Sprintf("at least %d", minQuorumSize)), svc.placement)
		}
		s.judgePins(svc, pins[svc.name], each[i].sure)
	}
}

// judgeZones counts the zones of the nodes the quorum services can run on.
func (s *haTopologyScan) judgeZones(nodes haNodes) {
	zones, unlabelled := zonesOf(nodes.sure)
	if len(zones) >= haMinZones {
		msg := fmt.Sprintf("Their nodes span %d zones: %s", len(zones), describeZones(zones))
		if len(unlabelled) > 0 {
			msg += fmt.Sprintf("; %d carry no zone label (%s)", len(unlabelled), nameList(unlabelled))
		}
		printSuccess(s.log, msg)
		return
	}
	detail := ""
	if len(unlabelled) > 0 {
		detail = fmt.Sprintf("; %d node(s) carry no %s label (%s)", len(unlabelled), corev1.LabelTopologyZone,
			nameList(unlabelled))
	}
	s.finding(haFailsEnforced, haZonesAdvice, "quorum services: their %d node(s) span %d zone(s)%s%s; %s (%s). "+
		"A 3-member quorum across 2 zones splits 2+1, and losing the zone that holds 2 members loses quorum: with "+
		"fewer than %d zones Tier-2 is not AZ-HA%s", len(nodes.sure), len(zones), describeZonesIn(zones), detail,
		s.mode.asks(haFailsEnforced, fmt.Sprintf("at least %d zones", haMinZones)), corev1.LabelTopologyZone,
		haMinZones, nodes.uncounted())
}

// zonesOf counts nodes per zone label, and lists the nodes without one.
func zonesOf(nodes []*corev1.Node) (zones map[string]int, unlabelled []string) {
	zones = map[string]int{}
	for _, n := range nodes {
		if z := n.Labels[corev1.LabelTopologyZone]; z != "" {
			zones[z]++
		} else {
			unlabelled = append(unlabelled, n.Name)
		}
	}
	return zones, unlabelled
}

func describeZones(zones map[string]int) string {
	names := make([]string, 0, len(zones))
	for z := range zones {
		names = append(names, z)
	}
	sort.Strings(names)
	for i, z := range names {
		names[i] = fmt.Sprintf("%s: %d", z, zones[z])
	}
	return strings.Join(names, ", ")
}

func describeZonesIn(zones map[string]int) string {
	if len(zones) == 0 {
		return ""
	}
	return " (" + describeZones(zones) + ")"
}

// judgeSurge requires each Tier-1 Deployment's pods to have one node more than
// its replicas: a rollout adds a surge pod before it removes an old one.
func (s *haTopologyScan) judgeSurge(deploys []haWorkloadPlacement) {
	if len(deploys) == 0 {
		printInfo(s.log, "No Tier-1 Deployment the HA modes scale is installed; their surge room is judged after "+
			"install")
		return
	}
	for _, d := range deploys {
		if d.replicas < 1 {
			continue
		}
		nodes := s.nodesFor(d.placement)
		need := int(d.replicas) + 1
		if len(nodes.sure) >= need {
			printInfo(s.log, fmt.Sprintf("  %s: %d eligible node(s) for %d replica(s) and a surge pod",
				d.name, len(nodes.sure), d.replicas))
			continue
		}
		s.finding(haFailsEnforced, haCapacityAdvice, "%s: %d eligible node(s)%s for %d replica(s); %s, one per "+
			"replica and one for a rollout's surge pod (placement: %s)", d.name, len(nodes.sure),
			nodeNames(nodes.sure, " (", ")"), d.replicas, s.mode.asks(haFailsEnforced, fmt.Sprint(need)),
			d.placement)
	}
}

// judgeStorage judges the StorageClass the stack's volumes bind to: the one
// NVCF_STORAGE_CLASS names, else the default the apiserver gives a claim
// without one. Immediate provisions a volume before its pod is placed, so a
// zonal volume can tie a quorum member to a zone its spread, or a node
// failure, leaves no room in; and a class that provisions node-local volumes
// cannot reattach one on another node (judgeLocalVolumes). A class that does
// not exist has neither; the Default StorageClass row reports it.
func (s *haTopologyScan) judgeStorage(ctx context.Context, client kubernetes.Interface) {
	var class *storagev1.StorageClass
	if name := strings.TrimSpace(os.Getenv(storageClassEnv)); name != "" {
		sc, err := observe(ctx, func(c context.Context) (*storagev1.StorageClass, error) {
			return client.StorageV1().StorageClasses().Get(c, name, metav1.GetOptions{})
		})
		switch {
		case apierrors.IsNotFound(err):
			printInfo(s.log, fmt.Sprintf("StorageClass %s, named by %s, does not exist; its binding mode is not "+
				"judged", name, storageClassEnv))
			return
		case err != nil:
			s.unreadable("StorageClass "+name, err)
			return
		}
		class = sc
	} else {
		list, err := observe(ctx, func(c context.Context) (*storagev1.StorageClassList, error) {
			return client.StorageV1().StorageClasses().List(c, metav1.ListOptions{})
		})
		if err != nil {
			s.unreadable("StorageClasses", err)
			return
		}
		var defaults []storagev1.StorageClass
		for _, sc := range list.Items {
			if sc.Annotations[defaultClassAnnotation] == annotationTrue ||
				sc.Annotations[betaDefaultClassAnnotation] == annotationTrue {
				defaults = append(defaults, sc)
			}
		}
		if len(defaults) == 0 {
			printInfo(s.log, "No default StorageClass; its binding mode is not judged")
			return
		}
		winner := admittedDefaultClass(defaults)
		class = &defaults[slices.IndexFunc(defaults, func(sc storagev1.StorageClass) bool { return sc.Name == winner })]
	}
	binding := storagev1.VolumeBindingImmediate
	if class.VolumeBindingMode != nil {
		binding = *class.VolumeBindingMode
	}
	if binding == storagev1.VolumeBindingWaitForFirstConsumer {
		printSuccess(s.log, fmt.Sprintf("StorageClass %s binds volumes %s", class.Name, binding))
	} else {
		s.finding(haFailsEnforced, haStorageAdvice, "StorageClass %s binds volumes %s; %s, which provisions a "+
			"quorum member's volume once the member is placed, so a zonal volume cannot tie it to a zone its spread "+
			"or a node failure leaves no room in", class.Name, binding,
			s.mode.asks(haFailsEnforced, string(storagev1.VolumeBindingWaitForFirstConsumer)))
	}
	s.judgeLocalVolumes(ctx, client, class)
}

func (s *haTopologyScan) verdict() {
	s.state.Recommendations = append(s.state.Recommendations, s.advice...)
	if len(s.unread) > 0 {
		// Reported whatever decides the row, as in Tier-2.
		s.state.Warnings = append(s.state.Warnings, fmt.Sprintf("%s: status unknown (could not read %s)",
			haTopologyLabel, readFailures(s.unread, s.unreadErrs)))
	}
	switch {
	case len(s.failures) > 0:
		printError(s.log, fmt.Sprintf("The cluster cannot hold HA mode %s (%d finding(s))", s.mode, len(s.failures)))
		s.setOK(false)
	case len(s.unread) > 0:
		printWarning(s.log, "What was read meets HA mode "+string(s.mode)+", but not everything could be read")
	default:
		printSuccess(s.log, "The cluster can hold HA mode "+string(s.mode))
		s.setOK(true)
	}
}

func (s *haTopologyScan) setOK(ok bool) { s.state.HATopologyOK = &ok }

// haTopologyRow is the HA Topology summary row. Without an HA mode nothing is
// asked of the topology, so the row does not apply.
func haTopologyRow(state *ValidationState) summaryRow {
	if !state.HAMode.guaranteed() {
		return haModeAbsentRow(haTopologyLabel, state.HAMode)
	}
	if state.HATopologyOK == nil {
		return summaryRow{Critical: true, Unknown: true, UnknownMsg: haTopologyLabel + notFullyObserved}
	}
	pass := haTopologyLabel + ": Meets HA Mode " + string(state.HAMode)
	if slices.ContainsFunc(state.Warnings, func(w string) bool { return strings.HasPrefix(w, haTopologyLabel+": ") }) {
		pass += " (with warnings)"
	}
	return summaryRow{Passed: *state.HATopologyOK, PassMsg: pass,
		FailMsg: haTopologyLabel + ": Below HA Mode " + string(state.HAMode), Critical: true}
}

// haTopologyOutcome is how the HA Topology row came out for the recheck.
func haTopologyOutcome(s *ValidationState) outcome {
	if !s.HAMode.guaranteed() {
		return passed
	}
	return rowOutcome(s.HATopologyOK)
}

// nameList joins names, at most nameListLimit of them, saying how many more
// there are.
func nameList(names []string) string {
	const nameListLimit = 10
	if len(names) <= nameListLimit {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:nameListLimit], ", ") + fmt.Sprintf(" and %d more", len(names)-nameListLimit)
}

// nodeNames lists nodes between before and after, or nothing when empty.
func nodeNames(nodes []*corev1.Node, before, after string) string {
	if len(nodes) == 0 {
		return ""
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	return before + nameList(names) + after
}
