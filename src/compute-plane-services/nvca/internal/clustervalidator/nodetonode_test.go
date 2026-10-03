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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

// readyNodes returns n Ready nodes named node-1..node-n.
func readyNodes(n int) []*corev1.Node {
	nodes := make([]*corev1.Node, n)
	for i := range nodes {
		nodes[i] = makeNode(fmt.Sprintf("node-%d", i+1), true, 0)
	}
	return nodes
}

// runningServers returns a Running probe pod with an IP on each of nodes;
// node-k's pod is s-k at 10.0.0.k.
func runningServers(nodes ...string) []corev1.Pod {
	pods := make([]corev1.Pod, len(nodes))
	for i, node := range nodes {
		k := strings.TrimPrefix(node, "node-")
		pods[i] = runningProbePod("s-"+k, node, "10.0.0."+k)
	}
	return pods
}

func ipOf(node string) string { return "10.0.0." + strings.TrimPrefix(node, "node-") }

// others returns the nodes other than the checker's, sorted.
func (f *n2nFixture) others(nodes ...string) []string {
	var out []string
	for _, n := range nodes {
		if n != f.checkerNode {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (f *n2nFixture) removeNode(_ *testing.T, name string) {
	_ = f.client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("nodes"), "", name)
}

func (f *n2nFixture) markNotReady(t *testing.T, name string) {
	t.Helper()
	gvr := corev1.SchemeGroupVersion.WithResource("nodes")
	obj, err := f.client.Tracker().Get(gvr, "", name)
	require.NoError(t, err)
	n := obj.(*corev1.Node).DeepCopy()
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}}
	require.NoError(t, f.client.Tracker().Update(gvr, n, ""))
}

// runN2N runs the check against f and returns the state.
func runN2N(f *n2nFixture) *ValidationState {
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)
	return state
}

// A run must leave none of its objects behind, whatever its outcome. The
// fixture stores everything created, so a cleanup step that is skipped, aimed
// at the wrong name, or ordered after an early return shows up here.
func TestCheckNodeToNode_LeavesNothingBehind(t *testing.T) {
	two := readyNodes(2)
	stuck := probePod("node-2", "Pending", "", "ContainerCreating")
	stuck.Name = "s-2"
	sandboxFailure := []corev1.Event{podEvent("s-2", "FailedCreatePodSandBox", 0)}
	cases := []struct {
		name    string
		pods    []corev1.Pod
		events  []corev1.Event
		checker func() (*corev1.Pod, error)
		setup   func(f *n2nFixture)
		row     *bool
	}{
		{name: "verified", pods: runningServers("node-1", "node-2"), checker: checkerExit(0), row: ptrBool(true)},
		{name: "unreachable", pods: runningServers("node-1", "node-2"),
			checker: checkerExit(nodeToNodeUnreachableExit), row: ptrBool(false)},
		{name: "sandbox fault", pods: append(runningServers("node-1"), stuck), events: sandboxFailure,
			checker: checkerExit(0), row: ptrBool(false)},
		{name: "daemonset rejected", checker: checkerExit(0), setup: func(f *n2nFixture) {
			f.client.PrependReactor("create", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewBadRequest("image not allowed")
			})
		}},
		{name: "probe pods unobserved", pods: runningServers("node-1", "node-2"), checker: checkerExit(0),
			setup: func(f *n2nFixture) {
				failing := true
				f.client.PrependReactor("delete", "daemonsets", func(ktesting.Action) (bool, runtime.Object, error) {
					failing = false
					return false, nil, nil
				})
				f.client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					if failing {
						return true, nil, apierrors.NewTooManyRequestsError("slow down")
					}
					return false, nil, nil
				})
			}},
		{name: "checker unobserved", pods: runningServers("node-1", "node-2"),
			checker: func() (*corev1.Pod, error) {
				return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", fmt.Errorf("denied"))
			}},
		{name: "checker refused", pods: runningServers("node-1", "node-2"),
			checker: func() (*corev1.Pod, error) { return refusedChecker(), nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newN2NFixture(t, two, tc.pods, tc.events, tc.checker)
			if tc.setup != nil {
				tc.setup(f)
			}
			state := runN2N(f)
			assert.Equal(t, tc.row, state.NodeToNodeOK, "warnings: %v", state.Warnings)
			assert.Empty(t, f.leftovers(t))
		})
	}
}

