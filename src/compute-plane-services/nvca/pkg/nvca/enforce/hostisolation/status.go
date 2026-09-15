/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package hostisolation

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/sets"
	listersv1 "k8s.io/client-go/listers/core/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	// ComponentName for health checks.
	ComponentName = "host-isolation"
)

func NewStatusGetter(
	nodeLister listersv1.NodeLister,
	podLister listersv1.PodLister,
) health.ComponentStatusGetter {
	return health.GetComponentStatusFunc(func(ctx context.Context) (hs types.AgentHealth, err error) {
		ch := types.ComponentHealth{
			Status:      types.HealthStatusHealthy,
			StatusLevel: types.StatusLevelError,
		}
		if err := validateNoMixedTenants(ctx, nodeLister, podLister); err != nil {
			ch.Status = types.HealthStatusUnhealthy
			ch.Errors = append(ch.Errors, err.Error())
			ch.StatusLevel = types.StatusLevelError
		}
		hs.Components = map[string]types.ComponentHealth{
			ComponentName: ch,
		}
		return hs, nil
	})
}

var ncaIDReq labels.Requirement

func init() {
	req, err := labels.NewRequirement(types.NCAIDKey, selection.Exists, nil)
	if err != nil {
		panic(err)
	}
	ncaIDReq = *req
}

// validateNoMixedTenants returns an error if any node contains two or more pods
// from different tenants by NCA ID.
func validateNoMixedTenants(ctx context.Context,
	nodeLister listersv1.NodeLister,
	podLister listersv1.PodLister,
) error {
	log := core.GetLogger(ctx)

	nodes, err := nodeLister.List(labels.Everything())
	if err != nil {
		log.WithError(err).Error("Failed to list nodes to check mixed tenancy")
		return err
	}

	pods, err := podLister.List(labels.NewSelector().Add(ncaIDReq))
	if err != nil {
		log.WithError(err).Error("Failed to list pods to check mixed tenancy")
		return err
	}

	nodeToNCAIDs := map[string]sets.Set[string]{}
	for _, node := range nodes {
		nodeToNCAIDs[node.Name] = sets.Set[string]{}
	}

	mixedNodeToPodNCAIDs := map[string]sets.Set[string]{}
	for _, pod := range pods {
		ncaIDs, podOnKnownNode := nodeToNCAIDs[pod.Spec.NodeName]
		if !podOnKnownNode {
			continue
		}
		podNCAID, hasNCAIDLabel := types.GetNCAIDLabelVal(pod.Labels)
		if !hasNCAIDLabel {
			continue
		}
		if ncaIDs.Len() == 0 {
			ncaIDs.Insert(podNCAID)
		} else if !ncaIDs.Has(podNCAID) {
			mixedNCAIDs, ok := mixedNodeToPodNCAIDs[pod.Spec.NodeName]
			if !ok {
				mixedNCAIDs = ncaIDs.Clone()
				mixedNodeToPodNCAIDs[pod.Spec.NodeName] = mixedNCAIDs
			}
			mixedNCAIDs.Insert(podNCAID)
		}
	}

	if len(mixedNodeToPodNCAIDs) != 0 {
		sb := strings.Builder{}
		i, l := 0, len(mixedNodeToPodNCAIDs)
		for nodeName, ncaIDSet := range mixedNodeToPodNCAIDs {
			sb.WriteString(nodeName)
			sb.WriteRune('=')
			ncaIDs := ncaIDSet.UnsortedList()
			sort.Strings(ncaIDs)
			sb.WriteString(strings.Join(ncaIDs, ","))
			if i < l-1 {
				sb.WriteString("; ")
			}
			i++
		}
		return fmt.Errorf("mixed tenants on nodes: %s", sb.String())
	}

	return nil
}
