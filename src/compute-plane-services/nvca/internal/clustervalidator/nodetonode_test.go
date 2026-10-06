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
	"os"
	"os/exec"
	"path/filepath"
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

// Every run reclaims the probe namespaces a killed probe left, whatever its
// role: the probe runs only for the control-plane role, but either role's
// validator can run on a cluster. Legacy probe DaemonSets are swept only by
// the control-plane role, the one granted DaemonSets.
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
			if role == RoleControlPlane {
				assert.True(t, apierrors.IsNotFound(err), "the legacy probe DaemonSet is swept")
			} else {
				assert.NoError(t, err, "a role without DaemonSet grants leaves it")
			}
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

// An events list that cannot be read leaves a scheduled pod with no IP
// undecided, end to end: the nodes that did start are probed, but the row
// cannot pass, as it could not had the events shown no sandbox event. No CNI
// advice is given, since nothing was seen to go wrong.
func TestCheckNodeToNode_EventsListErrorLeavesTheRowUnknown(t *testing.T) {
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

	assert.Nil(t, state.NodeToNodeOK, "warnings: %v", state.Warnings)
	warnings := strings.Join(state.Warnings, "; ")
	assert.Contains(t, warnings, "node-3: pod events could not be read")
	assert.Contains(t, warnings, "the overlay was verified from")
	assert.Contains(t, warnings, "the probe pod on node-3 was scheduled and got no pod IP")
	assert.Empty(t, state.Recommendations)
	assert.Greater(t, calls, 1, "the events list is retried before it is given up")
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
	assert.Equal(t, []string{"node-1", "node-2"}, pinnedNodes(t, terms))
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

// The checker retries a dial that the run's allow policy may not have reached
// yet, but not one the server refused.
func TestNodeToNodeCheckerScript_RetriesUnrefusedDials(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the checker script")
	}
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen")
	// The fake nc passes the loopback self-test, refuses 10.0.0.3, and times
	// out only the first dial to 10.0.0.2.
	fake := "#!/bin/sh\ncase \"$*\" in\n" +
		"*-l*|*127.0.0.1*) exit 0 ;;\n" +
		"*10.0.0.3*) echo 'Connection refused'; exit 1 ;;\n" +
		"*10.0.0.2*) [ -f " + seen + " ] && exit 0; : > " + seen + "; echo 'timed out'; exit 1 ;;\n" +
		"esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nc"), []byte(fake), 0o700))
	cmd := exec.Command(sh, "-c", nodeToNodeCheckerScript([]string{"10.0.0.2", "10.0.0.3"}))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, string(out))
	assert.Equal(t, nodeToNodeRefusedExit, exitErr.ExitCode(), string(out))
	assert.Contains(t, string(out), "refused: 10.0.0.3")
	assert.NotContains(t, string(out), "unreachable")
	assert.NotContains(t, string(out), "10.0.0.2:", "the retried dial succeeded")
}

// A validator disabled or uninstalled mid-run loses its RBAC (403), or its
// ServiceAccount and with it its token (401), before its cleanup runs. The
// cleanup then says the namespace is left behind and who reclaims it, once,
// rather than a warning per object.
func TestProbeCleanup_DeniedDeleteSaysWhoReclaims(t *testing.T) {
	denials := map[string]func(resource string) error{
		"forbidden": func(resource string) error {
			return apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "x", fmt.Errorf(
				`User "system:serviceaccount:nvca-operator:nvca-operator-cluster-validator" cannot delete resource %q`,
				resource))
		},
		"unauthorized": func(string) error { return apierrors.NewUnauthorized("Unauthorized") },
	}
	for name, deny := range denials {
		t.Run("node-to-node/"+name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, resource := range []string{"daemonsets", "pods", "namespaces"} {
				client.PrependReactor("delete", resource, func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, deny(resource)
				})
			}
			log, buf := bufferLog()
			newN2NProbe(client, &ValidationState{Log: log}, enforcementDefaultImg).cleanup()

			out := buf.String()
			assert.Contains(t, out, "is left behind")
			assert.Contains(t, out, "nvca-operator while it is installed")
			assert.Contains(t, out, orphanN2NNamespaceTTL.String())
			assert.Equal(t, 1, strings.Count(out, "\n"), "one warning, not one per object: %s", out)
			for _, a := range client.Actions() {
				assert.NotEqual(t, "pods", a.GetResource().Resource, "pod deletes would be denied too")
			}
		})
		t.Run("enforcement/"+name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("delete", "namespaces", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, deny("namespaces")
			})
			log, buf := bufferLog()
			cleanupTestNamespace(log, client, "netpol-validation-abc")
			assert.Contains(t, buf.String(), "netpol-validation-abc is left behind")
			assert.Contains(t, buf.String(), orphanNamespaceTTL.String())
		})
	}

	t.Run("other failures keep their own warnings", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		client.PrependReactor("delete", "*", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewServiceUnavailable("etcd leader change")
		})
		log, buf := bufferLog()
		newN2NProbe(client, &ValidationState{Log: log}, enforcementDefaultImg).cleanup()
		assert.Contains(t, buf.String(), "Failed to clean up probe DaemonSet")
		assert.Contains(t, buf.String(), "Failed to clean up probe namespace")
		assert.NotContains(t, buf.String(), "left behind")
	})
}

