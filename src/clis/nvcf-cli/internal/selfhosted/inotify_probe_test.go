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

package selfhosted

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// fakeNode is a minimal corev1.Node for the fake clientset's tracker.
func fakeNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestProbeAllNodes_EmptyCluster(t *testing.T) {
	client := fake.NewSimpleClientset()
	results, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err)
	assert.Empty(t, results, "no nodes → no per-node results")
}

func TestProbeAllNodes_ListNodesError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "nodes", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden: nodes")
	})
	_, err := probeAllNodes(context.Background(), client, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing nodes")
	assert.Contains(t, err.Error(), "forbidden")
}

func TestProbeAllNodes_PodCreateErrorSurfacesPerNode(t *testing.T) {
	unschedulableNode := fakeNode("node-c")
	unschedulableNode.Spec.Unschedulable = true
	client := fake.NewSimpleClientset(fakeNode("node-a"), fakeNode("node-b"), unschedulableNode)
	client.PrependReactor("create", "pods", func(_ ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden: pod create denied")
	})
	results, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err, "list succeeded so the overall probe should not return an error")
	require.Len(t, results, 2)
	for _, r := range results {
		require.Error(t, r.Err, "per-node create failure must surface as NodeInotifyLimits.Err")
		assert.Contains(t, r.Err.Error(), "create probe pod")
		assert.Contains(t, r.Err.Error(), r.NodeName)
		assert.False(t, r.OutOfBudget, "the node's own failure, not the budget's")
	}
}

// The probe is bounded as a whole. Nodes are probed probeConcurrency at a
// time, so on a cluster whose probe pods never finish each wave took
// perNodePodTimeout, and the probe cut short the validator that runs after it.
// Nodes the budget did not reach are reported as not probed, and every pod
// created is removed.
func TestProbeAllNodes_StallingPodsAreBoundedByTheProbeBudget(t *testing.T) {
	prev := inotifyProbeBudget
	inotifyProbeBudget = 500 * time.Millisecond
	t.Cleanup(func() { inotifyProbeBudget = prev })
	const extra = 3
	var nodes []runtime.Object
	for i := 0; i < probeConcurrency+extra; i++ {
		nodes = append(nodes, fakeNode(fmt.Sprintf("node-%02d", i)))
	}
	client := fake.NewSimpleClientset(nodes...)
	var created atomic.Int32
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		// The fake clientset does not honour generateName. The pods never
		// reach a terminal phase, like pods that cannot pull or schedule.
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Name = fmt.Sprintf("%s%d", pod.GenerateName, created.Add(1))
		return false, nil, nil
	})

	start := time.Now()
	results, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second, "the probe outlived its budget")
	require.Len(t, results, probeConcurrency+extra)
	notProbed := 0
	for _, r := range results {
		require.Error(t, r.Err, r.NodeName)
		assert.True(t, r.OutOfBudget, "%s: stopped in flight or never reached, a later run may probe it", r.NodeName)
		if strings.Contains(r.Err.Error(), "not probed: the inotify probe's 500ms budget ran out") {
			notProbed++
		}
	}
	assert.Equal(t, extra, notProbed, "the nodes past the first wave are never reached")
	pods, err := client.CoreV1().Pods(inotifyProbeNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, pods.Items, "every probe pod created is removed")
	assert.EqualValues(t, probeConcurrency, created.Load())
}

