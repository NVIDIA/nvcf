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
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The advice for the node-local volume findings.
const (
	haPinnedAdvice = "A quorum member on a node-local volume stays on that node: move one only by its service's " +
		"own member-replacement procedure, which gives it a new volume where its placement allows, and only " +
		"where the service holds its data on the other members too. A StorageClass whose volumes can attach on " +
		"other nodes avoids this."
	haLocalStorageAdvice = "Give the quorum services a StorageClass whose volumes can attach on another node " +
		"(global.storageClass), such as network or replicated block storage: a node-local volume cannot follow " +
		"its member when its node fails."
)

// memberPin is a quorum member whose bound volume requires one node.
type memberPin struct {
	member, node, zone string
}

// persistentVolumes lists the PersistentVolumes once, for the members they pin
// and the StorageClass. ok is false when they could not be read.
func (s *haTopologyScan) persistentVolumes(ctx context.Context, client kubernetes.Interface) (
	pvs []corev1.PersistentVolume, ok bool) {
	if !s.volumesRead {
		s.volumesRead = true
		list, err := observe(ctx, func(c context.Context) (*corev1.PersistentVolumeList, error) {
			return client.CoreV1().PersistentVolumes().List(c, metav1.ListOptions{})
		})
		if s.volumesErr = err; err != nil {
			s.unreadable("PersistentVolumes", err)
		} else {
			s.volumes = list.Items
			slices.SortFunc(s.volumes, func(a, b corev1.PersistentVolume) int {
				return strings.Compare(a.Name, b.Name)
			})
		}
	}
	return s.volumes, s.volumesErr == nil
}

// memberPins returns, by service name, the members of each installed quorum
// service whose bound volume requires one node by kubernetes.io/hostname, as
// a node-local volume does. The claims of each namespace holding a service
// that claims volumes are listed once.
func (s *haTopologyScan) memberPins(ctx context.Context, client kubernetes.Interface,
	services []haWorkloadPlacement) map[string][]memberPin {
	claims := map[string]map[string]*corev1.PersistentVolumeClaim{}
	out := map[string][]memberPin{}
	for _, svc := range services {
		sts := svc.sts
		if sts == nil || len(sts.Spec.VolumeClaimTemplates) == 0 {
			continue
		}
		ns := sts.Namespace
		byName, read := claims[ns]
		if !read {
			list, err := observe(ctx, func(c context.Context) (*corev1.PersistentVolumeClaimList, error) {
				return client.CoreV1().PersistentVolumeClaims(ns).List(c, metav1.ListOptions{})
			})
			if err != nil {
				s.unreadable("PersistentVolumeClaims in "+ns, err)
			} else {
				byName = map[string]*corev1.PersistentVolumeClaim{}
				for i := range list.Items {
					byName[list.Items[i].Name] = &list.Items[i]
				}
			}
			claims[ns] = byName
		}
		for ordinal := range int(svc.replicas) {
			member := fmt.Sprintf("%s-%d", sts.Name, ordinal)
			if node, found := s.pinOf(ctx, client, byName, sts.Spec.VolumeClaimTemplates, member); found {
				out[svc.name] = append(out[svc.name], memberPin{member: member, node: node,
					zone: s.zoneOfNode(node)})
			}
		}
	}
	return out
}

// pinOf is the node member's bound volume requires, when one of its claims,
// named after templates, is bound to a volume that requires one node.
func (s *haTopologyScan) pinOf(ctx context.Context, client kubernetes.Interface,
	claims map[string]*corev1.PersistentVolumeClaim, templates []corev1.PersistentVolumeClaim, member string) (
	string, bool) {
	for _, tpl := range templates {
		pvc := claims[tpl.Name+"-"+member]
		if pvc == nil || pvc.Spec.VolumeName == "" {
			continue
		}
		pvs, ok := s.persistentVolumes(ctx, client)
		if !ok {
			return "", false
		}
		i := slices.IndexFunc(pvs, func(pv corev1.PersistentVolume) bool { return pv.Name == pvc.Spec.VolumeName })
		if i < 0 || !boundTo(&pvs[i], pvc) {
			continue
		}
		if host := pinnedHostname(&pvs[i]); host != "" {
			return s.nodeNamed(host), true
		}
	}
	return "", false
}