func ptrBool(b bool) *bool { return &b }

// bufferLog is a logger whose output, as the CLI prints it, lands in buf.
func bufferLog() (*logrus.Entry, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	l.SetFormatter(&CLIFormatter{})
	return logrus.NewEntry(l), buf
}

// The cleanup removes this run's objects only: a concurrent run's namespace,
// DaemonSet and pods carry the same labels with another instance.
func TestCheckNodeToNode_CleansUpOnlyItsOwnRun(t *testing.T) {
	f := newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil, checkerExit(0))
	other := nodeToNodeNSPrefix + "other1"
	labels := n2nLabels(n2nServerComponent, "other1")
	ctx := context.Background()
	_, err := f.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: other, Labels: n2nLabels(n2nNamespaceComponent, "other1"),
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, f.client.Tracker().Create(appsv1.SchemeGroupVersion.WithResource("daemonsets"),
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: nodeToNodeDSName + "-other1", Namespace: other, Labels: labels,
		}}, other))
	require.NoError(t, f.client.Tracker().Create(corev1.SchemeGroupVersion.WithResource("pods"),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "s-other", Namespace: other, Labels: labels}}, other))

	state := runN2N(f)
	require.NotNil(t, state.NodeToNodeOK)

	assert.ElementsMatch(t, []string{
		"daemonset " + other + "/" + nodeToNodeDSName + "-other1",
		"pod " + other + "/s-other",
		"namespace " + other,
	}, f.leftovers(t), "only the concurrent run's objects remain")
}

// The cleanup runs on a context of its own: a run cancelled mid-probe, as on
// SIGTERM, still deletes its DaemonSet and namespace. A fake clientset ignores
// contexts, so this runs against an HTTP server standing in for the apiserver.
func TestCheckNodeToNode_CleanupSurvivesACancelledRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes":
			list := corev1.NodeList{Items: []corev1.Node{*makeNode("node-1", true, 0), *makeNode("node-2", true, 0)}}
			list.APIVersion, list.Kind = "v1", "NodeList"
			_ = json.NewEncoder(w).Encode(list)
		case r.Method == http.MethodPost:
			if strings.HasSuffix(r.URL.Path, "/daemonsets") {
				cancel() // the run is interrupted once the probe is in place
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(body)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods"):
			_, _ = w.Write([]byte(`{"kind":"PodList","apiVersion":"v1","items":[]}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","code":404}`))
		}
	}))
	defer srv.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{
		Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	})
	require.NoError(t, err)

	state := &ValidationState{Log: testLog()}
	finishesWithin(t, 30*time.Second, func() { checkNodeToNode(ctx, client, state, enforcementDefaultImg) })

	assert.Nil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
	mu.Lock()
	defer mu.Unlock()
	var deletes []string
	for _, s := range seen {
		if strings.HasPrefix(s, http.MethodDelete) {
			deletes = append(deletes, s)
		}
	}
	require.Len(t, deletes, 2, "requests: %v", seen)
	assert.Regexp(t, `^DELETE /apis/apps/v1/namespaces/nvcf-n2n-validation-\w+/daemonsets/nvcf-n2n-server-\w+$`,
		deletes[0])
	assert.Regexp(t, `^DELETE /api/v1/namespaces/nvcf-n2n-validation-\w+$`, deletes[1])
}

