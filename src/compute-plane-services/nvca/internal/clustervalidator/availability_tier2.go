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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
)

// tier2Placement is how the Tier-2 row judges where a quorum StatefulSet's
// Ready peers run.
type tier2Placement int

const (
	// tier2PlacementUnjudged: nothing asks for its pods on distinct nodes.
	tier2PlacementUnjudged tier2Placement = iota
	// tier2PlacementFails: two Ready peers on one node fail the row.
	tier2PlacementFails
	// tier2PlacementWarns: two Ready peers on one node are a failover risk,
	// warned about while the row passes.
	tier2PlacementWarns
	// tier2PlacementDelegated: the HA Guarantee row judges it against the
	// HA mode.
	tier2PlacementDelegated
)

// tier2PlacementFor is how the Tier-2 row judges sts's placement under mode.
// Under preferred or enforced the HA Guarantee row judges the stack's own
// quorum members (haQuorumMember). Under none any anti-affinity or hostname
// spread of its own pods asks for distinct nodes, as before HA modes existed.
// Otherwise its rendered terms stand in for the mode: a required term is
// judged as enforced, and a preferred term or a spread as preferred, which
// allows peers to share a node when nodes are short.
func tier2PlacementFor(mode HAMode, sts *appsv1.StatefulSet) tier2Placement {
	if mode.guaranteed() && haQuorumMember(sts) {
		return tier2PlacementDelegated
	}
	switch hostnameSeparation(&sts.Spec.Template, sts.Namespace) {
	case separationNone:
		return tier2PlacementUnjudged
	case separationHard:
		return tier2PlacementFails
	}
	if mode == HAModeNone {
		return tier2PlacementFails
	}
	return tier2PlacementWarns
}

// judged reports whether the Tier-2 row judges the placement itself.
func (p tier2Placement) judged() bool { return p == tier2PlacementFails || p == tier2PlacementWarns }

// coLocated reports two Ready peers of sts, first and second, on node.
func (s *tier2Scan) coLocated(ns string, sts *appsv1.StatefulSet, first, second, node string) {
	if tier2PlacementFor(s.state.HAMode, sts) != tier2PlacementWarns {
		s.fail(tier2CoLocatedAdvice, "%s/%s: pods %s and %s are co-located on node %s", ns, sts.Name, first, second,
			node)
		return
	}
	s.warn(fmt.Sprintf("failover risk: %s/%s pods %s and %s share node %s, which its preferred anti-affinity or "+
		"hostname spread allows, so a node loss takes both", ns, sts.Name, first, second, node))
	if ref := ns + "/" + sts.Name; len(s.sharing) == 0 || s.sharing[len(s.sharing)-1] != ref {
		s.sharing = append(s.sharing, ref)
	}
}

// readyMessage is the Tier-2 success line, which claims distinct nodes only
// for the StatefulSets it judged that way.
func (s *tier2Scan) readyMessage() string {
	if len(s.unspread) == 0 && len(s.sharing) == 0 && len(s.delegated) == 0 {
		return fmt.Sprintf("All %d quorum StatefulSet(s) Ready on distinct nodes", s.checkedCount)
	}
	var parts []string
	if n := s.checkedCount - len(s.unspread) - len(s.sharing) - len(s.delegated); n > 0 {
		parts = append(parts, fmt.Sprintf("%d on distinct nodes", n))
	}
	if n := len(s.sharing); n > 0 {
		parts = append(parts, fmt.Sprintf("%d with peers sharing a node", n))
	}
	if n := len(s.unspread); n > 0 {
		parts = append(parts, fmt.Sprintf("%d placement not assessed", n))
	}
	if n := len(s.delegated); n > 0 {
		parts = append(parts, fmt.Sprintf("placement of %s judged under %s", strings.Join(s.delegated, ", "),
			haGuaranteeLabel))
	}
	return fmt.Sprintf("All %d quorum StatefulSet(s) Ready (%s)", s.checkedCount, strings.Join(parts, "; "))
}

// tier2PassMessage is the detail of a passing Tier-2 row.
func tier2PassMessage(state *ValidationState) string {
	switch {
	case state.Tier2PeersSharingANode > 0:
		return fmt.Sprintf("Quorum OK (%d with peers sharing a node; see warnings)", state.Tier2PeersSharingANode)
	case state.Tier2PlacementNotAssessed > 0:
		return fmt.Sprintf("Ready (placement not assessed for %d)", state.Tier2PlacementNotAssessed)
	case state.Tier2PlacementDelegated > 0:
		return fmt.Sprintf("Quorum OK (placement of %d judged under %s)", state.Tier2PlacementDelegated,
			haGuaranteeLabel)
	}
	return "Quorum and Placement OK"
}