// boundTo reports whether pv is bound to pvc, and not to an earlier claim of
// the same name.
func boundTo(pv *corev1.PersistentVolume, pvc *corev1.PersistentVolumeClaim) bool {
	ref := pv.Spec.ClaimRef
	return ref != nil && ref.Namespace == pvc.Namespace && ref.Name == pvc.Name && (ref.UID == "" || ref.UID == pvc.UID)
}

// pinnedHostname is the one kubernetes.io/hostname pv's required node affinity
// allows, as a node-local volume (local-path, local) sets it: every term names
// that one hostname. "" when it allows more nodes or names none.
func pinnedHostname(pv *corev1.PersistentVolume) string {
	na := pv.Spec.NodeAffinity
	if na == nil || na.Required == nil || len(na.Required.NodeSelectorTerms) == 0 {
		return ""
	}
	host := ""
	for _, t := range na.Required.NodeSelectorTerms {
		i := slices.IndexFunc(t.MatchExpressions, func(r corev1.NodeSelectorRequirement) bool {
			return r.Key == corev1.LabelHostname && r.Operator == corev1.NodeSelectorOpIn && len(r.Values) == 1
		})
		if i < 0 || (host != "" && t.MatchExpressions[i].Values[0] != host) {
			return ""
		}
		host = t.MatchExpressions[i].Values[0]
	}
	return host
}

// nodeNamed is the node whose kubernetes.io/hostname label is host, or host
// itself when no node carries it.
func (s *haTopologyScan) nodeNamed(host string) string {
	for i := range s.nodes {
		if s.nodes[i].Labels[corev1.LabelHostname] == host {
			return s.nodes[i].Name
		}
	}
	return host
}

// zoneOfNode is the zone label of the node named name, "" when it has none or
// is not in the cluster.
func (s *haTopologyScan) zoneOfNode(name string) string {
	for i := range s.nodes {
		if s.nodes[i].Name == name {
			return s.nodes[i].Labels[corev1.LabelTopologyZone]
		}
	}
	return ""
}

// judgePins judges a quorum service whose members' volumes pin them to nodes,
// each such member eligible only on its node: one pinned to a node the
// service cannot run on, cordoned, NotReady, gone or outside its placement,
// cannot run anywhere; two pinned to one node cannot keep one member per
// node; and pinned members that crowd a zone keep its zone spread from
// holding. Each is short of the mode, and the members so pinned also cannot be
// recreated elsewhere when their node fails.
func (s *haTopologyScan) judgePins(svc haWorkloadPlacement, pins []memberPin, eligible []*corev1.Node) {
	if len(pins) == 0 {
		return
	}
	short := false
	byNode := map[string][]string{}
	var placeable []memberPin
	for _, p := range pins {
		byNode[p.node] = append(byNode[p.node], p.member)
		if slices.ContainsFunc(eligible, func(n *corev1.Node) bool { return n.Name == p.node }) {
			placeable = append(placeable, p)
		}
	}
	for _, node := range slices.Sorted(maps.Keys(byNode)) {
		members := byNode[node]
		if why := s.pinUnusable(node, eligible, svc.placement); why != "" {
			pins, they := "a node-local volume pins", "it"
			if len(members) > 1 {
				pins, they = "node-local volumes pin", "they"
			}
			s.finding(haFailsEnforced, haPinnedAdvice, "%s: %s %s to node %s, which %s, so %s cannot run on any "+
				"node; %s", svc.name, pins, joinAnd(members), node, why, they,
				s.mode.asks(haFailsEnforced, "every member on a node it can run on"))
			short = true
			continue
		}
		if len(members) > 1 {
			s.finding(haFailsEnforced, haPinnedAdvice, "%s: node-local volumes pin members %s to node %s; %s",
				svc.name, joinAnd(members), node, s.mode.asks(haFailsEnforced, "one member per node"))
			short = true
		}
	}
	if problem, crowded := pinnedZoneSkew(int(svc.replicas), placeable, eligible); problem != "" {
		ask := s.mode.asks(haFailsEnforced, "that spread")
		if s.mode == HAModeEnforced {
			ask += ", under which a member recreated in " + crowded + " cannot be placed"
		}
		s.finding(haFailsEnforced, haPinnedAdvice, "%s: %s; %s", svc.name, problem, ask)
		short = true
	}
	if !short {
		return
	}
	members := make([]string, len(pins))
	for i, p := range pins {
		members[i] = p.member
	}
	s.finding(haWarns, haPinnedAdvice, "%s: %s cannot be recreated on another node if their node fails: their "+
		"volumes are node-local", svc.name, joinAnd(members))
}