func TestBuildInotifyProbePodShape(t *testing.T) {
	pod := buildInotifyProbePod("node-x", "run1", "")

	assert.Equal(t, inotifyProbeNamespace, pod.Namespace)
	assert.Equal(t, "nvcf-inotify-probe-", pod.GenerateName, "GenerateName lets the API server assign a unique suffix")
	// The run and role labels let a run reclaim exactly its own pods, and the
	// managed-by value is this CLI's, not the one released CLIs delete by.
	assert.Equal(t, "run1", pod.Labels[clusterValidatorRunLabel])
	assert.Equal(t, clusterValidatorComputePlaneRole, pod.Labels[clusterValidatorRoleLabel])
	assert.Equal(t, clusterValidatorManagedBy, pod.Labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, inotifyProbeAppLabel, pod.Labels["app.kubernetes.io/name"])
	assert.Empty(t, pod.Name, "explicit Name conflicts with GenerateName")
	assert.Equal(t, "node-x", pod.Spec.NodeName, "must pin to the target node")
	assert.False(t, pod.Spec.HostPID,
		"hostPID is not needed; inotify sysctls are world-readable via the /host hostPath mount, and dropping hostPID reduces the privilege footprint for PSA baseline/restricted clusters")
	assert.Equal(t, corev1.RestartPolicyNever, pod.Spec.RestartPolicy)
	require.Len(t, pod.Spec.Tolerations, 1)
	assert.Equal(t, corev1.TolerationOpExists, pod.Spec.Tolerations[0].Operator,
		"tolerate-all so tainted nodes (control-plane, GPU) are probed")

	require.Len(t, pod.Spec.Containers, 1)
	c := pod.Spec.Containers[0]
	assert.Equal(t, inotifyProbeContainer, c.Name)
	assert.Equal(t, inotifyProbeImage, c.Image)
	assert.Equal(t, []string{"sh", "-c", inotifyProbeShellCmd}, c.Command)
	require.Len(t, c.VolumeMounts, 1)
	assert.Equal(t, "/host", c.VolumeMounts[0].MountPath)
	assert.True(t, c.VolumeMounts[0].ReadOnly, "no writes to host fs from a probe pod")

	require.Len(t, pod.Spec.Volumes, 1)
	require.NotNil(t, pod.Spec.Volumes[0].HostPath)
	assert.Equal(t, "/", pod.Spec.Volumes[0].HostPath.Path)

	// Pinned with NodeName, the pod is admitted against the node's free
	// capacity: a request has it rejected on a fully requested node. A limit
	// alone would become the request.
	assert.Empty(t, c.Resources.Requests)
	assert.Empty(t, c.Resources.Limits)
	require.NotNil(t, pod.Spec.SecurityContext)
	require.NotNil(t, pod.Spec.SecurityContext.RunAsUser)
	assert.NotZero(t, *pod.Spec.SecurityContext.RunAsUser, "the sysctls are world-readable, so root is not needed")
	require.NotNil(t, c.SecurityContext)
	assert.True(t, *c.SecurityContext.RunAsNonRoot)
	assert.True(t, *c.SecurityContext.ReadOnlyRootFilesystem)
	assert.False(t, *c.SecurityContext.AllowPrivilegeEscalation)
	assert.Equal(t, []corev1.Capability{"ALL"}, c.SecurityContext.Capabilities.Drop)
}

// The configured probe image replaces the Docker Hub default, so a cluster
// that pulls from a mirror can run the probe.
func TestBuildInotifyProbePod_UsesTheConfiguredImage(t *testing.T) {
	pod := buildInotifyProbePod("node-x", "run1", "mirror.example/busybox:1.36")
	assert.Equal(t, "mirror.example/busybox:1.36", pod.Spec.Containers[0].Image)
}

// probePodsClient serves one node whose probe pods finish at once with the
// given output. Creates get a name, a UID and a creationTimestamp on the
// apiserver's clock, which here runs serverSkew from the local one.
func probePodsClient(t *testing.T, serverSkew time.Duration, objs ...runtime.Object) *fake.Clientset {
	t.Helper()
	client := fake.NewSimpleClientset(append([]runtime.Object{fakeNode("node-a")}, objs...)...)
	var n atomic.Int32
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Name = fmt.Sprintf("%s%d", pod.GenerateName, n.Add(1))
		pod.UID = types.UID("uid-" + pod.Name)
		pod.CreationTimestamp = metav1.NewTime(time.Now().Add(serverSkew))
		pod.Status.Phase = corev1.PodSucceeded
		return false, nil, nil
	})
	return client
}

// probePod is a probe pod of another run, created age ago on a clock skewed
// by serverSkew.
func probePod(name, runID string, serverSkew, age time.Duration) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: inotifyProbeNamespace, Labels: inotifyProbeLabels(runID),
		CreationTimestamp: metav1.NewTime(time.Now().Add(serverSkew - age)),
	}}
}

