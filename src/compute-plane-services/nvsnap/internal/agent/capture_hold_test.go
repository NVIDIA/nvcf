// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8stesting "k8s.io/client-go/testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/capturehold"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// held makes p wait on the capture hold, its containers Ready since warmAt.
func held(p corev1.Pod, warmAt time.Time) *corev1.Pod {
	p.Spec.ReadinessGates = []corev1.PodReadinessGate{{ConditionType: capturehold.Condition}}
	p.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse},
		{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(warmAt)},
	}
	return &p
}

func settled(t *testing.T, kc kubernetes.Interface, p *corev1.Pod) bool {
	t.Helper()
	got, err := kc.CoreV1().Pods(p.Namespace).Get(context.Background(), p.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return capturehold.Settled(got)
}

func createPods(t *testing.T, kc kubernetes.Interface, pods ...*corev1.Pod) {
	t.Helper()
	for _, p := range pods {
		if _, err := kc.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

// A lone pod is released once its capture is decided, and held while it
// is not; the release keeps the pod's other conditions.
func TestConsiderCaptureHold_LonePod(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	p := held(stsRank("lone", "0", "1", "rs-1", false), time.Now())
	createPods(t, kc, p)

	c.considerCaptureHold(ctx, p, true)
	if settled(t, kc, p) {
		t.Fatal("released while its capture is undecided")
	}
	c.considerCaptureHold(ctx, p, false)
	if !settled(t, kc, p) {
		t.Fatal("not released once its capture was decided")
	}
	got, _ := kc.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
	if !capturehold.Warm(got) {
		t.Errorf("the pod's other conditions were not kept: %v", got.Status.Conditions)
	}
}

// Past the hold limit a pod is released whatever holds it; within it,
// an undecided pod is not.
func TestConsiderCaptureHold_Limit(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	old := held(stsRank("old", "0", "1", "rs-1", false), time.Now().Add(-captureHoldLimit-time.Minute))
	rank := held(stsRank("rank-1", "1", "2", "sts-a", false), time.Now().Add(-captureHoldLimit-time.Minute))
	young := held(stsRank("young", "0", "1", "rs-2", false), time.Now().Add(-captureHoldLimit+time.Minute))
	createPods(t, kc, old, rank, young)
	for _, p := range []*corev1.Pod{old, rank, young} {
		c.considerCaptureHold(ctx, p, true)
	}
	if !settled(t, kc, old) || !settled(t, kc, rank) {
		t.Error("a pod held past the limit was not released")
	}
	if settled(t, kc, young) {
		t.Error("a pod within the limit was released while undecided")
	}
}

// The other ranks of a multi-pod instance wait for their leader, which
// releases its own instance's ranks and no other's.
func TestCaptureHold_MultiPodInstance(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	now := time.Now()
	r0 := held(stsRank("kimi-0", "0", "2", "sts-a", false), now)
	r1 := held(stsRank("kimi-1", "1", "2", "sts-a", false), now)
	other := held(stsRank("other-1", "1", "2", "sts-b", false), now)
	createPods(t, kc, r0, r1, other)

	c.considerCaptureHold(ctx, r1, false)
	if settled(t, kc, r1) {
		t.Fatal("a rank was released before its leader decided")
	}
	c.considerCaptureHold(ctx, r0, false)
	if !settled(t, kc, r0) || !settled(t, kc, r1) {
		t.Error("the leader's decision did not release the instance")
	}
	if settled(t, kc, other) {
		t.Error("another instance's rank was released")
	}
}

// A pod not held, or being deleted, is left alone.
func TestConsiderCaptureHold_LeavesOthersAlone(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	plain := stsRank("plain", "0", "1", "rs-1", true)
	gone := held(stsRank("gone", "0", "1", "rs-2", false), time.Now())
	now := metav1.Now()
	gone.DeletionTimestamp = &now
	createPods(t, kc, &plain, gone)
	c.considerCaptureHold(ctx, &plain, false)
	c.considerCaptureHold(ctx, gone, false)
	got, _ := kc.CoreV1().Pods("fn").Get(ctx, "plain", metav1.GetOptions{})
	for _, cond := range got.Status.Conditions {
		if cond.Type == capturehold.Condition {
			t.Error("a pod that is not held got the condition")
		}
	}
	if settled(t, kc, gone) {
		t.Error("a pod being deleted was released")
	}
}

// considerGroupCapture is undecided until the pod is warm, and decided
// for a pod no capture applies to.
func TestConsiderGroupCapture_Decision(t *testing.T) {
	c, _ := trustFixture(t)
	ctx := context.Background()
	cold := stsRank("cold", "0", "1", "rs-1", false)
	if !c.considerGroupCapture(ctx, &cold) {
		t.Error("a pod not warm yet is decided")
	}
	fallback := held(stsRank("fallback", "0", "1", "rs-1", false), time.Now())
	fallback.Annotations[webhook.FallbackReasonAnnotation] = "gpushare-failed"
	if c.considerGroupCapture(ctx, fallback) {
		t.Error("a pod admitted to fall back is undecided")
	}
	unranked := held(stsRank("unranked", "0", "1", "rs-1", false), time.Now())
	unranked.Annotations = map[string]string{}
	if c.considerGroupCapture(ctx, unranked) {
		t.Error("a pod no capture applies to is undecided")
	}
	decided := held(stsRank("decided", "0", "1", "rs-1", false), time.Now())
	c.captureDecided.Store(decided.UID, struct{}{})
	if c.considerGroupCapture(ctx, decided) {
		t.Error("a pod whose capture is over is undecided")
	}
}

// A rank other than 0 is released once its leader is not held (a
// replacement rank of a running instance starts cold), and held while
// its leader is.
func TestConsiderCaptureHold_RankFollowsItsLeader(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	now := time.Now()
	leader := stsRank("kimi-0", "0", "2", "sts-a", true) // serving, not held
	rank := held(stsRank("kimi-1", "1", "2", "sts-a", false), now)
	heldLeader := held(stsRank("dyn-0", "0", "2", "sts-b", false), now)
	heldRank := held(stsRank("dyn-1", "1", "2", "sts-b", false), now)
	createPods(t, kc, &leader, rank, heldLeader, heldRank)
	c.considerCaptureHold(ctx, rank, true)
	if !settled(t, kc, rank) {
		t.Error("a rank whose leader serves stays held")
	}
	c.considerCaptureHold(ctx, heldRank, true)
	if settled(t, kc, heldRank) {
		t.Error("a rank was released while its leader is held")
	}
}

// Once its capture has started, a pod is held for the capture's timeout
// from then, however long it was warm before.
func TestHoldExpired_Capturing(t *testing.T) {
	longWarm := time.Now().Add(-captureHoldLimit - time.Hour)
	p := held(stsRank("p", "0", "1", "rs-1", false), longWarm)
	if !holdExpired(p) {
		t.Fatal("a pod warm past the limit is not expired")
	}
	p.Status.Conditions = append(p.Status.Conditions, corev1.PodCondition{Type: capturehold.Condition, Status: corev1.ConditionFalse,
		Reason: capturehold.CapturingReason, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Minute))})
	if holdExpired(p) {
		t.Error("a pod whose capture started a minute ago is expired")
	}
	p.Status.Conditions[len(p.Status.Conditions)-1].LastTransitionTime = metav1.NewTime(time.Now().Add(-capturingHoldLimit - time.Minute))
	if !holdExpired(p) {
		t.Error("a capture past its timeout still holds the pod")
	}
}

// A pod held past the limit is not captured (it is released into
// service); a pod whose capture runs stays undecided whatever its record
// says.
func TestConsiderGroupCapture_ExpiredAndRunning(t *testing.T) {
	c, _ := trustFixture(t)
	ctx := context.Background()
	expired := held(stsRank("expired", "0", "1", "rs-1", false), time.Now().Add(-captureHoldLimit-time.Minute))
	if c.considerGroupCapture(ctx, expired) {
		t.Error("a pod held past the limit is undecided")
	}
	if _, done := c.captureDecided.Load(expired.UID); !done {
		t.Error("a pod held past the limit may still be captured")
	}
	running := held(stsRank("running", "0", "1", "rs-1", false), time.Now())
	c.attempted.Store("capture/"+string(running.UID), struct{}{})
	if !c.considerGroupCapture(ctx, running) {
		t.Error("a pod whose capture runs is decided")
	}
}

// holdForCapture marks the held members and leaves the others alone; a
// member already released into service stops the capture.
func TestHoldForCapture(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	h := held(stsRank("h", "0", "1", "rs-1", false), time.Now())
	plain := stsRank("plain", "0", "1", "rs-2", true)
	createPods(t, kc, h, &plain)
	log := c.a.log.WithField("t", "t")
	if serves, err := c.holdForCapture(ctx, []*corev1.Pod{h, &plain}, log); serves != "" || err != nil {
		t.Fatalf("holdForCapture: serves %q err %v", serves, err)
	}
	got, _ := kc.CoreV1().Pods("fn").Get(ctx, "h", metav1.GetOptions{})
	cond := capturehold.Get(got)
	if cond == nil || cond.Status != corev1.ConditionFalse || cond.Reason != capturehold.CapturingReason {
		t.Fatalf("held member not marked capturing: %+v", cond)
	}
	if !capturehold.Held(got) {
		t.Error("marking released the member")
	}
	got, _ = kc.CoreV1().Pods("fn").Get(ctx, "plain", metav1.GetOptions{})
	if capturehold.Get(got) != nil {
		t.Error("a member without the gate was marked")
	}

	// Released meanwhile (its hold expired): the capture must not start,
	// and the release is not overwritten.
	r := held(stsRank("r", "0", "1", "rs-3", false), time.Now())
	createPods(t, kc, r)
	cur, _ := kc.CoreV1().Pods("fn").Get(ctx, "r", metav1.GetOptions{})
	c.releaseCaptureHold(ctx, cur, "HoldExpired", "test")
	if serves, _ := c.holdForCapture(ctx, []*corev1.Pod{r}, log); serves != "r" {
		t.Errorf("a released member did not stop the capture: serves %q", serves)
	}
	if !settled(t, kc, r) {
		t.Error("the release was overwritten")
	}
}

// Every change of the hold is conditional on the version of the pod it
// was decided on.
func TestPatchCaptureHold_Conditional(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	p := held(stsRank("p", "0", "1", "rs-1", false), time.Now())
	p.ResourceVersion = "42"
	var body []byte
	kc.PrependReactor("patch", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if pa, ok := a.(k8stesting.PatchAction); ok && pa.GetSubresource() == "status" {
			body = pa.GetPatch()
		}
		return false, nil, nil
	})
	createPods(t, kc, p)
	_ = c.a.patchCaptureHold(ctx, p, corev1.ConditionTrue, "r", "m")
	var got struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &got); err != nil || got.Metadata.ResourceVersion != "42" {
		t.Errorf("patch %s: no resourceVersion precondition (%v)", body, err)
	}
}

