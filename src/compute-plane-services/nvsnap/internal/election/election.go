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
// Design: docs/design/helm-chart-cache-election.md.
package election

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

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
	// LeaderSpecAnnotation on the Lease is the SpecIdentity of the leader
	// as admitted: what it runs, which a capture under the election's
	// hash must match.
	LeaderSpecAnnotation = "nvsnap.io/leader-spec"
	// LeaderUIDAnnotation on the Lease is the UID of the leader pod,
	// bound once the pod exists (see ResolveLeader). The election id is
	// readable on the leader pod and any pod in its namespace can copy
	// it; the UID is the apiserver's and cannot be copied.
	LeaderUIDAnnotation = "nvsnap.io/leader-uid"

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
	// MainContainer is the index of the model container, the same one the
	// webhook composes the hash from and the watcher captures.
	MainContainer int
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

// VerifyStamp implements the capture watcher's check of a stamped hash: nil
// when pod is the leader of the live election for hash, on a node of the
// compute capability nodeCC the hash was elected for. The election Lease
// lives in the election namespace, which pod authors cannot write. It
// names the leader by namespace and by an id minted at admission, but the
// id is readable on the leader pod and can be copied onto another pod, so
// the Lease also binds the leader's UID (ResolveLeader) and records what
// the leader runs (SpecIdentity): a pod that copies the hash, the id and
// the pin is neither the bound UID nor, unless it runs the same model
// container, the same spec. The compute capability the hash was composed
// for is the one admission pinned the pod to.
func (e *LeaseElector) VerifyStamp(ctx context.Context, pod *corev1.Pod, hash, nodeCC string) error {
	if e.KubeClient == nil {
		return fmt.Errorf("no kube client")
	}
	if pod == nil {
		return fmt.Errorf("nil pod")
	}
	lease, err := e.KubeClient.CoordinationV1().Leases(e.Namespace).Get(ctx, LeaseName(hash), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("no live election for the hash")
		}
		return fmt.Errorf("read the election: %w", err)
	}
	id := pod.Annotations[ElectionIDAnnotation]
	switch {
	case lease.Annotations[HashAnnotation] != hash:
		return fmt.Errorf("the election is for another hash")
	case id == "" || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != id || lease.Annotations[LeaderIDAnnotation] != id:
		return fmt.Errorf("the pod is not the election's leader")
	case lease.Annotations[LeaderNamespaceAnnotation] != pod.Namespace:
		return fmt.Errorf("the election's leader is in another namespace")
	case lease.Annotations[LeaderSpecAnnotation] == "" || lease.Annotations[LeaderSpecAnnotation] != SpecIdentity(pod, e.MainContainer):
		return fmt.Errorf("the pod does not run what the election's leader was admitted with")
	}
	uid, err := e.bindLeader(ctx, lease)
	if err != nil {
		return err
	}
	if uid == "" || pod.UID != uid {
		return fmt.Errorf("the pod is not the election's leader pod")
	}
	pinned := PinnedComputeCapability(pod)
	switch {
	case pinned == "":
		return fmt.Errorf("the pod is not pinned to a GPU architecture")
	case nodeCC != pinned:
		return fmt.Errorf("the node's compute capability %q is not the elected %q", nodeCC, pinned)
	}
	return nil
}

// bindLeader returns the UID of the election's leader pod, recording it on
// the Lease the first time it is resolved so later checks do not depend
// on which pods carry the id by then. "" means the leader pod is gone.
func (e *LeaseElector) bindLeader(ctx context.Context, lease *coordinationv1.Lease) (types.UID, error) {
	leases := e.KubeClient.CoordinationV1().Leases(e.Namespace)
	var bound types.UID
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if uid := lease.Annotations[LeaderUIDAnnotation]; uid != "" {
			bound = types.UID(uid)
			return nil
		}
		pods, err := e.KubeClient.CoreV1().Pods(lease.Annotations[LeaderNamespaceAnnotation]).List(ctx, metav1.ListOptions{
			LabelSelector: HashLabel + "=" + checkpointstore.ShortHash(lease.Annotations[HashAnnotation]),
		})
		if err != nil {
			return fmt.Errorf("list the election's leader candidates: %w", err)
		}
		leader, err := ResolveLeader(lease, pods.Items)
		if err != nil || leader == nil {
			return err
		}
		next := lease.DeepCopy()
		next.Annotations[LeaderUIDAnnotation] = string(leader.UID)
		if _, err := leases.Update(ctx, next, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				// Another check bound it, or the Lease was renewed or
				// replaced: re-read and decide again.
				cur, gerr := leases.Get(ctx, lease.Name, metav1.GetOptions{})
				if gerr != nil {
					return gerr
				}
				if cur.UID != lease.UID {
					return fmt.Errorf("the election was replaced")
				}
				lease = cur
			}
			return err
		}
		bound = leader.UID
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("bind the election's leader: %w", err)
	}
	return bound, nil
}

// ResolveLeader picks the leader of lease among pods, the pods of its hash
// in the leader's namespace. A UID bound on the Lease decides. Before it
// is bound, the leader is the earliest-created pod carrying the election
// id: the id exists only on the Lease, which pod authors cannot read, and
// on the leader as admitted, so a pod that copied it was created after
// the leader. Two pods created in the same second cannot be told apart
// and the election is refused rather than guessed. Returns nil, nil when
// no pod is the leader (it is gone).
func ResolveLeader(lease *coordinationv1.Lease, pods []corev1.Pod) (*corev1.Pod, error) {
	ns := lease.Annotations[LeaderNamespaceAnnotation]
	if uid := types.UID(lease.Annotations[LeaderUIDAnnotation]); uid != "" {
		for i := range pods {
			if pods[i].UID == uid && pods[i].Namespace == ns {
				return &pods[i], nil
			}
		}
		return nil, nil
	}
	id := lease.Annotations[LeaderIDAnnotation]
	var candidates []*corev1.Pod
	for i := range pods {
		if p := &pods[i]; id != "" && p.Namespace == ns && p.Annotations[ElectionIDAnnotation] == id {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CreationTimestamp.Before(&candidates[j].CreationTimestamp)
	})
	if len(candidates) > 1 && candidates[1].CreationTimestamp.Equal(&candidates[0].CreationTimestamp) {
		return nil, fmt.Errorf("%d pods created at the same time carry the election id; the leader cannot be told apart", len(candidates))
	}
	return candidates[0], nil
}

// SpecIdentity digests what the model container at index main runs: its
// name, image repository, command and args. Admission records it on the
// Lease for the leader, and the stamp check requires it of the pod it
// captures, so a capture published under the elected hash comes from a
// pod running the model the hash was composed for. The env is left out
// because the webhook injects into it, and the image's tag and digest
// because another webhook may pin or rewrite them after ours; the version
// is already part of the hash. "" when the pod has no such container.
func SpecIdentity(pod *corev1.Pod, main int) string {
	if pod == nil || main < 0 || main >= len(pod.Spec.Containers) {
		return ""
	}
	c := pod.Spec.Containers[main]
	b, err := json.Marshal(struct {
		Name    string   `json:"name"`
		Image   string   `json:"image"`
		Command []string `json:"command"`
		Args    []string `json:"args"`
	}{c.Name, imageRepo(c.Image), c.Command, c.Args})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// imageRepo drops the tag and digest of an image reference, keeping the
// registry and repository: "reg:5000/org/img:v1@sha256:..." is
// "reg:5000/org/img".
func imageRepo(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
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
				LeaderSpecAnnotation:      SpecIdentity(pod, e.MainContainer),
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