// A probe deletes exactly its own pods, by UID, and leaves an overlapping
// probe's pods alone. Another run's pod past the orphan TTL on the
// apiserver's clock is reclaimed, whatever the local clock says: here the
// apiserver runs ten hours behind, so by the local clock every pod is stale.
func TestProbeAllNodes_ReclaimsOnlyItsOwnAndStalePods(t *testing.T) {
	const skew = -10 * time.Hour
	live := probePod("nvcf-inotify-probe-live", "otherrun", skew, orphanValidatorRBACTTL-time.Minute)
	stale := probePod("nvcf-inotify-probe-stale", "deadrun", skew, orphanValidatorRBACTTL+time.Minute)
	// Probe labels on a name the probe never generates.
	foreign := probePod("operator-pod", "deadrun", skew, 2*orphanValidatorRBACTTL)
	client := probePodsClient(t, skew, live, stale, foreign)

	_, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err)

	pods, err := client.CoreV1().Pods(inotifyProbeNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	var left []string
	for _, p := range pods.Items {
		left = append(left, p.Name)
	}
	assert.ElementsMatch(t, []string{live.Name, foreign.Name}, left,
		"the probe's own pod and the stale orphan go; a live probe's pod and a foreign one stay")
	for _, a := range client.Actions() {
		d, ok := a.(ktesting.DeleteActionImpl)
		if !ok || d.GetResource().Resource != "pods" {
			continue
		}
		require.NotNil(t, d.DeleteOptions.Preconditions, d.Name)
		require.NotNil(t, d.DeleteOptions.Preconditions.UID, d.Name)
		assert.Nil(t, d.DeleteOptions.Preconditions.ResourceVersion, d.Name)
	}
}

// A Create the budget or an interrupt cut off may still have been applied.
// No per-node delete runs for it, so the probe reclaims it by its run label.
func TestProbeAllNodes_ReclaimsAPodWhoseCreateErrored(t *testing.T) {
	client := fake.NewSimpleClientset(fakeNode("node-a"))
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		pod.Name = pod.GenerateName + "applied"
		require.NoError(t, client.Tracker().Add(pod))
		return true, nil, context.DeadlineExceeded
	})

	results, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Err)
	pods, err := client.CoreV1().Pods(inotifyProbeNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, pods.Items, "the applied pod mounts the host's root and must not be left behind")
}

// The reclaim after a probe ends within probePodCleanupTimeout of the probe's
// deadline, the room the probe share leaves for it, however slow the
// apiserver. Bounded by the longer sweep timeout, it ate into the time sized
// for the validator.
func TestProbeRun_ReclaimEndsWithinTheCleanupRoom(t *testing.T) {
	prevPod, prevSweep := probePodCleanupTimeout, validatorCleanupTimeout
	probePodCleanupTimeout, validatorCleanupTimeout = 300*time.Millisecond, time.Minute
	t.Cleanup(func() { probePodCleanupTimeout, validatorCleanupTimeout = prevPod, prevSweep })
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, QPS: -1})
	require.NoError(t, err)
	run := &probeRun{id: "r1"}

	start := time.Now()
	run.reclaim(client, start.Add(time.Hour))
	assert.Less(t, time.Since(start), 5*time.Second, "a probe that ended early still bounds its reclaim")

	start = time.Now()
	run.reclaim(client, start.Add(-probePodCleanupTimeout))
	assert.Less(t, time.Since(start), probePodCleanupTimeout,
		"the per-node deletes used the room after the deadline, so the reclaim gets none")
}

// quotaClient serves one node in a namespace whose ResourceQuota on cpu and
// memory rejects a pod that does not state them, as the admission plugin
// words it. Admitted pods finish at once; every create is recorded.
func quotaClient(t *testing.T, retryErr error) (*fake.Clientset, *[]corev1.ResourceRequirements) {
	t.Helper()
	client := fake.NewSimpleClientset(fakeNode("node-a"))
	var creates []corev1.ResourceRequirements
	var n atomic.Int32
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		res := pod.Spec.Containers[0].Resources
		creates = append(creates, res)
		if len(res.Requests) == 0 && len(res.Limits) == 0 {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, pod.GenerateName,
				errors.New("failed quota: compute: must specify limits.cpu for: probe; limits.memory for: probe; "+
					"requests.cpu for: probe; requests.memory for: probe"))
		}
		if retryErr != nil {
			return true, nil, retryErr
		}
		pod.Name = fmt.Sprintf("%s%d", pod.GenerateName, n.Add(1))
		pod.Status.Phase = corev1.PodSucceeded
		return false, nil, nil
	})
	return client, &creates
}

