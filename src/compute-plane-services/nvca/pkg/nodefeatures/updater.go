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

package nodefeatures

import (
	"context"
	"fmt"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

// NodeUpdater can update nodes to a state expected by NVCA.
type NodeUpdater struct {
	nodeClient      clientv1.NodeInterface
	clusterProvider string
}

func NewNodeUpdater(nodeClient clientv1.NodeInterface, clusterProvider string) *NodeUpdater {
	return &NodeUpdater{
		nodeClient:      nodeClient,
		clusterProvider: clusterProvider,
	}
}

// UpdateInstanceType ensures node has the instance-type label set,
// and can construct and apply that label if not.
func (u *NodeUpdater) UpdateInstanceType(ctx context.Context, node *v1.Node) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"node": node.Name,
	})

	// Clone node so cached node labels do not data race with other cache consumers.
	node = node.DeepCopy()
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}

	if !types.IsNodeReady(node) {
		if _, ok := node.Labels[UniformInstanceTypeLabelKey]; !ok {
			log.Debug("Skip labeling not-ready Node with instance type label")
			return nil
		}
		log.Info("Removing not-ready Node instance type label")
		// UniformInstanceTypeLabelKey exists update the node labels to remove
		return u.updateNodeLabels(ctx, node, "")
	}

	// node label is directly used to generate the InstanceTypeName
	labelVal, ok := node.Labels[gpuProductLabelOverrideKey]
	if !ok {
		labelVal, ok = node.Labels[gpuProductLabelKey]
		if !ok {
			log.Errorf("Neither GPU product labels %s or %s found", gpuProductLabelKey, gpuProductLabelOverrideKey)
			return fmt.Errorf("neither GPU product labels %s or %s found", gpuProductLabelKey, gpuProductLabelOverrideKey)
		}
	}

	gpuName, err := types.ParseGPUName(labelVal)
	if err != nil {
		log.WithError(err).Error("Failed to parse GPU name from label value")
		return err
	}

	itName := string(types.MakeInstanceName(u.clusterProvider, gpuName))

	if val, ok := node.Labels[UniformInstanceTypeLabelKey]; ok && val == itName {
		log.WithField("instance_type", itName).Debug("Node instance type label matches expected value")
		return nil
	}

	log.WithField("instance_type", itName).Info("Labeling Node with instance type")

	return u.updateNodeLabels(ctx, node, itName)
}

func (u *NodeUpdater) updateNodeLabels(ctx context.Context, node *v1.Node, itName string) error {
	log := core.GetLogger(ctx).WithFields(logrus.Fields{
		"node": node.Name,
	})
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		n, err := u.nodeClient.Get(ctx, node.Name, metav1.GetOptions{})

		// Track K8s API call metrics
		if m := metrics.FromContext(ctx); m != nil {
			m.TrackK8sAPICall("node", err)
		}

		if err != nil {
			return err
		}
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		// clear the label and add if needed
		delete(n.Labels, UniformInstanceTypeLabelKey)
		if itName != "" {
			n.Labels[UniformInstanceTypeLabelKey] = itName
		}
		_, err = u.nodeClient.Update(ctx, n, metav1.UpdateOptions{})
		return err
	}); err != nil {
		log.WithError(err).Error("Failed to update Node labels")
		return err
	}
	return nil
}