// Every run reclaims what a killed probe left, whatever its role: the probe
// runs only for the control-plane role, but either role's validator can run
// on a cluster.
func TestRun_SweepsProbeLeftoversForEveryRole(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour))
	for _, role := range []Role{RoleComputePlane, RoleControlPlane, ""} {
		t.Run(string(role), func(t *testing.T) {
			labels := map[string]string{
				"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
				"app.kubernetes.io/component":  "n2n-probe",
			}
			client := fake.NewSimpleClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
					Name: nodeToNodeNSPrefix + "stale1", Labels: labels, CreationTimestamp: old,
				}},
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
					Name: "labelled-not-ours", Labels: labels, CreationTimestamp: old,
				}},
				&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
					Name: nodeToNodeDSName + "-legacy", Namespace: "default", CreationTimestamp: old,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "nvcf-cluster-validator",
						"app.kubernetes.io/component":  "n2n-server",
					},
				}},
			)
			_ = Run(context.Background(), client, nil, "", "", "", false, role)

			ctx := context.Background()
			_, err := client.CoreV1().Namespaces().Get(ctx, nodeToNodeNSPrefix+"stale1", metav1.GetOptions{})
			assert.True(t, apierrors.IsNotFound(err), "the stale probe namespace is swept")
			_, err = client.AppsV1().DaemonSets("default").Get(ctx, nodeToNodeDSName+"-legacy", metav1.GetOptions{})
			assert.True(t, apierrors.IsNotFound(err), "the legacy probe DaemonSet is swept")
			_, err = client.CoreV1().Namespaces().Get(ctx, "labelled-not-ours", metav1.GetOptions{})
			assert.NoError(t, err, "a labelled namespace without the probe prefix survives")
		})
	}
}

// A node removed, gone NotReady or fenced while the checker dialled it
// explains its own unreachable server pod. It is a coverage gap, and the
// nodes still up that were reached verify the overlay.
func TestCheckNodeToNode_NodeLostDuringTheProbeIsAGap(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	lose := map[string]func(t *testing.T, f *n2nFixture, node string){
		"removed":  func(t *testing.T, f *n2nFixture, node string) { f.removeNode(t, node) },
		"NotReady": func(t *testing.T, f *n2nFixture, node string) { f.markNotReady(t, node) },
	}
	for want, mutate := range lose {
		t.Run(want, func(t *testing.T) {
			var f *n2nFixture
			var lost string
			f = newN2NFixture(t, readyNodes(3), runningServers(nodes...), nil, func() (*corev1.Pod, error) {
				lost = f.others(nodes...)[1]
				mutate(t, f, lost)
				return checkerReported(nodeToNodeUnreachableExit,
					ipOf(lost)+": nc: "+ipOf(lost)+" (19999): Connection timed out", "unreachable: "+ipOf(lost)), nil
			})
			state := runN2N(f)

			require.NotNil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
			assert.True(t, *state.NodeToNodeOK, "the node still up was reached")
			assert.Contains(t, strings.Join(state.Warnings, "; "), lost+": "+want+" during the probe")
			assert.Empty(t, state.Recommendations, "no firewall advice for a node that left")
		})
	}
}

// Only the nodes still up that the checker could not reach fail the row.
func TestCheckNodeToNode_UnreachableNodeStillUpFails(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	var f *n2nFixture
	var up, lost string
	f = newN2NFixture(t, readyNodes(3), runningServers(nodes...), nil, func() (*corev1.Pod, error) {
		o := f.others(nodes...)
		up, lost = o[0], o[1]
		f.removeNode(t, lost)
		return checkerReported(nodeToNodeUnreachableExit, "unreachable: "+ipOf(up)+" "+ipOf(lost)), nil
	})
	log, buf := bufferLog()
	state := &ValidationState{Log: log}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)

	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK)
	assert.Contains(t, buf.String(), "could not reach the server pods on "+up+" (port")
	assert.Contains(t, strings.Join(state.Warnings, "; "), lost+": removed during the probe")
	require.Len(t, state.Recommendations, 1)
	assert.Contains(t, state.Recommendations[0], "Calico GlobalNetworkPolicy",
		"a cluster-wide policy is the one the probe's own NetworkPolicy cannot override")
}