// A namespace whose quota requires requests and limits rejects the probe pod,
// which states none. The pod is created once more with small, equal requests
// and limits, so the node is probed rather than reported as not probed.
func TestProbeAllNodes_StatesResourcesWhenAQuotaRequiresThem(t *testing.T) {
	client, creates := quotaClient(t, nil)
	results, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Len(t, *creates, 2, "one retry, with resources")
	retry := (*creates)[1]
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		req, lim := retry.Requests[name], retry.Limits[name]
		assert.False(t, req.IsZero(), name)
		assert.Zero(t, req.Cmp(lim), "%s: equal requests and limits", name)
	}
	if results[0].Err != nil {
		assert.NotContains(t, results[0].Err.Error(), "create probe pod", "the retry was admitted")
	}
	pods, err := client.CoreV1().Pods(inotifyProbeNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	assert.Empty(t, pods.Items, "the retried pod is removed like any other")
}

// The retry happens once, and only for a quota that requires resources: its
// own rejection is the node's error, and any other refusal is not retried.
func TestProbeAllNodes_QuotaRetryIsOnceAndOnlyForMissingResources(t *testing.T) {
	client, creates := quotaClient(t, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p",
		errors.New("exceeded quota: compute, requested: requests.cpu=50m, used: requests.cpu=4, limited: requests.cpu=4")))
	results, err := probeAllNodes(context.Background(), client, "", nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Len(t, *creates, 2)
	require.Error(t, results[0].Err)
	assert.Contains(t, results[0].Err.Error(), "exceeded quota")

	denied := fake.NewSimpleClientset(fakeNode("node-a"))
	calls := 0
	denied.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p",
			errors.New(`violates PodSecurity "restricted:latest": hostPath volumes`))
	})
	results, err = probeAllNodes(context.Background(), denied, "", nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, 1, calls, "a refusal for any other reason is not retried")
	assert.Contains(t, results[0].Err.Error(), "PodSecurity")
}

// probeCluster is a fake cluster of nodes node-00, node-01, and so on, for a
// prober called several times, as the polls of one --wait call it. A pod that
// finishes logs logs(node, call), served after delay; one that does not stays
// pending, like a pod that cannot pull its image or a node that is NotReady.
type probeCluster struct {
	client kubernetes.Interface
	mu     sync.Mutex
	call   int
	nodeOf map[string]string // probe pod -> node
	// created is the node of each probe pod created, per call.
	created [][]string
}

func newProbeCluster(
	t *testing.T, nodes int, finishes bool, delay time.Duration, logs func(node string, call int) string,
) *probeCluster {
	t.Helper()
	pc := &probeCluster{nodeOf: map[string]string{}}
	var objs []runtime.Object
	for i := 0; i < nodes; i++ {
		n := fakeNode(fmt.Sprintf("node-%02d", i))
		n.UID = types.UID("uid-" + n.Name)
		objs = append(objs, n)
	}
	client := fake.NewSimpleClientset(objs...)
	var seq atomic.Int32
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		pod.Name = fmt.Sprintf("%s%d", pod.GenerateName, seq.Add(1))
		if finishes {
			pod.Status.Phase = corev1.PodSucceeded
		}
		pc.mu.Lock()
		pc.nodeOf[pod.Name] = pod.Spec.NodeName
		pc.created[len(pc.created)-1] = append(pc.created[len(pc.created)-1], pod.Spec.NodeName)
		pc.mu.Unlock()
		return false, nil, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		pc.mu.Lock()
		node, call := pc.nodeOf[path.Base(path.Dir(r.URL.Path))], pc.call
		pc.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, logs(node, call))
	}))
	t.Cleanup(srv.Close)
	logsFrom, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, QPS: -1})
	require.NoError(t, err)
	pc.client = logsClient{Clientset: client, logs: logsFrom}
	return pc
}