// The operator's sweep reclaims both kinds of probe namespace once they are
// past their TTL, and says when a later sweep has more to do: while a probe
// namespace is too young to delete, or was just deleted. A Terminating one
// has its probe pods forced out and leaves nothing more to do. A failed
// request is reported.
func TestSweepLeftoverProbes(t *testing.T) {
	n2n := func(name string, age time.Duration) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: nodeToNodeNSPrefix + name, Labels: n2nLabels(n2nNamespaceComponent, name),
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		}}
	}
	ctx := context.Background()
	exists := func(client kubernetes.Interface, name string) bool {
		_, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		return err == nil
	}
	sweep := func(t *testing.T, client kubernetes.Interface) bool {
		more, err := SweepLeftoverProbes(ctx, testLog(), client)
		require.NoError(t, err)
		return more
	}

	t.Run("young ones wait", func(t *testing.T) {
		client := fake.NewSimpleClientset(
			n2n("stale", orphanN2NNamespaceTTL+time.Minute), n2n("young", time.Minute),
			makeNetpolValidationNs("netpol-validation-stale", orphanNamespaceTTL+time.Minute),
			makeNetpolValidationNs("netpol-validation-young", time.Minute))
		assert.True(t, sweep(t, client))
		assert.False(t, exists(client, nodeToNodeNSPrefix+"stale"))
		assert.False(t, exists(client, "netpol-validation-stale"))
		assert.True(t, exists(client, nodeToNodeNSPrefix+"young"))
		assert.True(t, exists(client, "netpol-validation-young"))
	})
	for kind, young := range map[string]*corev1.Namespace{
		"node-to-node": n2n("young", time.Minute),
		"enforcement":  makeNetpolValidationNs("netpol-validation-young", time.Minute),
	} {
		t.Run("a young "+kind+" namespace waits", func(t *testing.T) {
			assert.True(t, sweep(t, fake.NewSimpleClientset(young)))
		})
	}
	for kind, tc := range map[string]struct {
		ns  *corev1.Namespace
		pod map[string]string
	}{
		"node-to-node": {n2n("stuck", orphanN2NNamespaceTTL+time.Minute), n2nLabels("", "stuck")},
		"enforcement": {makeNetpolValidationNs("netpol-validation-stuck", orphanNamespaceTTL+time.Minute),
			map[string]string{"app": "netpol-test", "role": "server"}},
	} {
		t.Run("a Terminating "+kind+" namespace has its probe pods forced out", func(t *testing.T) {
			tc.ns.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			tc.ns.Finalizers = []string{"kubernetes"}
			probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: tc.ns.Name, Labels: tc.pod}}
			other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: tc.ns.Name}}
			client := fake.NewSimpleClientset(tc.ns, probe, other)
			var forced []string
			client.PrependReactor("delete", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
				del := a.(ktesting.DeleteActionImpl)
				require.NotNil(t, del.DeleteOptions.GracePeriodSeconds)
				assert.Zero(t, *del.DeleteOptions.GracePeriodSeconds)
				forced = append(forced, del.Name)
				return false, nil, nil
			})
			assert.False(t, sweep(t, client), "nothing more for a later sweep to do")
			assert.Equal(t, []string{"probe"}, forced)
			assert.True(t, exists(client, tc.ns.Name), "a Terminating namespace is not deleted again")
		})
	}
	t.Run("nothing left", func(t *testing.T) {
		client := fake.NewSimpleClientset(n2n("stale", orphanN2NNamespaceTTL+time.Minute),
			makeNetpolValidationNs("netpol-validation-stale", orphanNamespaceTTL+time.Minute))
		assert.True(t, sweep(t, client), "a deleted namespace may still be terminating")
		assert.False(t, sweep(t, client))
		assert.False(t, sweep(t, fake.NewSimpleClientset()))
	})
	for _, call := range []struct{ verb, resource string }{
		{"list", "namespaces"}, {"delete", "namespaces"}, {"list", "pods"}, {"delete", "pods"},
	} {
		t.Run(call.verb+" "+call.resource+" failure", func(t *testing.T) {
			stuck := makeNetpolValidationNs("netpol-validation-stuck", orphanNamespaceTTL+time.Minute)
			stuck.DeletionTimestamp = &metav1.Time{Time: time.Now()}
			probe := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Name: "probe", Namespace: stuck.Name, Labels: map[string]string{"app": "netpol-test"},
			}}
			client := fake.NewSimpleClientset(n2n("stale", orphanN2NNamespaceTTL+time.Minute), stuck, probe)
			client.PrependReactor(call.verb, call.resource, func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewServiceUnavailable("etcd leader change")
			})
			_, err := SweepLeftoverProbes(ctx, testLog(), client)
			assert.ErrorContains(t, err, "etcd leader change")
		})
	}
}