// The checker's own node leaving makes every dial suspect.
func TestCheckNodeToNode_CheckerNodeLostIsUnknown(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	var f *n2nFixture
	f = newN2NFixture(t, readyNodes(3), runningServers(nodes...), nil, func() (*corev1.Pod, error) {
		f.markNotReady(t, f.checkerNode)
		o := f.others(nodes...)
		return checkerReported(nodeToNodeUnreachableExit, "unreachable: "+ipOf(o[0])+" "+ipOf(o[1])), nil
	})
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "the checker's node "+f.checkerNode+" was NotReady")
}

// Without the output naming the unreachable pods, a node lost during the probe
// cannot be told from a broken path to the others.
func TestCheckNodeToNode_UnnamedFailureWithALostNodeIsUnknown(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	var f *n2nFixture
	f = newN2NFixture(t, readyNodes(3), runningServers(nodes...), nil, func() (*corev1.Pod, error) {
		f.removeNode(t, f.others(nodes...)[1])
		return checkerPod(nodeToNodeUnreachableExit), nil
	})
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "did not say which")
}

// A server pod that refused the connection was not listening, which says
// nothing about the overlay: its node is a coverage gap. With no other node
// reached, nothing was verified.
func TestCheckNodeToNode_RefusedServerIsAGap(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3"}
	var f *n2nFixture
	var refused string
	f = newN2NFixture(t, readyNodes(3), runningServers(nodes...), nil, func() (*corev1.Pod, error) {
		refused = f.others(nodes...)[0]
		return checkerReported(nodeToNodeRefusedExit, "refused: "+ipOf(refused)), nil
	})
	state := runN2N(f)
	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), refused+": its server pod refused the connection")

	var g *n2nFixture
	g = newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil, func() (*corev1.Pod, error) {
		return checkerReported(nodeToNodeRefusedExit, "refused: "+ipOf(g.others("node-1", "node-2")[0])), nil
	})
	state = runN2N(g)
	assert.Nil(t, state.NodeToNodeOK, "no server pod answered, so nothing was verified")
}

// The checker stops after a few unreachable pods so a dead overlay fails
// fast. If all of those were on nodes that left, the rest were never dialled.
func TestCheckNodeToNode_StoppedWithOnlyLostNodesIsUnknown(t *testing.T) {
	nodes := []string{"node-1", "node-2", "node-3", "node-4", "node-5"}
	var f *n2nFixture
	f = newN2NFixture(t, readyNodes(5), runningServers(nodes...), nil, func() (*corev1.Pod, error) {
		lost := f.others(nodes...)[:nodeToNodeMaxUnreachable]
		ips := make([]string, len(lost))
		for i, n := range lost {
			f.removeNode(t, n)
			ips[i] = ipOf(n)
		}
		return checkerReported(nodeToNodeUnreachableExit,
			"stopped: after 3 unreachable", "unreachable: "+strings.Join(ips, " ")), nil
	})
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "the others were not dialled")
}

// A sandbox failure on a node that left during the probe is that node's, not
// the overlay's: the run goes on with the nodes still up.
func TestCheckNodeToNode_SandboxFaultOnALostNodeIsAGap(t *testing.T) {
	stuck := probePod("node-3", "Pending", "", "ContainerCreating")
	stuck.Name = "s-3"
	f := newN2NFixture(t, readyNodes(3), append(runningServers("node-1", "node-2"), stuck),
		[]corev1.Event{podEvent("s-3", "FailedCreatePodSandBox", 0)}, checkerExit(0))
	f.client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		f.removeNode(t, "node-3")
		return false, nil, nil
	})
	state := runN2N(f)

	require.NotNil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
	assert.True(t, *state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "node-3: removed during the probe")
}