// probe is one call of prober, the prober's next poll.
func (pc *probeCluster) probe(t *testing.T, prober NodeInotifyProber) []NodeInotifyLimits {
	t.Helper()
	pc.mu.Lock()
	pc.call++
	pc.created = append(pc.created, nil)
	pc.mu.Unlock()
	limits, err := prober(context.Background(), "test")
	require.NoError(t, err)
	return limits
}

func (pc *probeCluster) prober() NodeInotifyProber {
	return newInotifyProber("", func(string) (kubernetes.Interface, error) { return pc.client, nil })
}

func withinLimitsLog(string, int) string { return "8192\n524288\n" }

// shortenInotifyProbe sets the probe's budget and each node's own bound.
func shortenInotifyProbe(t *testing.T, budget, perNode time.Duration) {
	t.Helper()
	prevBudget, prevNode := inotifyProbeBudget, perNodePodTimeout
	inotifyProbeBudget, perNodePodTimeout = budget, perNode
	t.Cleanup(func() { inotifyProbeBudget, perNodePodTimeout = prevBudget, prevNode })
}

// Nodes whose probe pods cannot finish hit their own bound, a failure of the
// node a later poll cannot clear. On a cluster with more such nodes than one
// wave, the budget ran out on the next wave the same way on every poll, so
// --wait never ended. A node the budget stopped is probed first on the next
// poll, where it hits its own bound too, and that poll is not transient.
func TestInotifyProber_NodesThatCannotFinishStopKeepingTheWaitGoing(t *testing.T) {
	shortenInotifyProbe(t, 600*time.Millisecond, 400*time.Millisecond)
	const extra = 4
	pc := newProbeCluster(t, probeConcurrency+extra, false, 0, withinLimitsLog)
	prober := pc.prober()

	first := pc.probe(t, prober)
	require.Len(t, first, probeConcurrency+extra)
	var cut []string
	for _, l := range first {
		require.Error(t, l.Err, l.NodeName)
		if l.OutOfBudget {
			cut = append(cut, l.NodeName)
		}
	}
	assert.Len(t, cut, extra, "only the nodes the budget stopped may be reached later: %v", first)

	second := pc.probe(t, prober)
	require.Len(t, second, probeConcurrency+extra)
	for _, l := range second {
		require.Error(t, l.Err, l.NodeName)
		assert.False(t, l.OutOfBudget, "%s: every node has now failed on its own", l.NodeName)
	}
	require.GreaterOrEqual(t, len(pc.created[1]), probeConcurrency)
	assert.Subset(t, pc.created[1][:probeConcurrency], cut, "the nodes the budget stopped go in the first wave")

	row := nodeInotifyCheck(func(context.Context, string) ([]NodeInotifyLimits, error) { return second, nil }, "").
		Run(context.Background())
	assert.Equal(t, SeverityWarning, row.Severity)
	assert.False(t, row.Transient, "--wait has nothing left to wait for")
}

// A cluster with more nodes than one poll's budget reaches is measured over
// several polls: a node found within the limits is not probed again, and the
// nodes no poll has a result for go first. Before, every poll started over in
// the same order, and on a large cluster never reached the tail.
func TestInotifyProber_ReachesEveryNodeOfALargeClusterOverPolls(t *testing.T) {
	shortenInotifyProbe(t, 500*time.Millisecond, 90*time.Second)
	const nodes = 120
	pc := newProbeCluster(t, nodes, true, 40*time.Millisecond, withinLimitsLog)
	prober := pc.prober()

	within := map[string]bool{}
	for poll := 0; poll < 8 && len(within) < nodes; poll++ {
		limits := pc.probe(t, prober)
		require.Len(t, limits, nodes)
		for _, node := range pc.created[poll] {
			assert.False(t, within[node], "%s was within the limits on an earlier poll and was probed again", node)
		}
		for _, l := range limits {
			if l.Err == nil && l.withinLimits() {
				within[l.NodeName] = true
			} else {
				assert.True(t, l.OutOfBudget, "%s: %v", l.NodeName, l.Err)
			}
		}
	}
	assert.Len(t, within, nodes, "every node is measured within a few polls")
	assert.Greater(t, len(pc.created), 1, "the cluster is larger than one poll reaches")
}

