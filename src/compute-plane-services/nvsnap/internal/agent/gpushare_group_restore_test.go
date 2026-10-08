// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

// groupRestoreHarness serves a stub agent for every target node: ensure-local
// and metadata reads answer from checkpoints, and /v1/restore behaves like a
// gpushare member of a group restore (report restored, wait for the unlock,
// write its handle list, wait for the merged one).
type groupRestoreHarness struct {
	a        *Agent
	srv      *httptest.Server
	ckpts    map[string]*CheckpointMetadata
	failCkpt string

	mu       sync.Mutex
	requests []RestoreRequest
	ins      map[string]string
}

func newGroupRestoreHarness(t *testing.T, ckpts map[string]*CheckpointMetadata) *groupRestoreHarness {
	t.Helper()
	h := &groupRestoreHarness{ckpts: ckpts, ins: map[string]string{}}
	r := mux.NewRouter()
	h.a = &Agent{config: Config{CheckpointDir: t.TempDir()}}
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}", h.a.fabricStateHandler).Methods("GET")
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}/in", h.a.fabricInHandler).Methods("PUT")
	r.HandleFunc("/v1/gpushare/fabric/{session}/{namespace}/{pod}/unlock", h.a.fabricUnlockHandler).Methods("PUT")
	r.HandleFunc("/v1/checkpoints/{id}/ensure-local", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.ckpts[mux.Vars(r)["id"]]; !ok {
			http.Error(w, "no such checkpoint", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}).Methods("POST")
	r.HandleFunc("/v1/checkpoints/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		md, ok := h.ckpts[mux.Vars(r)["id"]]
		if !ok || r.URL.Query().Get("path") != "metadata.json" {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(md)
	}).Methods("GET")
	r.HandleFunc("/v1/restore", h.restore).Methods("POST")
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *groupRestoreHarness) restore(w http.ResponseWriter, r *http.Request) {
	var req RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	if req.CheckpointID == h.failCkpt {
		http.Error(w, "restore failed: criu-v2 restore: exit status 1", http.StatusInternalServerError)
		return
	}
	ckptDir := filepath.Join(h.a.config.CheckpointDir, req.CheckpointID)
	sess, err := h.a.openRestoreSession(restoreV2Group{Session: req.GPUShareFabricSession, InetAddrMap: req.InetAddrMap,
		Namespace: req.PlaceholderNamespace, Pod: req.PlaceholderPodName}, ckptDir, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer sess.close()
	v, err := sess.awaitUnlock(r.Context(), quietFabricLog())
	if err != nil || v != "unlock" {
		http.Error(w, fmt.Sprintf("restore failed: verdict %q %v", v, err), http.StatusInternalServerError)
		return
	}
	// The tool's resume: its list, then the merged one.
	if err := writeFileAtomic(sess.hostDir, "out", []byte(req.PlaceholderPodName+"-old "+req.PlaceholderPodName+"-new\n")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(filepath.Join(sess.hostDir, "in")); err == nil {
			h.mu.Lock()
			h.ins[req.PlaceholderPodName] = string(b)
			h.mu.Unlock()
			break
		}
		if time.Now().After(deadline) {
			http.Error(w, "no handle list", http.StatusInternalServerError)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(&RestoreResult{NewPodName: req.PlaceholderPodName})
}

// groupRestoreCluster has a node per target pod, all resolving to the
// harness, and the target pods with their new IPs.
func groupRestoreCluster(t *testing.T, h *groupRestoreHarness, podIPs map[string]string) *fake.Clientset {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(h.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	h.a.config.ListenAddr = ":" + port
	var objs []runtime.Object
	for pod, ip := range podIPs {
		node := "node-" + pod
		objs = append(objs,
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: host}}}},
			&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: pod, Namespace: "llm"}, Spec: corev1.PodSpec{NodeName: node}, Status: corev1.PodStatus{PodIP: ip}})
	}
	kc := fake.NewSimpleClientset(objs...)
	h.a.kubeClient = kc
	return kc
}