// splitPairChecker is the checker of a 3-node overlay where node-1 and node-2
// cannot reach each other and both reach node-3.
func splitPairChecker(f **n2nFixture) func() (*corev1.Pod, error) {
	return func() (*corev1.Pod, error) {
		switch (*f).checkerNode {
		case "node-1":
			return checkerReported(nodeToNodeUnreachableExit, "unreachable: "+ipOf("node-2")), nil
		case "node-2":
			return checkerReported(nodeToNodeUnreachableExit, "unreachable: "+ipOf("node-1")), nil
		}
		return checkerPod(0), nil
	}
}

// failedNodeToNode runs the suite's node-to-node check until its random
// checker lands on a node of the broken pair, and returns that result.
func failedNodeToNode(t *testing.T, n2n check) *ValidationState {
	t.Helper()
	for range 50 {
		first := &ValidationState{Log: testLog(), Role: RoleControlPlane}
		n2n.run(context.Background(), first)
		if first.NodeToNodeOK != nil && !*first.NodeToNodeOK {
			return first
		}
	}
	t.Fatal("no first run failed")
	return nil
}

func checkNamed(checks []check, name string) check {
	for _, c := range checks {
		if c.name == name {
			return c
		}
	}
	panic("no check " + name)
}

// The recheck of a node-to-node failure dials from the same checker node to
// the same nodes. A checker picked anew tests other paths: with node-1 and
// node-2 unable to reach each other, a recheck from node-3 passed and
// replaced the failure it never retested.
func TestSuite_NodeToNodeRecheckDialsTheFailedPathsAgain(t *testing.T) {
	var f *n2nFixture
	f = newN2NFixture(t, readyNodes(3), runningServers("node-1", "node-2", "node-3"), nil, splitPairChecker(&f))
	su := &suite{client: f.client}
	n2n := checkNamed(su.checks(RoleControlPlane), "Node-to-Node Communication")
	first := failedNodeToNode(t, n2n)
	failedFrom := f.checkerNode

	for range 10 {
		results := []*ValidationState{first}
		state := &ValidationState{Log: testLog(), Role: RoleControlPlane}
		su.recheck(context.Background(), state, []check{n2n}, results, [][]string{nil})
		assert.Equal(t, failedFrom, f.checkerNode, "the recheck dials from the node the failure was seen from")
		require.NotNil(t, results[0].NodeToNodeOK)
		assert.False(t, *results[0].NodeToNodeOK)
	}
}

// A recheck that cannot dial the failed paths again leaves the failure
// standing: its checker node is no longer one to probe. A target that is no
// longer one to probe is left out, and the rest are dialled again.
func TestSuite_NodeToNodeRecheckOfPathsNoLongerProbed(t *testing.T) {
	var f *n2nFixture
	f = newN2NFixture(t, readyNodes(3), runningServers("node-1", "node-2", "node-3"), nil, splitPairChecker(&f))
	su := &suite{client: f.client}
	n2n := checkNamed(su.checks(RoleControlPlane), "Node-to-Node Communication")
	first := failedNodeToNode(t, n2n)
	failedFrom, checkers := f.checkerNode, len(f.checkerNodes)

	f.markNotReady(t, failedFrom)
	log, buf := bufferLog()
	results := []*ValidationState{first}
	su.recheck(context.Background(), &ValidationState{Log: log, Role: RoleControlPlane}, []check{n2n}, results,
		[][]string{nil})
	assert.Same(t, first, results[0], "the failure stands")
	assert.Len(t, f.checkerNodes, checkers, "no checker ran")
	assert.Contains(t, buf.String(), "checker node "+failedFrom+" is no longer one to probe")
	assert.Contains(t, buf.String(), "The recheck could not observe Node-to-Node Communication")

	for name, leave := range map[string]func(f *n2nFixture){
		"NotReady": func(f *n2nFixture) { f.markNotReady(t, "node-3") },
		"removed":  func(f *n2nFixture) { f.removeNode(t, "node-3") },
	} {
		var f *n2nFixture
		f = newN2NFixture(t, readyNodes(3), runningServers("node-1", "node-2", "node-3"), nil, splitPairChecker(&f))
		su := &suite{client: f.client}
		n2n := checkNamed(su.checks(RoleControlPlane), "Node-to-Node Communication")
		first := failedNodeToNode(t, n2n)
		failedFrom := f.checkerNode
		leave(f)
		results := []*ValidationState{first}
		su.recheck(context.Background(), &ValidationState{Log: testLog(), Role: RoleControlPlane}, []check{n2n},
			results, [][]string{nil})
		assert.Equal(t, failedFrom, f.checkerNode, name)
		assert.NotSame(t, first, results[0], "%s: the paths still probed are dialled again", name)
		require.NotNil(t, results[0].NodeToNodeOK, name)
		assert.False(t, *results[0].NodeToNodeOK, "%s: the pair still cannot reach each other", name)
		assert.Contains(t, strings.Join(results[0].Warnings, "; "), "node-3", "%s: node-3 is a coverage gap", name)
	}
}