// A StatefulSet of several pods created in order is not held; one created
// in parallel, one of one pod, and a Deployment's pods are.
func TestCaptureHoldAdmits(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	two, one := int32(2), int32(1)
	for _, s := range []appsv1.StatefulSet{
		{ObjectMeta: metav1.ObjectMeta{Name: "ordered", Namespace: "fn"}, Spec: appsv1.StatefulSetSpec{Replicas: &two}},
		{ObjectMeta: metav1.ObjectMeta{Name: "parallel", Namespace: "fn"}, Spec: appsv1.StatefulSetSpec{Replicas: &two, PodManagementPolicy: appsv1.ParallelPodManagement}},
		{ObjectMeta: metav1.ObjectMeta{Name: "single", Namespace: "fn"}, Spec: appsv1.StatefulSetSpec{Replicas: &one}},
		{ObjectMeta: metav1.ObjectMeta{Name: "from1", Namespace: "fn"}, Spec: appsv1.StatefulSetSpec{Replicas: &two, PodManagementPolicy: appsv1.ParallelPodManagement,
			Ordinals: &appsv1.StatefulSetOrdinals{Start: 1}}},
	} {
		if _, err := kc.AppsV1().StatefulSets("fn").Create(ctx, &s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	lws := func() *corev1.Pod {
		p := stsRank("l", "0", "2", "u", false)
		p.Labels = map[string]string{lwsGroupKeyLabel: "g"}
		return &p
	}
	grove := func() *corev1.Pod {
		p := stsRank("g", "0", "2", "u", false)
		p.Labels = map[string]string{webhook.GroveScalingGroupLabel: "sg", webhook.GroveScalingGroupReplicaLabel: "0"}
		return &p
	}
	owned := func(kind, name string) *corev1.Pod {
		p := stsRank("p", "0", "1", "u", false)
		p.OwnerReferences[0].Kind, p.OwnerReferences[0].Name = kind, name
		return &p
	}
	for _, tc := range []struct {
		pod  *corev1.Pod
		want bool
	}{
		{owned("StatefulSet", "ordered"), false},
		{owned("StatefulSet", "parallel"), true},
		{owned("StatefulSet", "single"), true},
		{owned("StatefulSet", "from1"), false},
		{owned("StatefulSet", "missing"), false},
		{owned("ReplicaSet", "rs"), true},
		{lws(), false},
		{grove(), false},
	} {
		if ok, why := c.a.captureHoldAdmits(ctx, tc.pod); ok != tc.want {
			t.Errorf("%s/%s: admits %v (%s), want %v", tc.pod.OwnerReferences[0].Kind, tc.pod.OwnerReferences[0].Name, ok, why, tc.want)
		}
	}
}

// A replacement rank of an instance restored from a group capture finds
// its leader by the group restore's rank, and is released once that
// leader serves.
func TestConsiderCaptureHold_RestoredLeader(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	leader := stsRank("kimi-0", "", "", "sts-a", true)
	// Its group's size counts the capture's companions too.
	leader.Annotations = map[string]string{webhook.CRIUGroupOrdinalAnnotation: "0", webhook.CRIUGroupSizeAnnotation: "3"}
	rank := held(stsRank("kimi-1", "1", "2", "sts-a", false), time.Now())
	createPods(t, kc, &leader, rank)
	c.considerCaptureHold(ctx, rank, true)
	if !settled(t, kc, rank) {
		t.Error("a replacement rank of a restored, serving instance stays held")
	}
}

// A leader no capture will take is decided before it is warm, so a
// leader that never becomes warm holds no other rank.
func TestConsiderGroupCapture_DecidedBeforeWarm(t *testing.T) {
	c, _ := trustFixture(t)
	ctx := context.Background()
	notOptedIn := stsRank("p-0", "0", "2", "sts-a", false)
	if c.considerGroupCapture(ctx, &notOptedIn) {
		t.Error("a multi-pod leader not opted into capture is undecided")
	}
	single := stsRank("s", "0", "1", "rs-1", false)
	if !c.considerGroupCapture(ctx, &single) {
		t.Error("a pod that may be captured once warm is decided before it is warm")
	}
}

// An attempt that does not start the capture drops its capturing mark:
// the member stays held, by the limit since it became warm.
func TestUnholdForCapture(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	h := held(stsRank("h", "0", "1", "rs-1", false), time.Now())
	createPods(t, kc, h)
	log := c.a.log.WithField("t", "t")
	if serves, err := c.holdForCapture(ctx, []*corev1.Pod{h}, log); serves != "" || err != nil {
		t.Fatal(serves, err)
	}
	c.unholdForCapture(ctx, []*corev1.Pod{h}, log)
	got, _ := kc.CoreV1().Pods("fn").Get(ctx, "h", metav1.GetOptions{})
	cond := capturehold.Get(got)
	if cond == nil || cond.Reason != capturehold.AwaitingReason || !capturehold.Held(got) {
		t.Errorf("after an attempt that did not start: %+v", cond)
	}
}

// A rank added by a scale-up finds the leader admitted at the old size.
func TestConsiderCaptureHold_LeaderOfAnOlderSize(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	leader := stsRank("p-0", "0", "1", "sts-a", true) // admitted when the set had one pod
	rank := held(stsRank("p-1", "1", "2", "sts-a", false), time.Now())
	createPods(t, kc, &leader, rank)
	c.considerCaptureHold(ctx, rank, true)
	if !settled(t, kc, rank) {
		t.Error("a scaled-up rank stays held behind a serving leader of the old size")
	}
}

// A leader admitted while the webhook was down carries no rank of nvsnap's
// and no gate: it can never be captured, so its ranks are released. It is
// found by its StatefulSet ordinal, and only within its own StatefulSet.
func TestConsiderCaptureHold_LeaderMissedTheWebhook(t *testing.T) {
	c, kc := trustFixture(t)
	ctx := context.Background()
	now := time.Now()
	bare := func(p corev1.Pod) *corev1.Pod {
		delete(p.Annotations, modelvolume.CacheOrdinalAnnotation)
		delete(p.Annotations, modelvolume.CacheGroupSizeAnnotation)
		return &p
	}
	leader := bare(stsRank("kimi-0", "0", "2", "sts-a", true))
	rank := held(stsRank("kimi-1", "1", "2", "sts-a", false), now)
	other := bare(stsRank("dyn-0", "0", "2", "sts-b", true)) // another StatefulSet's
	otherRank := held(stsRank("kimi-1b", "1", "2", "sts-c", false), now)
	createPods(t, kc, leader, rank, other, otherRank)
	c.considerCaptureHold(ctx, rank, true)
	if !settled(t, kc, rank) {
		t.Error("a rank whose leader missed the webhook stays held")
	}
	c.considerCaptureHold(ctx, otherRank, true)
	if settled(t, kc, otherRank) {
		t.Error("a rank was released by another StatefulSet's leader")
	}
}

func TestStatefulSetOrdinal(t *testing.T) {
	p := stsRank("kimi-3", "3", "4", "sts", false)
	if o, ok := statefulSetOrdinal(&p); !ok || o != 3 {
		t.Errorf("by name: %d %v", o, ok)
	}
	p.Labels = map[string]string{"apps.kubernetes.io/pod-index": "0"}
	if o, ok := statefulSetOrdinal(&p); !ok || o != 0 {
		t.Errorf("by label: %d %v", o, ok)
	}
	p.OwnerReferences[0].Kind = "ReplicaSet"
	if _, ok := statefulSetOrdinal(&p); ok {
		t.Error("a ReplicaSet's pod has a StatefulSet ordinal")
	}
}
