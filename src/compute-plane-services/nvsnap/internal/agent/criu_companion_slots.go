// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Identical companions. An instance may have several companions of one
// configuration: the replicas of one Deployment (two Dynamo frontends).
// Each has its own checkpoint, but a new pod of that configuration can
// restore any of them, so the alias of the configuration names all their
// slots and admission hands them out one per placeholder.
//
// The pods of a Deployment are admitted concurrently, by any agent's
// webhook, and before they have a name. So a slot is claimed on the
// group record with a compare-and-swap: a claim holds the slot for the
// namespace until the admitted pod exists (companionClaimTTL); from then
// on the pod's own annotations hold it. Should two placeholders still end
// up with one slot, the restore driver refuses them (doubledSlot).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// companionClaimTTL is how long a slot claim outlasts its admission:
// longer than the API server takes to persist the admitted pod.
const companionClaimTTL = 2 * time.Minute

// companionClaimPrefix keys a slot claim in the group record:
// claim.<namespace>.<slot> = the claim's time.
const companionClaimPrefix = "claim."

// parseSlots reads the slots a companion's alias names ("slots", or
// "slot" in aliases from before identical companions).
func parseSlots(d map[string]string) []int {
	raw := d["slots"]
	if raw == "" {
		raw = d["slot"]
	}
	if raw == "" {
		return nil
	}
	fields := strings.Split(raw, ",")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// claimCompanionSlot is the webhook's CRIUCompanionSlot: it claims for pod
// the first of slots that no live placeholder of group g in its namespace
// holds and no recent admission there claimed.
func (a *Agent) claimCompanionSlot(ctx context.Context, pod *corev1.Pod, g webhook.CRIUGroup, slots []int) (int, error) {
	pods, err := a.kubeClient.CoreV1().Pods(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("cannot list the namespace's pods: %w", err)
	}
	held := map[int]bool{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || !isCompanion(p) || p.Annotations[webhook.CRIUGroupAnnotation] != g.Key {
			continue
		}
		if o, aerr := strconv.Atoi(p.Annotations[webhook.CRIUGroupOrdinalAnnotation]); aerr == nil {
			held[o] = true
		}
	}
	cms := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace())
	slot := -1
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, gerr := cms.Get(ctx, criuGroupConfigMapName(g.Key), metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		if !recordUsable(cm.Data) {
			return fmt.Errorf("group record is %q", cm.Data["state"])
		}
		slot = pickCompanionSlot(cm.Data, pod.Namespace, slots, held, time.Now())
		if slot < 0 {
			return fmt.Errorf("every slot %v of the configuration is taken in namespace %s", slots, pod.Namespace)
		}
		cm.Data[claimKey(pod.Namespace, slot)] = time.Now().UTC().Format(time.RFC3339Nano)
		_, uerr := cms.Update(ctx, cm, metav1.UpdateOptions{})
		return uerr
	})
	if err != nil {
		return 0, err
	}
	return slot, nil
}

func claimKey(ns string, slot int) string {
	return companionClaimPrefix + ns + "." + strconv.Itoa(slot)
}

// pickCompanionSlot returns the first of slots neither held nor claimed in
// namespace ns within companionClaimTTL of now, or -1. It drops the
// expired claims of every namespace from d.
func pickCompanionSlot(d map[string]string, ns string, slots []int, held map[int]bool, now time.Time) int {
	for k, v := range d {
		if !strings.HasPrefix(k, companionClaimPrefix) {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, v); err != nil || now.Sub(t) >= companionClaimTTL {
			delete(d, k)
		}
	}
	for _, s := range slots {
		if _, claimed := d[claimKey(ns, s)]; !held[s] && !claimed {
			return s
		}
	}
	return -1
}

// doubledSlot reports two live placeholders of leader's instance that hold
// one slot of group key: a restore can use neither.
func doubledSlot(pods []corev1.Pod, leader *corev1.Pod, key string) error {
	seen := map[int]string{}
	for _, p := range groupPlaceholders(pods, leader, key, false) {
		o, err := strconv.Atoi(p.Annotations[webhook.CRIUGroupOrdinalAnnotation])
		if err != nil {
			continue
		}
		if prev, ok := seen[o]; ok {
			return fmt.Errorf("placeholders %s and %s both hold slot %d", prev, p.Name, o)
		}
		seen[o] = p.Name
	}
	return nil
}
