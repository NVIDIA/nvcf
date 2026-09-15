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

package sharedcluster

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	// ScheduleLabelKey being present on a node with any value indicates
	// that any ICMS request instance can be scheduled there.
	// The presence of this label on any cluster node indicates that
	// the cluster is shared; the lack of this label on all cluster nodes
	// indicates the cluster is not shared and any node is available for scheduling.
	ScheduleLabelKey = "nvca.nvcf.nvidia.io/schedule"

	trueVal = "true"
)

var (
	subMu sync.Mutex
	sem   int64

	sharedClusterOn = &atomic.Bool{}

	initOnce    sync.Once
	initialized bool
)

func AddNodePublisher(ctx context.Context,
	inf cache.SharedIndexInformer,
) (*atomic.Bool, cache.InformerSynced, error) {
	log := core.GetLogger(ctx).WithField("rpc", "sharedcluster.NodePublisher")

	if initialized {
		panic("code bug: shared cluster node publisher already initialized")
	}
	initOnce.Do(func() { initialized = true })

	// Every time a node event occurs, notify the receiver.
	eh, err := inf.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, ok := toNode(log, obj)
			if !ok {
				return
			}
			log.WithField("name", node.Name).Info("Add node event")
			if node.Labels[ScheduleLabelKey] == trueVal {
				addNotify(ctx, 1)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNode, ok := toNode(log, oldObj)
			if !ok {
				return
			}
			newNode, ok := toNode(log, newObj)
			if !ok {
				return
			}
			log := log.WithField("name", newNode.Name)

			oldVal := oldNode.Labels[ScheduleLabelKey]
			newVal := newNode.Labels[ScheduleLabelKey]

			var val int64 = 0
			switch {
			case oldVal == trueVal && newVal == trueVal:
				log.Debug("Update node event")
			case oldVal != trueVal && newVal == trueVal:
				log.Info("Update node event on label addition")
				val = 1
			case oldVal == trueVal && newVal != trueVal:
				log.Info("Update node event on label removal")
				val = -1
			default:
				// Both false, no need to acquire lock.
				return
			}

			addNotify(ctx, val)
		},
		DeleteFunc: func(obj interface{}) {
			node, ok := toNode(log, obj)
			if !ok {
				return
			}
			log.WithField("name", node.Name).Info("Delete node event")
			if node.Labels[ScheduleLabelKey] == trueVal {
				addNotify(ctx, -1)
			}
		},
	})
	if err != nil {
		return nil, nil, err
	}

	return sharedClusterOn, eh.HasSynced, nil
}

func toNode(log logrus.FieldLogger, obj any) (*v1.Node, bool) {
	node, ok := obj.(*v1.Node)
	if !ok {
		log.Errorf("expected *corev1.Node, got %T", obj)
		return nil, false
	}
	return node, true
}

func addNotify(ctx context.Context, i int64) {
	subMu.Lock()
	defer subMu.Unlock()
	if i == 0 {
		return
	}
	if sem+i == 1 || sem+i == 0 {
		on := sem+i == 1
		core.GetLogger(ctx).
			WithField("shared_cluster", on).
			Info("Shared cluster mode change, notifying subscribers")
		sharedClusterOn.Store(on)
	}
	if sem+i < 0 {
		sem = 0
	} else {
		sem += i
	}
}