// A sandbox failure carries its event message, which tells a CNI fault from a
// pause image the node could not pull.
func TestCheckNodeToNode_SandboxFaultShowsItsEvidence(t *testing.T) {
	stuck := probePod("node-2", "Pending", "", "ContainerCreating")
	stuck.Name = "s-2"
	event := podEvent("s-2", "FailedCreatePodSandBox", 0)
	event.Message = `failed to get sandbox image "registry.k8s.io/pause:3.9"`
	f := newN2NFixture(t, readyNodes(2), append(runningServers("node-1"), stuck), []corev1.Event{event},
		checkerExit(0))
	log, buf := bufferLog()
	state := &ValidationState{Log: log}
	checkNodeToNode(context.Background(), f.client, state, enforcementDefaultImg)

	require.NotNil(t, state.NodeToNodeOK)
	assert.False(t, *state.NodeToNodeOK)
	assert.Contains(t, buf.String(), `node-2: failed to get sandbox image "registry.k8s.io/pause:3.9"`)
	require.Len(t, state.Recommendations, 1)
	assert.Contains(t, state.Recommendations[0], "(CNI or container runtime)")
}

// An events list that cannot be read leaves a pod with no IP undecided, end to
// end: the nodes that did start are probed, and no CNI advice is given.
func TestCheckNodeToNode_EventsListErrorIsAGap(t *testing.T) {
	stuck := probePod("node-3", "Pending", "", "ContainerCreating")
	stuck.Name = "s-3"
	f := newN2NFixture(t, readyNodes(3), append(runningServers("node-1", "node-2"), stuck),
		[]corev1.Event{podEvent("s-3", "FailedCreatePodSandBox", 0)}, checkerExit(0))
	calls := 0
	f.client.PrependReactor("list", "events", func(ktesting.Action) (bool, runtime.Object, error) {
		calls++
		return true, nil, apierrors.NewInternalError(fmt.Errorf("etcd timeout"))
	})
	state := runN2N(f)

	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "node-3: pod events could not be read")
	assert.Empty(t, state.Recommendations)
	assert.Equal(t, 3, calls, "the events list is retried before it is given up")
}

// The checker's node is picked at random, so successive runs cover
// different paths.
func TestCheckNodeToNode_CheckerNodeVaries(t *testing.T) {
	picked := map[string]bool{}
	for i := 0; i < 20 && len(picked) < 2; i++ {
		f := newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil, checkerExit(0))
		runN2N(f)
		require.Len(t, f.checkerNodes, 1)
		picked[f.checkerNodes[0]] = true
	}
	assert.Len(t, picked, 2, "20 runs all picked the same checker node")
}

// The DaemonSet tolerates every taint, so it is pinned to the eligible nodes:
// a fenced node gets no probe pod, and is named as a coverage gap.
func TestCheckNodeToNode_PinsTheDaemonSetToEligibleNodes(t *testing.T) {
	nodes := readyNodes(3)
	nodes[2].Spec.Taints = []corev1.Taint{{Key: "karpenter.sh/disrupted", Effect: corev1.TaintEffectNoSchedule}}
	f := newN2NFixture(t, nodes, runningServers("node-1", "node-2"), nil, checkerExit(0))
	state := runN2N(f)

	require.NotNil(t, f.ds)
	affinity := f.ds.Spec.Template.Spec.Affinity
	require.NotNil(t, affinity)
	require.NotNil(t, affinity.NodeAffinity)
	require.NotNil(t, affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution)
	terms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	require.Len(t, terms, 1)
	require.Len(t, terms[0].MatchFields, 1)
	assert.Equal(t, []string{"node-1", "node-2"}, terms[0].MatchFields[0].Values)
	require.NotNil(t, state.NodeToNodeOK)
	assert.True(t, *state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"Node-to-Node: not probed on 1 node(s): node-3 (tainted karpenter.sh/disrupted)")
}

// A single eligible node is not applicable, and the text says how many nodes
// were left out rather than calling the cluster single-node.
func TestCheckNodeToNode_NotApplicableNamesTheSkippedNodes(t *testing.T) {
	nodes := readyNodes(3)
	nodes[1].Spec.Unschedulable = true
	nodes[2].Status.Conditions[0].Status = corev1.ConditionFalse
	objs := []runtime.Object{nodes[0], nodes[1], nodes[2]}
	state := &ValidationState{Log: testLog()}
	checkNodeToNode(context.Background(), fake.NewSimpleClientset(objs...), state, enforcementDefaultImg)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, state.NodeToNodeNotApplicable, "one eligible node of 3")
	assert.Contains(t, strings.Join(state.Warnings, "; "),
		"Node-to-Node: not probed on 2 node(s): node-2 (cordoned), node-3 (NotReady)")
}