func groupCkpts() map[string]*CheckpointMetadata {
	return map[string]*CheckpointMetadata{
		"c0": {SourcePodIP: "10.0.0.1", GPUShareGroup: &GPUShareGroupInfo{Session: "sx", Index: 0, Size: 2}},
		"c1": {SourcePodIP: "10.0.0.2", GPUShareGroup: &GPUShareGroupInfo{Session: "sx", Index: 1, Size: 2}},
	}
}

func groupRestoreReq() GroupRestoreRequest {
	return GroupRestoreRequest{Members: []GroupRestoreMember{
		{CheckpointID: "c1", PlaceholderNamespace: "llm", PlaceholderPodName: "new-1"},
		{CheckpointID: "c0", PlaceholderNamespace: "llm", PlaceholderPodName: "new-0"},
	}}
}

func TestGroupRestore_OneMapUnlockThenExchange(t *testing.T) {
	h := newGroupRestoreHarness(t, groupCkpts())
	groupRestoreCluster(t, h, map[string]string{"new-0": "10.1.0.10", "new-1": "10.1.0.11"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := h.a.groupRestore(ctx, groupRestoreReq(), quietFabricLog())
	if err != nil {
		t.Fatalf("group restore: %v", err)
	}
	wantMap := "10.0.0.1=10.1.0.10,10.0.0.2=10.1.0.11"
	if res.InetAddrMap != wantMap {
		t.Errorf("map = %q, want %q (every pod, index order)", res.InetAddrMap, wantMap)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) != 2 {
		t.Fatalf("%d restores started, want 2", len(h.requests))
	}
	for _, r := range h.requests {
		if r.InetAddrMap != wantMap || r.GPUShareFabricSession != res.Session {
			t.Errorf("member %s restored with map %q session %q; every pod needs the same full map", r.PlaceholderPodName, r.InetAddrMap, r.GPUShareFabricSession)
		}
	}
	if !res.Fabric.Unlocked || !res.Fabric.Exchanged || res.Fabric.Handles != 2 {
		t.Errorf("fabric = %+v, want unlocked and both lists exchanged", res.Fabric)
	}
	if h.ins["new-0"] != h.ins["new-1"] || !strings.Contains(h.ins["new-0"], "new-0-old") || !strings.Contains(h.ins["new-0"], "new-1-old") {
		t.Errorf("merged lists = %q / %q", h.ins["new-0"], h.ins["new-1"])
	}
}

func TestGroupRestore_FailureDeletesTheTargetPods(t *testing.T) {
	h := newGroupRestoreHarness(t, groupCkpts())
	h.failCkpt = "c1"
	kc := groupRestoreCluster(t, h, map[string]string{"new-0": "10.1.0.10", "new-1": "10.1.0.11"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := h.a.groupRestore(ctx, groupRestoreReq(), quietFabricLog())
	if err == nil {
		t.Fatal("group restore succeeded with a failed member")
	}
	if res == nil || res.Fabric.Unlocked {
		t.Fatalf("result = %+v: the survivors must be aborted, not unlocked", res)
	}
	pods, _ := kc.CoreV1().Pods("llm").List(ctx, metav1.ListOptions{})
	if len(pods.Items) != 0 || len(res.DeletedPods) != 2 {
		t.Errorf("pods left %d, deleted %v; a failed instance's pods go, so their locks go with them", len(pods.Items), res.DeletedPods)
	}
}

func TestGroupRestore_RejectsCheckpointsOfOtherInstances(t *testing.T) {
	ckpts := groupCkpts()
	ckpts["c1"].GPUShareGroup.Session = "other"
	h := newGroupRestoreHarness(t, ckpts)
	groupRestoreCluster(t, h, map[string]string{"new-0": "10.1.0.10", "new-1": "10.1.0.11"})
	if _, err := h.a.groupRestore(context.Background(), groupRestoreReq(), quietFabricLog()); err == nil {
		t.Fatal("restored checkpoints of two instances as one")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) != 0 {
		t.Errorf("%d restores started before the plan was checked", len(h.requests))
	}
}
