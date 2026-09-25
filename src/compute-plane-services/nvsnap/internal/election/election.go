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

// Package election picks one downloader per cache hash when a chart
// creates N model workers at once. The webhook calls Elect for every
// model workload it admits; exactly one admission per hash wins, because
// the election is a Kubernetes Lease create, which is atomic. The winner
// is decorated as the capture source, the losers as gated restore pods
// that nvsnap-server releases once the capture is promoted.
//
// Design: docs/proposals/helm-chart-cache-election.md.
package election

import (
	"context"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// Pod metadata the election stamps and later selects on.
const (
	// HashAnnotation carries the full cache hash the webhook composed at
	// admission. The capture watcher reads it instead of recomposing from
	// the live pod, so both sides agree by construction.
	HashAnnotation = "nvsnap.io/hash"
	// HashLabel carries the short hash, so nvsnap-server can list every
	// pod of a hash with a label selector.
	HashLabel = "nvsnap.io/hash"
	// RoleAnnotation records the admission decision: leader, follower or
	// restore.
	RoleAnnotation = "nvsnap.io/role"
	// GatedLabel is "true" while a follower holds the scheduling gate.
	GatedLabel = "nvsnap.io/gated"
	// GateName is the schedulingGates entry a follower carries until the
	// capture for its hash is promoted.
	GateName = "nvsnap.io/wait-for-cache"
	// ColdStartAnnotation is stamped on a follower whose leader failed;
	// the pod is recreated by its controller and re-admitted.
	ColdStartAnnotation = "nvsnap.io/cold-start"

	// LeaseKindLabel and LeaseKindValue mark election Leases so the
	// server reconciler can list them.
	LeaseKindLabel = "nvsnap.io/kind"
	LeaseKindValue = "capture-election"
	// ElectionIDAnnotation on the leader pod is the id the webhook minted
	// at admission and used as the Lease holder. A pod has no UID or name
	// yet when a mutating webhook sees its CREATE, so the webhook's own id
	// is the only handle that exists on both the Lease and the pod.
	ElectionIDAnnotation = "nvsnap.io/election-id"
	// LeaderNamespaceAnnotation and LeaderIDAnnotation on the Lease name
	// the leader pod (namespace + election id) so its liveness can be
	// checked.
	LeaderNamespaceAnnotation = "nvsnap.io/leader-namespace"
	LeaderIDAnnotation        = "nvsnap.io/leader-id"
	// DeadlineAnnotation is the RFC3339 time after which the reconciler
	// treats the leader as failed even if its pod is still around.
	DeadlineAnnotation = "nvsnap.io/deadline"

	// DefaultDeadline bounds a cold start plus a capture.
	DefaultDeadline = 60 * time.Minute
)

// Role is the admission decision for a model workload.
type Role string

// Roles: the leader captures, followers wait gated for its promote, and
// restore means a promoted capture already existed at admission.
const (
	RoleLeader   Role = "leader"
	RoleFollower Role = "follower"
	RoleRestore  Role = "restore"
)

// Elector decides who downloads. Implementations must be safe for
// concurrent admissions of the same hash.
type Elector interface {
	// Elect returns RoleLeader for exactly one live election per hash and
	// RoleFollower for every other caller while that election stands. The
	// returned id is the Lease holder; the webhook stamps it on the leader
	// as ElectionIDAnnotation so the server can find the leader later.
	Elect(ctx context.Context, hash string, pod *corev1.Pod) (Role, string, error)
}

// LeaseName is the election Lease for a hash.
func LeaseName(hash string) string { return "nvsnap-capture-" + checkpointstore.ShortHash(hash) }

// PodIdentity names a pod at admission. Deployment and DynamoGraph pods
// have neither name nor UID yet at CREATE, only generateName.
func PodIdentity(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	name := pod.Name
	if name == "" {
		name = pod.GenerateName + "*"
	}
	if pod.UID != "" {
		return fmt.Sprintf("%s/%s(%s)", pod.Namespace, name, pod.UID)
	}
	return pod.Namespace + "/" + name
}

// LeaseElector elects through a Lease create in Namespace.
type LeaseElector struct {
	KubeClient kubernetes.Interface
	// Namespace holds the election Leases (nvsnap-system).
	Namespace string
	// Deadline bounds the leader's cold start plus capture. Zero means
	// DefaultDeadline.
	Deadline time.Duration
	// Now is a clock seam for tests.
	Now func() time.Time
	// NewID mints the election id; a seam for tests. nil uses a UUID.
	NewID func() string
}

func (e *LeaseElector) newID() string {
	if e.NewID != nil {
		return e.NewID()
	}
	return string(uuid.NewUUID())
}

func (e *LeaseElector) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *LeaseElector) deadline() time.Duration {
	if e.Deadline <= 0 {
		return DefaultDeadline
	}
	return e.Deadline
}

// Elect creates the Lease for hash with a freshly minted id as holder.
// Created means leader; AlreadyExists means follower; anything else is an
// error the caller fails open on.
func (e *LeaseElector) Elect(ctx context.Context, hash string, pod *corev1.Pod) (Role, string, error) {
	if e.KubeClient == nil {
		return "", "", fmt.Errorf("election: no kube client")
	}
	if pod == nil {
		return "", "", fmt.Errorf("election: nil pod")
	}
	now := e.now()
	secs := int32(e.deadline().Seconds())
	holder := e.newID()
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      LeaseName(hash),
			Namespace: e.Namespace,
			Labels: map[string]string{
				LeaseKindLabel: LeaseKindValue,
				HashLabel:      checkpointstore.ShortHash(hash),
			},
			Annotations: map[string]string{
				HashAnnotation:            hash,
				LeaderNamespaceAnnotation: pod.Namespace,
				LeaderIDAnnotation:        holder,
				DeadlineAnnotation:        now.Add(e.deadline()).UTC().Format(time.RFC3339),
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &secs,
			AcquireTime:          &metav1.MicroTime{Time: now},
		},
	}
	_, err := e.KubeClient.CoordinationV1().Leases(e.Namespace).Create(ctx, lease, metav1.CreateOptions{})
	switch {
	case err == nil:
		return RoleLeader, holder, nil
	case apierrors.IsAlreadyExists(err):
		return RoleFollower, "", nil
	default:
		return "", "", fmt.Errorf("election: create lease %s: %w", lease.Name, err)
	}
}