// The checker is tried on at most nodeToNodeCheckerAttempts nodes.
func TestCheckNodeToNode_GivesUpAfterThreeRefusals(t *testing.T) {
	f := newN2NFixture(t, readyNodes(4), runningServers("node-1", "node-2", "node-3", "node-4"), nil,
		func() (*corev1.Pod, error) { return refusedChecker(), nil })
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Len(t, f.checkerNodes, nodeToNodeCheckerAttempts)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "the kubelet refused the checker pod on")
}

// A Failed checker whose container ran was not refused by the kubelet, so it
// is not retried elsewhere, and its exit code decides.
func TestCheckNodeToNode_FailedCheckerThatRanIsNotARefusal(t *testing.T) {
	f := newN2NFixture(t, readyNodes(3), runningServers("node-1", "node-2", "node-3"), nil,
		func() (*corev1.Pod, error) {
			p := checkerPod(137)
			p.Status.Reason = "DeadlineExceeded"
			return p, nil
		})
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Len(t, f.checkerNodes, 1)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "container exited 137")
}

// One running probe pod has no cross-node path to dial, so nothing was
// observed. A pod that could not pull its image says so, with the override.
func TestCheckNodeToNode_OneRunningPodIsUnknown(t *testing.T) {
	pulling := probePod("node-2", "Pending", "", "ImagePullBackOff")
	pulling.Name = "s-2"
	f := newN2NFixture(t, readyNodes(2), append(runningServers("node-1"), pulling), nil, checkerExit(0))
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Empty(t, f.checkerNodes, "no checker runs without a second node to dial")
	assert.Contains(t, strings.Join(state.Warnings, "; "), "probe pods did not start on two nodes")
	assert.Equal(t, []string{nodeToNodeImagePullRecommendation}, state.Recommendations)
}

// An image whose nc fails the loopback self-test is a coverage gap with the
// image advice, never an overlay fault.
func TestCheckNodeToNode_NetcatSelfTestFailureIsUnknown(t *testing.T) {
	f := newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil,
		checkerExit(nodeToNodeNoNetcatExit))
	state := runN2N(f)

	assert.Nil(t, state.NodeToNodeOK)
	assert.Equal(t, []string{nodeToNodeNetcatRecommendation}, state.Recommendations)
}

// A checker that cannot pull its image stops at once with the image advice,
// rather than waiting out its timeout and reporting no result.
func TestCheckNodeToNode_CheckerImagePullStopsEarly(t *testing.T) {
	f := newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil, func() (*corev1.Pod, error) {
		p := probePod("node-1", "Pending", "", "ErrImagePull")
		return &p, nil
	})
	var state *ValidationState
	finishesWithin(t, 10*time.Second, func() { state = runN2N(f) })

	assert.Nil(t, state.NodeToNodeOK)
	assert.Equal(t, []string{nodeToNodeImagePullRecommendation}, state.Recommendations)
}

// The run admits its own traffic in the probe namespace, so a default-deny
// policy added to every new namespace cannot fail the overlay. When that
// policy cannot be created, a failed connection is not evidence either.
func TestCheckNodeToNode_AllowPolicy(t *testing.T) {
	f := newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil, checkerExit(0))
	runN2N(f)
	policies, err := f.client.NetworkingV1().NetworkPolicies("").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, policies.Items, 1)
	pol := policies.Items[0]
	assert.True(t, strings.HasPrefix(pol.Namespace, nodeToNodeNSPrefix))
	tcp := corev1.ProtocolTCP
	port := intstr.FromInt(nodeToNodeTestPort)
	peers := []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}
	ports := []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}}
	assert.Equal(t, networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{},
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: peers, Ports: ports}},
		Egress:      []networkingv1.NetworkPolicyEgressRule{{To: peers, Ports: ports}},
	}, pol.Spec)

	g := newN2NFixture(t, readyNodes(2), runningServers("node-1", "node-2"), nil,
		checkerExit(nodeToNodeUnreachableExit))
	g.client.PrependReactor("create", "networkpolicies", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "networking.k8s.io", Resource: "networkpolicies"}, "", fmt.Errorf("denied"))
	})
	state := runN2N(g)
	assert.Nil(t, state.NodeToNodeOK)
	assert.Contains(t, strings.Join(state.Warnings, "; "), "allow NetworkPolicy was not admitted")
	assert.Empty(t, g.leftovers(t))
}