// pinUnusable says why node, which a node-local volume pins a member of a
// service of placement to, cannot take that member, or "" when it is one of
// the service's eligible nodes.
func (s *haTopologyScan) pinUnusable(node string, eligible []*corev1.Node, placement nodePlacement) string {
	if slices.ContainsFunc(eligible, func(n *corev1.Node) bool { return n.Name == node }) {
		return ""
	}
	for i := range s.nodes {
		if n := &s.nodes[i]; n.Name == node {
			if why := cmp.Or(nodeUnsupported(n), nodeIneligible(n)); why != "" {
				return "is not usable (" + why + ")"
			}
			return "its placement does not admit (" + placement.String() + ")"
		}
	}
	return "is not in the cluster"
}

// pinnedZoneSkew says how pins keep a quorum of n members from a zone spread
// of maxSkew 1 over the zones of the nodes it can run on and of the pinned
// nodes, and names the zones too crowded, or returns "". n members fit that
// spread only as lo or lo+1 per zone, lo+1 in n mod zones of them, and a zone
// holding more pinned members than that cannot shed them. A pinned node
// without a zone label leaves it unjudged: the zone findings cover it.
func pinnedZoneSkew(n int, pins []memberPin, eligible []*corev1.Node) (problem, crowded string) {
	counts := map[string]int{}
	for _, node := range eligible {
		if z := node.Labels[corev1.LabelTopologyZone]; z != "" {
			counts[z] = 0
		}
	}
	for _, p := range pins {
		if p.zone == "" {
			return "", ""
		}
		counts[p.zone]++
	}
	if len(counts) < 2 {
		return "", ""
	}
	lo, extra := n/len(counts), n%len(counts)
	hi := lo
	if extra > 0 {
		hi++
	}
	zones := slices.Sorted(maps.Keys(counts))
	var listed, over, above []string
	for _, z := range zones {
		listed = append(listed, fmt.Sprintf("%s %d", z, counts[z]))
		if counts[z] > hi {
			over = append(over, z)
		}
		if counts[z] > lo {
			above = append(above, z)
		}
	}
	if len(over) == 0 && len(above) <= extra {
		return "", ""
	}
	if len(over) == 0 {
		over = above
	}
	var placed []string
	for _, p := range pins {
		placed = append(placed, p.member+" on "+p.node)
	}
	allowed := fmt.Sprintf("%d per zone", lo)
	if extra > 0 {
		allowed = fmt.Sprintf("%d or %d per zone, %d in at most %d zone(s)", lo, hi, hi, extra)
	}
	crowded = joinAnd(over)
	verb := "holds"
	if len(over) > 1 {
		verb = "hold"
	}
	return fmt.Sprintf("node-local volumes pin members per zone %s (%s); %s %s more than a zone spread of maxSkew 1 "+
		"allows for %d members over %d zones (%s)", strings.Join(listed, ", "), strings.Join(placed, ", "), crowded,
		verb, n, len(zones), allowed), crowded
}

// judgeLocalVolumes warns, under either mode, when class provisions node-local
// volumes, as its bound PersistentVolumes that require one node by
// kubernetes.io/hostname show, whatever its provisioner is named: such a
// volume cannot be attached on another node, so a quorum member on one cannot
// be recreated elsewhere when its node fails. Without such a volume nothing
// tells, and nothing is reported.
func (s *haTopologyScan) judgeLocalVolumes(ctx context.Context, client kubernetes.Interface,
	class *storagev1.StorageClass) {
	pvs, ok := s.persistentVolumes(ctx, client)
	if !ok {
		return
	}
	var local []string
	for i := range pvs {
		pv := &pvs[i]
		if pv.Spec.StorageClassName == class.Name && pv.Status.Phase == corev1.VolumeBound && pinnedHostname(pv) != "" {
			local = append(local, pv.Name)
		}
	}
	if len(local) == 0 {
		return
	}
	s.finding(haWarns, haLocalStorageAdvice, "StorageClass %s provisions node-local volumes: %d bound "+
		"PersistentVolume(s) of it (%s) each require one node by %s, so a volume cannot be attached on another node "+
		"and a quorum member whose node fails cannot be recreated elsewhere until that node returns", class.Name,
		len(local), nameList(local), corev1.LabelHostname)
}