// A node below the limits is probed again on the next poll, so limits raised
// while --wait polls are seen; its earlier reading is kept only while the
// budget stops the new probe.
func TestInotifyProber_ProbesANodeBelowTheLimitsAgain(t *testing.T) {
	pc := newProbeCluster(t, 2, true, 0, func(node string, call int) string {
		if node == "node-01" && call == 1 {
			return "128\n524288\n"
		}
		return "8192\n524288\n"
	})
	prober := pc.prober()

	first := pc.probe(t, prober)
	require.Len(t, first, 2)
	second := pc.probe(t, prober)
	require.Len(t, second, 2)
	assert.Equal(t, []string{"node-01"}, pc.created[1], "only the node below the limits is probed again")
	for _, l := range second {
		assert.True(t, l.withinLimits(), "%s: %+v", l.NodeName, l)
	}
}

// How each way a probe ends carries over to the next poll: what it reports
// now, and where the node goes in the next poll's order.
func TestInotifyProbeMemory_CarriesEachOutcomeToTheNextPoll(t *testing.T) {
	node := func(name string) corev1.Node {
		return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)}}
	}
	cut := errors.New("waiting for probe pod: context deadline exceeded")
	notReached := errors.New("not probed")
	pulled := errors.New("ErrImagePull")
	within := NodeInotifyLimits{MaxUserInstances: 8192, MaxUserWatches: 524288}
	below := NodeInotifyLimits{MaxUserInstances: 128, MaxUserWatches: 524288}
	nodes := []corev1.Node{node("ok"), node("low"), node("own"), node("cut"), node("unreached"), node("new")}

	mem := &inotifyProbeMemory{}
	got := map[string]NodeInotifyLimits{}
	settle := func(i int, res NodeInotifyLimits, stop probeStop) {
		res.NodeName = nodes[i].Name
		got[nodes[i].Name] = mem.settle(&nodes[i], res, stop)
	}
	settle(0, within, probeFinished)
	settle(1, below, probeFinished)
	settle(2, NodeInotifyLimits{Err: pulled}, probeFinished)
	settle(3, NodeInotifyLimits{Err: cut}, probeStoppedPartWay)
	settle(4, NodeInotifyLimits{Err: notReached}, probeNotStarted)
	for name, out := range map[string]bool{"ok": false, "low": false, "own": false, "cut": true, "unreached": true} {
		assert.Equal(t, out, got[name].OutOfBudget, name)
	}

	results := make([]NodeInotifyLimits, len(nodes))
	order := mem.plan(nodes, results)
	var names []string
	for _, i := range order {
		names = append(names, nodes[i].Name)
	}
	assert.Equal(t, []string{"cut", "unreached", "new", "low", "own"}, names,
		"stopped part way first, then nodes never reached, then nodes a new probe may change")
	assert.True(t, results[0].withinLimits(), "a node within the limits is kept, not probed again")

	// Next poll: the budget stops every probe it makes.
	settle(1, NodeInotifyLimits{Err: notReached}, probeNotStarted)
	settle(2, NodeInotifyLimits{Err: cut}, probeStoppedPartWay)
	settle(3, NodeInotifyLimits{Err: cut}, probeStoppedPartWay)
	settle(4, NodeInotifyLimits{Err: notReached}, probeNotStarted)
	assert.Equal(t, below.MaxUserInstances, got["low"].MaxUserInstances, "its earlier reading stands")
	assert.Equal(t, pulled, got["own"].Err, "its earlier failure stands")
	assert.False(t, got["low"].OutOfBudget)
	assert.False(t, got["own"].OutOfBudget)
	assert.False(t, got["cut"].OutOfBudget, "stopped part way again although it went first")
	assert.True(t, got["unreached"].OutOfBudget, "still never reached")
}
