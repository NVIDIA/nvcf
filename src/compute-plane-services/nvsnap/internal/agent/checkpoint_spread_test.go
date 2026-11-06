// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// nodeAgents runs a fake agent for each node, each on its own loopback
// address and all on one port, as node agents are; it records the
// checkpoints each is asked to make local. failing nodes answer 500.
type nodeAgents struct {
	mu   sync.Mutex
	asks map[string][]string // node -> checkpoint ids
	port string
}

func startNodeAgents(t *testing.T, nodes []string, failing map[string]bool) (*nodeAgents, []*corev1.Node) {
	t.Helper()
	na := &nodeAgents{asks: map[string][]string{}}
	var lc net.ListenConfig
	var listeners []net.Listener
	for attempt := 0; attempt < 5 && listeners == nil; attempt++ {
		first, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, port, _ := net.SplitHostPort(first.Addr().String())
		listeners = []net.Listener{first}
		for i := 1; i < len(nodes); i++ {
			l, err := lc.Listen(t.Context(), "tcp", fmt.Sprintf("127.0.0.%d:%s", i+1, port))
			if err != nil { // the port is taken on that address: try another
				for _, l := range listeners {
					_ = l.Close()
				}
				listeners = nil
				break
			}
			listeners = append(listeners, l)
		}
		na.port = port
	}
	if listeners == nil {
		t.Skip("cannot bind one port on several loopback addresses")
	}
	kubeNodes := make([]*corev1.Node, 0, len(nodes))
	for i, node := range nodes {
		srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/checkpoints/"), "/ensure-local")
			na.mu.Lock()
			na.asks[node] = append(na.asks[node], id)
			na.mu.Unlock()
			if failing[node] {
				http.Error(w, "no source", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})}
		go func() { _ = srv.Serve(listeners[i]) }()
		t.Cleanup(func() { _ = srv.Close() })
		kubeNodes = append(kubeNodes, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: fmt.Sprintf("127.0.0.%d", i+1)}}}})
	}
	return na, kubeNodes
}

func (na *nodeAgents) asked(node string) []string {
	na.mu.Lock()
	defer na.mu.Unlock()
	return slices.Clone(na.asks[node])
}

// Each member under the rails minimum is asked for on every other member's
// node and not on its own; a member at or over it, or of unknown size, is
// not copied; one node failing does not stop the others.
func TestSpreadSmallCheckpoints(t *testing.T) {
	na, nodes := startNodeAgents(t, []string{"node-a", "node-b", "node-c"}, map[string]bool{"node-c": true})
	a, _ := receiverAgent(t, "")
	a.kubeClient = fake.NewClientset(nodes[0], nodes[1], nodes[2])
	a.config.ListenAddr = ":" + na.port
	a.config.Rails.MinSize = "1Mi"

	results := []*CheckpointResult{
		{CheckpointID: "rank0", CheckpointSize: 2 << 20}, // over the minimum
		{CheckpointID: "etcd", CheckpointSize: 100},
		{CheckpointID: "unknown", CheckpointSize: 0},
		{CheckpointID: "nats", CheckpointSize: 200},
	}
	members := []string{"node-a", "node-b", "node-c", "node-a"}
	a.spreadSmallCheckpoints(t.Context(), results, members, a.log.WithField("t", "spread"))

	for node, want := range map[string][]string{
		"node-a": {"etcd"},         // not nats: it was captured there
		"node-b": {"nats"},         // not etcd: it was captured there
		"node-c": {"etcd", "nats"}, // asked even though it fails
	} {
		if got := na.asked(node); !sameSet(got, want) {
			t.Errorf("%s asked for %v, want %v", node, got, want)
		}
	}
}

// A node whose agent cannot be found is skipped; the rest are still asked.
func TestSpreadSmallCheckpoints_UnknownNode(t *testing.T) {
	na, nodes := startNodeAgents(t, []string{"node-a", "node-b"}, nil)
	a, _ := receiverAgent(t, "")
	a.kubeClient = fake.NewClientset(nodes[0], nodes[1]) // node-gone has no Node
	a.config.ListenAddr = ":" + na.port
	a.spreadSmallCheckpoints(t.Context(), []*CheckpointResult{{CheckpointID: "etcd", CheckpointSize: 100}},
		[]string{"node-a", "node-gone", "node-b"}, a.log.WithField("t", "spread"))
	if got := na.asked("node-b"); !slices.Equal(got, []string{"etcd"}) {
		t.Errorf("node-b asked for %v, want [etcd]", got)
	}
	if got := na.asked("node-a"); len(got) != 0 {
		t.Errorf("the capture node was asked for %v", got)
	}
}