// No probe pod uses the API, so none carries a ServiceAccount token, and none
// pulls an image the node already has.
func TestProbePods_CarryNoCredentials(t *testing.T) {
	specs := map[string]corev1.PodSpec{
		"n2n server":         buildNodeToNodeDaemonSet("ds", "ns", nil, "img", []string{"node-1"}).Spec.Template.Spec,
		"n2n checker":        buildNodeToNodeCheckerPod("c", "ns", "node-1", nil, []string{"10.0.0.2"}, "img").Spec,
		"enforcement server": buildServerPod("ns", "img").Spec,
		"enforcement probe":  buildProbePod("ns", "p", "img", "client", "10.0.0.5", 0).Spec,
	}
	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, spec.AutomountServiceAccountToken)
			assert.False(t, *spec.AutomountServiceAccountToken)
			require.NotNil(t, spec.EnableServiceLinks)
			assert.False(t, *spec.EnableServiceLinks)
			for _, c := range spec.Containers {
				assert.Equal(t, corev1.PullIfNotPresent, c.ImagePullPolicy)
			}
		})
	}
}

// Launchers match on the start of the probe image advice, and it names the
// override for each of them.
func TestProbeImageRecommendation_IsAStableMarker(t *testing.T) {
	assert.Equal(t, "Node-to-node probe image ", ProbeImageRecommendation)
	for _, rec := range []string{nodeToNodeImagePullRecommendation, nodeToNodeNetcatRecommendation} {
		assert.True(t, strings.HasPrefix(rec, ProbeImageRecommendation), rec)
		assert.Contains(t, rec, "clusterValidator.nodeToNodeProbeImage")
		assert.Contains(t, rec, "--cluster-validator-probe-image")
		assert.Contains(t, rec, "NVCF_N2N_PROBE_IMAGE")
	}
}

// The enforcement test deletes only a namespace it created, named so that
// concurrent runs do not collide.
func TestCheckNetworkPolicyEnforcement_DeletesOnlyItsOwnNamespace(t *testing.T) {
	cfg := &EnforcementConfig{Enabled: true, TestImage: "busybox:1.36", TimeoutSeconds: 1}

	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "namespaces"}, "x")
	})
	deleted := false
	client.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
		deleted = true
		return true, nil, nil
	})
	state := &ValidationState{Log: testLog()}
	checkNetworkPolicyEnforcement(context.Background(), client, state, cfg)
	assert.False(t, deleted, "a namespace this run did not create must not be deleted")

	client = fake.NewSimpleClientset()
	var created *corev1.Namespace
	client.PrependReactor("create", "namespaces", func(a ktesting.Action) (bool, runtime.Object, error) {
		created = a.(ktesting.CreateAction).GetObject().(*corev1.Namespace).DeepCopy()
		return false, nil, nil
	})
	checkNetworkPolicyEnforcement(context.Background(), client, &ValidationState{Log: testLog()}, cfg)
	require.NotNil(t, created)
	assert.Regexp(t, regexp.MustCompile(`^netpol-validation-[a-z0-9]{6}$`), created.Name)
	assert.Equal(t, "nvcf-cluster-validator", created.Labels["app.kubernetes.io/managed-by"])
	_, err := client.CoreV1().Namespaces().Get(context.Background(), created.Name, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the namespace the run created is cleaned up")
}
