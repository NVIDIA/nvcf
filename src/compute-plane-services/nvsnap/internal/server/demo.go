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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/db"
)

// DemoPhase represents the current phase of the demo session.
type DemoPhase string

// Demo state machine phases reported to the UI.
const (
	PhaseIdle          DemoPhase = "IDLE"
	PhaseDeploying     DemoPhase = "DEPLOYING"
	PhaseRunning       DemoPhase = "RUNNING"
	PhaseCheckpointing DemoPhase = "CHECKPOINTING"
	PhaseCheckpointed  DemoPhase = "CHECKPOINTED"
	PhaseRestoring     DemoPhase = "RESTORING"
	PhaseRestored      DemoPhase = "RESTORED"
)

const demoNamespace = "nvsnap-system"

// DemoCheckpoint holds info about a completed checkpoint.
type DemoCheckpoint struct {
	ID       string  `json:"id"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration"`
}

// DemoState is the full state returned to the UI on every poll.
type DemoState struct {
	Phase              DemoPhase        `json:"phase"`
	WorkloadType       string           `json:"workloadType"`
	PodName            string           `json:"podName"`
	PodStatus          string           `json:"podStatus"`
	NodeName           string           `json:"nodeName"`
	Message            string           `json:"message"`
	Error              string           `json:"error,omitempty"`
	Checkpoints        []DemoCheckpoint `json:"checkpoints"`
	DeployDuration     float64          `json:"deployDuration"`
	CheckpointDuration float64          `json:"checkpointDuration"`
	RestoreDuration    float64          `json:"restoreDuration"`
	StartedAt          *time.Time       `json:"startedAt,omitempty"`
}

// demoSession holds the mutable demo state, protected by a mutex.
type demoSession struct {
	mu     sync.Mutex
	state  DemoState
	cancel context.CancelFunc // cancels the current background operation
}

func newDemoSession() *demoSession {
	return &demoSession{
		state: DemoState{
			Phase:       PhaseIdle,
			Checkpoints: []DemoCheckpoint{},
		},
	}
}

// snapshot returns a copy of the current state.
func (d *demoSession) snapshot() DemoState {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.state
	// Copy checkpoints slice to avoid races
	s.Checkpoints = make([]DemoCheckpoint, len(d.state.Checkpoints))
	copy(s.Checkpoints, d.state.Checkpoints)
	return s
}

// --- HTTP Handlers ---

func (s *Server) demoGetState(w http.ResponseWriter, r *http.Request) {
	state := s.demo.snapshot()

	// Enrich with live pod status when a pod exists
	if state.PodName != "" && (state.Phase == PhaseDeploying || state.Phase == PhaseRunning ||
		state.Phase == PhaseRestoring || state.Phase == PhaseRestored) {
		pod, err := s.kubeClient.CoreV1().Pods(demoNamespace).Get(r.Context(), state.PodName, metav1.GetOptions{})
		if err == nil {
			state.PodStatus = podStatusMessage(pod)
		}
	}

	s.writeJSON(w, http.StatusOK, state)
}

func (s *Server) demoDeploy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkloadType string `json:"workloadType"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	cfg, ok := workloadConfigs[req.WorkloadType]
	if !ok {
		s.writeError(w, http.StatusBadRequest, "unknown workload type: "+req.WorkloadType)
		return
	}

	s.demo.mu.Lock()
	if s.demo.state.Phase != PhaseIdle {
		s.demo.mu.Unlock()
		s.writeError(w, http.StatusConflict, "demo not in IDLE phase (current: "+string(s.demo.state.Phase)+")")
		return
	}

	// Pick a GPU node
	nodeName, err := s.pickGPUNode(r.Context())
	if err != nil {
		s.demo.mu.Unlock()
		s.writeError(w, http.StatusInternalServerError, "no GPU node available: "+err.Error())
		return
	}

	now := time.Now()
	s.demo.state = DemoState{
		Phase:        PhaseDeploying,
		WorkloadType: req.WorkloadType,
		PodName:      cfg.PodName,
		NodeName:     nodeName,
		Message:      "Creating pod...",
		StartedAt:    &now,
		Checkpoints:  []DemoCheckpoint{},
	}
	snapshot := s.demo.state
	snapshot.Checkpoints = make([]DemoCheckpoint, 0)
	s.demo.mu.Unlock()
	s.hub.BroadcastJSON("demo:state", snapshot)

	ctx, cancel := context.WithCancel(context.Background())
	s.demo.mu.Lock()
	s.demo.cancel = cancel
	s.demo.mu.Unlock()

	go s.runDemoDeploy(ctx, cfg, nodeName)

	s.writeJSON(w, http.StatusAccepted, map[string]string{"message": "Deploy started"})
}

func (s *Server) demoInference(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Prompt    string `json:"prompt"`
		MaxTokens int    `json:"maxTokens"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Prompt == "" {
		req.Prompt = "The meaning of life is"
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 50
	}

	s.demo.mu.Lock()
	phase := s.demo.state.Phase
	wt := s.demo.state.WorkloadType
	podName := s.demo.state.PodName
	s.demo.mu.Unlock()

	if phase != PhaseRunning && phase != PhaseRestored {
		s.writeError(w, http.StatusConflict, "model not ready (phase: "+string(phase)+")")
		return
	}

	cfg := workloadConfigs[wt]

	// Get pod IP
	pod, err := s.kubeClient.CoreV1().Pods(demoNamespace).Get(r.Context(), podName, metav1.GetOptions{})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "pod not found: "+err.Error())
		return
	}
	podIP := pod.Status.PodIP
	if podIP == "" {
		s.writeError(w, http.StatusInternalServerError, "pod has no IP")
		return
	}

	// Proxy OpenAI-compatible completions request
	start := time.Now()
	completionReq, _ := json.Marshal(map[string]interface{}{
		"model":      cfg.Model,
		"prompt":     req.Prompt,
		"max_tokens": req.MaxTokens,
	})

	client := &http.Client{Timeout: 60 * time.Second}
	url := fmt.Sprintf("http://%s:%d/v1/completions", podIP, cfg.Port)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(completionReq))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "inference request failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		s.writeError(w, resp.StatusCode, "inference error: "+string(body))
		return
	}

	// Parse OpenAI completions response
	var completionResp struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &completionResp); err != nil {
		logrus.WithError(err).Warn("demo: decode completion response")
	}

	text := ""
	tokens := 0
	if len(completionResp.Choices) > 0 {
		text = completionResp.Choices[0].Text
	}
	tokens = completionResp.Usage.CompletionTokens

	latency := time.Since(start).Seconds()

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"text":    text,
		"tokens":  tokens,
		"latency": latency,
	})
}

func (s *Server) demoCheckpoint(w http.ResponseWriter, r *http.Request) {
	s.demo.mu.Lock()
	phase := s.demo.state.Phase
	wt := s.demo.state.WorkloadType
	podName := s.demo.state.PodName
	nodeName := s.demo.state.NodeName
	s.demo.mu.Unlock()

	if phase != PhaseRunning && phase != PhaseRestored {
		s.writeError(w, http.StatusConflict, "cannot checkpoint in phase: "+string(phase))
		return
	}

	now := time.Now()
	s.demoUpdateState(func(st *DemoState) {
		st.Phase = PhaseCheckpointing
		st.Message = "Freezing processes..."
		st.StartedAt = &now
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.demo.mu.Lock()
	s.demo.cancel = cancel
	s.demo.mu.Unlock()

	go s.runDemoCheckpoint(ctx, wt, podName, nodeName)

	s.writeJSON(w, http.StatusAccepted, map[string]string{"message": "Checkpoint started"})
}

func (s *Server) demoRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CheckpointID string `json:"checkpointId"`
		// TargetNode optionally pins the restore pod to a specific node
		// (overrides the webhook's auto-pin to manifest.CapturedOnNodes).
		// Empty = let the webhook use CapturedOnNodes — i.e., the
		// capture-source node, zero-byte same-node restore.
		// Set = demonstrate cross-node restore via tier-2 peer or
		// tier-3 blobstore fetch.
		TargetNode string `json:"targetNode"`
	}
	// Body is optional
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.demo.mu.Lock()
	phase := s.demo.state.Phase
	wt := s.demo.state.WorkloadType
	nodeName := s.demo.state.NodeName
	checkpoints := make([]DemoCheckpoint, len(s.demo.state.Checkpoints))
	copy(checkpoints, s.demo.state.Checkpoints)
	s.demo.mu.Unlock()

	if phase != PhaseCheckpointed {
		s.writeError(w, http.StatusConflict, "cannot restore in phase: "+string(phase))
		return
	}

	// Default to latest checkpoint
	ckptID := req.CheckpointID
	if ckptID == "" && len(checkpoints) > 0 {
		ckptID = checkpoints[len(checkpoints)-1].ID
	}
	if ckptID == "" {
		s.writeError(w, http.StatusBadRequest, "no checkpoint available")
		return
	}

	now := time.Now()
	s.demoUpdateState(func(st *DemoState) {
		st.Phase = PhaseRestoring
		st.Message = "Restoring from checkpoint..."
		st.StartedAt = &now
	})

	ctx, cancel := context.WithCancel(context.Background())
	s.demo.mu.Lock()
	s.demo.cancel = cancel
	s.demo.mu.Unlock()

	go s.runDemoRestore(ctx, wt, nodeName, ckptID, req.TargetNode)

	s.writeJSON(w, http.StatusAccepted, map[string]string{"message": "Restore started"})
}

func (s *Server) demoScaleOut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replicas int `json:"replicas"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Replicas < 2 {
		req.Replicas = 2
	}
	if req.Replicas > 8 {
		req.Replicas = 8
	}

	s.demo.mu.Lock()
	wt := s.demo.state.WorkloadType
	nodeName := s.demo.state.NodeName
	checkpoints := make([]DemoCheckpoint, len(s.demo.state.Checkpoints))
	copy(checkpoints, s.demo.state.Checkpoints)
	s.demo.mu.Unlock()

	if len(checkpoints) == 0 {
		s.writeError(w, http.StatusBadRequest, "no checkpoint available")
		return
	}
	ckptID := checkpoints[len(checkpoints)-1].ID

	cfg := workloadConfigs[wt]
	log := s.log.WithFields(logrus.Fields{"handler": "demo-scale-out", "replicas": req.Replicas})

	s.demoLog(fmt.Sprintf("Scaling out to %d replicas from checkpoint %s", req.Replicas, ckptID))

	var created []string
	for i := 0; i < req.Replicas; i++ {
		suffix := string(rune('a' + i))
		podName := fmt.Sprintf("%s-%s", cfg.RestorePodName, suffix)

		manifest := cfg.RestoreManifest
		manifest = strings.ReplaceAll(manifest, "__NODE_NAME__", nodeName)
		manifest = strings.ReplaceAll(manifest, "__CHECKPOINT_ID__", ckptID)

		var pod corev1.Pod
		if err := sigsyaml.Unmarshal([]byte(manifest), &pod); err != nil {
			log.WithError(err).Error("Failed to parse manifest")
			continue
		}

		// Unique name and GPU assignment
		pod.Name = podName
		pod.Labels["app"] = podName
		for j := range pod.Spec.Containers {
			for k := range pod.Spec.Containers[j].Env {
				if pod.Spec.Containers[j].Env[k].Name == "CUDA_VISIBLE_DEVICES" {
					pod.Spec.Containers[j].Env[k].Value = fmt.Sprintf("%d", i)
				}
			}
		}

		_, err := s.kubeClient.CoreV1().Pods(demoNamespace).Create(r.Context(), &pod, metav1.CreateOptions{})
		if err != nil {
			log.WithError(err).WithField("pod", podName).Warn("Failed to create replica")
			continue
		}
		created = append(created, podName)
		s.demoLog(fmt.Sprintf("Created replica %s on GPU %d", podName, i))
	}

	// Update demo state to reflect scaled-out replicas
	if len(created) > 0 {
		s.demoUpdateState(func(st *DemoState) {
			st.Phase = PhaseRestored
			st.Message = fmt.Sprintf("Scaled to %d replicas", len(created))
			st.PodName = created[0] // Primary replica
		})
	}

	s.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"message":  fmt.Sprintf("Created %d replicas", len(created)),
		"replicas": created,
	})
}

func (s *Server) demoCleanup(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithField("handler", "demo-cleanup")

	// Cancel any running background operation
	s.demo.mu.Lock()
	if s.demo.cancel != nil {
		s.demo.cancel()
		s.demo.cancel = nil
	}
	s.demo.mu.Unlock()

	// Delete all demo-labeled pods
	err := s.kubeClient.CoreV1().Pods(demoNamespace).DeleteCollection(
		r.Context(),
		metav1.DeleteOptions{},
		metav1.ListOptions{LabelSelector: "nvsnap.io/demo=true"},
	)
	if err != nil {
		log.WithError(err).Warn("Failed to delete demo pods")
	}

	// Wait for demo pods to be fully gone (up to 30s)
	for i := 0; i < 15; i++ {
		pods, err := s.kubeClient.CoreV1().Pods(demoNamespace).List(
			r.Context(), metav1.ListOptions{LabelSelector: "nvsnap.io/demo=true"})
		if err != nil || len(pods.Items) == 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	s.demoUpdateState(func(st *DemoState) {
		*st = DemoState{
			Phase:       PhaseIdle,
			Checkpoints: []DemoCheckpoint{},
		}
	})

	s.writeJSON(w, http.StatusOK, map[string]string{"message": "Demo reset"})
}

// demoWorkloads returns the list of available demo workloads from the
// catalog (deploy/k8s/workloads/*.yaml, parsed at startup). The UI
// fetches this list at mount time and renders tiles; no hardcoded set
// in JS. Adding a workload = drop a yaml pair in the catalog dir +
// rebuild the nvsnap-server image (or mount a different catalog dir).
//
// Sorted: by GPU count, then by name, so single-GPU tiles always
// render before multi-GPU.
func (s *Server) demoWorkloads(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Desc     string `json:"desc"`
		Model    string `json:"model"`
		Port     int    `json:"port"`
		GPUs     int    `json:"gpus"`
		Path     string `json:"path"`
		CkptSize string `json:"ckpt_size"`
	}
	out := make([]entry, 0, len(workloadConfigs))
	for k := range workloadConfigs {
		c := workloadConfigs[k]
		out = append(out, entry{
			ID: c.ID, Name: c.DemoName, Desc: c.Desc, Model: c.Model,
			Port: c.Port, GPUs: c.GPUs, Path: string(c.Path), CkptSize: c.CkptSize,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GPUs != out[j].GPUs {
			return out[i].GPUs < out[j].GPUs
		}
		return out[i].ID < out[j].ID
	})
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) demoManifest(w http.ResponseWriter, r *http.Request) {
	wt := r.URL.Query().Get("workload")
	if wt == "" {
		s.demo.mu.Lock()
		wt = s.demo.state.WorkloadType
		s.demo.mu.Unlock()
	}
	cfg, ok := workloadConfigs[wt]
	if !ok {
		s.writeError(w, http.StatusBadRequest, "unknown workload: "+wt)
		return
	}
	manifestType := r.URL.Query().Get("type") // "deploy" or "restore"
	if manifestType == "restore" {
		s.writeJSON(w, http.StatusOK, map[string]string{"yaml": cfg.RestoreManifest, "type": "restore", "workload": wt})
	} else {
		s.writeJSON(w, http.StatusOK, map[string]string{"yaml": cfg.DeployManifest, "type": "deploy", "workload": wt})
	}
}

func (s *Server) demoCleanTestPods(w http.ResponseWriter, r *http.Request) {
	log := s.log.WithField("handler", "demo-clean-test-pods")

	// Build the set of pod names to clean from BOTH:
	//  1. Labelled pods (nvsnap.io/demo=true) — current contract.
	//  2. Every workloadConfig's PodName + RestorePodName + scaled-replica
	//     suffixes (-a..-h). Catches pods deployed before the label was
	//     added, or applied directly via kubectl rather than the server.
	toDelete := map[string]struct{}{}

	pods, err := s.kubeClient.CoreV1().Pods(demoNamespace).List(
		r.Context(), metav1.ListOptions{LabelSelector: "nvsnap.io/demo=true"})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range pods.Items {
		toDelete[pods.Items[i].Name] = struct{}{}
	}
	for k := range workloadConfigs {
		cfg := workloadConfigs[k]
		toDelete[cfg.PodName] = struct{}{}
		toDelete[cfg.RestorePodName] = struct{}{}
		for i := 0; i < 8; i++ {
			suffix := string(rune('a' + i))
			toDelete[fmt.Sprintf("%s-%s", cfg.RestorePodName, suffix)] = struct{}{}
		}
	}

	deleted := 0
	for name := range toDelete {
		err := s.kubeClient.CoreV1().Pods(demoNamespace).Delete(
			r.Context(), name, metav1.DeleteOptions{})
		if err == nil {
			deleted++
			continue
		}
		if !apierrors.IsNotFound(err) {
			log.WithError(err).WithField("pod", name).Warn("Failed to delete test pod")
		}
	}

	log.WithField("deleted", deleted).Info("Cleaned test pods")
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"deleted": deleted,
		"message": fmt.Sprintf("Deleted %d test pod(s)", deleted),
	})
}

func (s *Server) demoPods(w http.ResponseWriter, r *http.Request) {
	pods, err := s.kubeClient.CoreV1().Pods(demoNamespace).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type podRow struct {
		Name     string `json:"name"`
		Ready    string `json:"ready"`
		Status   string `json:"status"`
		Restarts int32  `json:"restarts"`
		Age      string `json:"age"`
	}

	rows := make([]podRow, 0, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]

		// Compute ready count
		readyCount := 0
		totalCount := len(pod.Spec.Containers)
		var restarts int32
		for j := range pod.Status.ContainerStatuses {
			cs := &pod.Status.ContainerStatuses[j]
			if cs.Ready {
				readyCount++
			}
			restarts += cs.RestartCount
		}

		// Status string (match kubectl logic)
		status := string(pod.Status.Phase)
		for j := range pod.Status.ContainerStatuses {
			cs := &pod.Status.ContainerStatuses[j]
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				status = cs.State.Waiting.Reason
			}
		}
		for j := range pod.Status.InitContainerStatuses {
			cs := &pod.Status.InitContainerStatuses[j]
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				status = "Init:" + cs.State.Waiting.Reason
			} else if cs.State.Running != nil {
				completedInit := 0
				for k := range pod.Status.InitContainerStatuses {
					ics := &pod.Status.InitContainerStatuses[k]
					if ics.Ready || (ics.State.Terminated != nil && ics.State.Terminated.ExitCode == 0) {
						completedInit++
					}
				}
				status = fmt.Sprintf("Init:%d/%d", completedInit, len(pod.Spec.InitContainers))
			}
		}
		if pod.DeletionTimestamp != nil {
			status = "Terminating"
		}

		// Age
		age := time.Since(pod.CreationTimestamp.Time)
		ageStr := ""
		switch {
		case age.Hours() >= 24:
			ageStr = fmt.Sprintf("%dd", int(age.Hours()/24))
		case age.Hours() >= 1:
			ageStr = fmt.Sprintf("%dh", int(age.Hours()))
		case age.Minutes() >= 1:
			ageStr = fmt.Sprintf("%dm", int(age.Minutes()))
		default:
			ageStr = fmt.Sprintf("%ds", int(age.Seconds()))
		}

		rows = append(rows, podRow{
			Name:     pod.Name,
			Ready:    fmt.Sprintf("%d/%d", readyCount, totalCount),
			Status:   status,
			Restarts: restarts,
			Age:      ageStr,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"pods": rows,
	})
}

func (s *Server) demoCheckpointFiles(w http.ResponseWriter, r *http.Request) {
	s.demo.mu.Lock()
	nodeName := s.demo.state.NodeName
	checkpoints := make([]DemoCheckpoint, len(s.demo.state.Checkpoints))
	copy(checkpoints, s.demo.state.Checkpoints)
	s.demo.mu.Unlock()

	if len(checkpoints) == 0 {
		s.writeError(w, http.StatusBadRequest, "no checkpoint available")
		return
	}
	ckptID := checkpoints[len(checkpoints)-1].ID

	nodeIP, err := s.nodeIP(r.Context(), nodeName)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "node IP lookup failed: "+err.Error())
		return
	}

	path := r.URL.Query().Get("path")
	url := fmt.Sprintf("http://%s:%d/v1/checkpoints/%s/files?path=%s", nodeIP, s.config.AgentPort, ckptID, path)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "agent request failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) demoCheckpointFile(w http.ResponseWriter, r *http.Request) {
	s.demo.mu.Lock()
	nodeName := s.demo.state.NodeName
	checkpoints := make([]DemoCheckpoint, len(s.demo.state.Checkpoints))
	copy(checkpoints, s.demo.state.Checkpoints)
	s.demo.mu.Unlock()

	if len(checkpoints) == 0 {
		s.writeError(w, http.StatusBadRequest, "no checkpoint available")
		return
	}
	ckptID := checkpoints[len(checkpoints)-1].ID

	nodeIP, err := s.nodeIP(r.Context(), nodeName)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "node IP lookup failed: "+err.Error())
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		s.writeError(w, http.StatusBadRequest, "path parameter required")
		return
	}

	url := fmt.Sprintf("http://%s:%d/v1/checkpoints/%s/file?path=%s", nodeIP, s.config.AgentPort, ckptID, path)
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "agent request failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// --- Background operations ---

func (s *Server) runDemoDeploy(ctx context.Context, cfg WorkloadConfig, nodeName string) {
	log := s.log.WithFields(logrus.Fields{"handler": "demo-deploy", "workload": cfg.PodName})

	// Substitute node name in manifest
	manifest := strings.ReplaceAll(cfg.DeployManifest, "__NODE_NAME__", nodeName)

	// Parse manifest
	var pod corev1.Pod
	if err := sigsyaml.Unmarshal([]byte(manifest), &pod); err != nil {
		log.WithError(err).Error("Failed to parse deploy manifest")
		s.demoSetError("Failed to parse manifest: " + err.Error())
		return
	}

	// Wait for any existing pod with same name to be fully gone
	for i := 0; i < 30; i++ {
		_, err := s.kubeClient.CoreV1().Pods(demoNamespace).Get(ctx, cfg.PodName, metav1.GetOptions{})
		if err != nil {
			break // Pod doesn't exist
		}
		if ctx.Err() != nil {
			return // Cancelled
		}
		log.Info("Waiting for old pod to be deleted...")
		time.Sleep(2 * time.Second)
	}

	s.demoLog("Creating pod " + cfg.PodName + "...")
	_, err := s.kubeClient.CoreV1().Pods(demoNamespace).Create(ctx, &pod, metav1.CreateOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to create pod")
		s.demoSetError("Failed to create pod: " + err.Error())
		return
	}

	s.demoLog("Pod created, waiting for init containers...")
	s.demoUpdateState(func(st *DemoState) {
		st.Message = "Pod created, waiting for init containers..."
	})

	// Poll until pod is ready (readiness probe passes)
	if err := s.waitForPodReady(ctx, demoNamespace, cfg.PodName, 10*time.Minute); err != nil {
		log.WithError(err).Error("Pod didn't become ready")
		s.demoSetError("Pod startup failed: " + err.Error())
		return
	}

	s.demoLog("Model loaded and ready")
	s.demoUpdateState(func(st *DemoState) {
		elapsed := 0.0
		if st.StartedAt != nil {
			elapsed = time.Since(*st.StartedAt).Seconds()
		}
		st.Phase = PhaseRunning
		st.PodStatus = "Ready"
		st.Message = fmt.Sprintf("Model ready (%.0fs)", elapsed)
		st.DeployDuration = elapsed
		st.StartedAt = nil
	})

	log.Info("Deploy completed")
}

func (s *Server) runDemoCheckpoint(ctx context.Context, _, podName, nodeName string) {
	log := s.log.WithFields(logrus.Fields{"handler": "demo-checkpoint", "pod": podName})

	// Re-resolve the pod's actual node at checkpoint time. The nodeName
	// captured at deploy time can be stale if the pod was rescheduled
	// (GPU availability, node pressure, etc.).
	pod, err := s.kubeClient.CoreV1().Pods(demoNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to get pod for node resolution")
		s.demoSetError("Pod lookup failed: " + err.Error())
		return
	}
	if pod.Spec.NodeName == "" {
		s.demoSetError("Pod has no node assigned (not yet scheduled)")
		return
	}
	if pod.Spec.NodeName != nodeName {
		log.WithFields(logrus.Fields{
			"cached_node": nodeName,
			"actual_node": pod.Spec.NodeName,
		}).Info("Pod rescheduled since deploy; using live node")
	}
	nodeName = pod.Spec.NodeName
	s.demoUpdateState(func(st *DemoState) { st.NodeName = nodeName })

	// Multi-GPU pods route to the rootfs path. CRIU + cuda-checkpoint
	// doesn't support TP>1 (libcudart wall). Same shared helper as
	// the generic /api/v1/checkpoints endpoint — one mechanism, two
	// callers (demo state machine here, CRD/catalog there).
	if podGPUCount(pod) >= 2 {
		s.runDemoRootfsCheckpoint(ctx, pod, log)
		return
	}

	nodeIP, err := s.nodeIP(ctx, nodeName)
	if err != nil {
		log.WithError(err).Error("Failed to get node IP")
		s.demoSetError("Node IP lookup failed: " + err.Error())
		return
	}

	// Proxy to agent checkpoint endpoint
	agentReq, _ := json.Marshal(map[string]interface{}{
		"namespace":    demoNamespace,
		"podName":      podName,
		"leaveRunning": false,
	})
	url := fmt.Sprintf("http://%s:%d/v1/checkpoint", nodeIP, s.config.AgentPort)
	log.WithField("url", url).Info("Proxying checkpoint to agent")

	s.demoLog("Freezing cgroup and dumping GPU memory...")
	s.demoUpdateState(func(st *DemoState) {
		st.Message = "Dumping GPU memory..."
	})

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(agentReq))
	if err != nil {
		log.WithError(err).Error("Failed to build agent checkpoint request")
		s.demoSetError("Checkpoint failed: " + err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		log.WithError(err).Error("Agent checkpoint failed")
		s.demoSetError("Checkpoint failed: " + err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.WithField("status", resp.StatusCode).Error("Agent returned error: " + string(respBody))
		s.demoSetError("Checkpoint error: " + string(respBody))
		return
	}

	// Parse agent response
	var result struct {
		CheckpointID   string  `json:"checkpointId"`
		CheckpointSize int64   `json:"checkpointSize"`
		Duration       float64 `json:"durationSeconds"`
	}
	if uerr := json.Unmarshal(respBody, &result); uerr != nil {
		logrus.WithError(uerr).Warn("demo: decode checkpoint status")
	}

	s.demoLog(fmt.Sprintf("Checkpoint complete: %s (%.1fs)", result.CheckpointID, result.Duration))
	log.WithFields(logrus.Fields{
		"checkpointId": result.CheckpointID,
		"size":         result.CheckpointSize,
		"duration":     result.Duration,
	}).Info("Checkpoint completed")

	// Delete the original pod
	s.demoLog("Removing original pod...")
	s.demoUpdateState(func(st *DemoState) {
		st.Message = "Removing original pod..."
	})

	err = s.kubeClient.CoreV1().Pods(demoNamespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		log.WithError(err).Warn("Failed to delete original pod")
	}

	// Wait for pod to be gone
	for i := 0; i < 30; i++ {
		_, err := s.kubeClient.CoreV1().Pods(demoNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			break // Pod is gone
		}
		time.Sleep(2 * time.Second)
	}

	ckpt := DemoCheckpoint{
		ID:       result.CheckpointID,
		Size:     result.CheckpointSize,
		Duration: result.Duration,
	}
	s.demoUpdateState(func(st *DemoState) {
		elapsed := 0.0
		if st.StartedAt != nil {
			elapsed = time.Since(*st.StartedAt).Seconds()
		}
		st.Phase = PhaseCheckpointed
		st.PodName = ""
		st.PodStatus = ""
		st.Message = fmt.Sprintf("Checkpoint saved (%.0fs)", elapsed)
		st.CheckpointDuration = elapsed
		st.StartedAt = nil
		st.Checkpoints = append(st.Checkpoints, ckpt)
	})
}

// nodeShortName returns the last hyphen-delimited segment of a long
// K8s node name (e.g. "gke-.../...-gpu-b6f5872c-4r1x" → "4r1x") for
// human-readable demo logs.
func nodeShortName(n string) string {
	if i := strings.LastIndex(n, "-"); i >= 0 && i < len(n)-1 {
		return n[i+1:]
	}
	return n
}

// runDemoRootfsCheckpoint is the rootfs counterpart of the inline
// CRIU branch in runDemoCheckpoint. Shares dispatchRootfsCapture with
// the generic /api/v1/checkpoints handler — only the demo state
// machine bookkeeping is duplicated here. The source pod is NOT
// deleted (rootfs capture is non-destructive; the pod keeps serving
// inference while restored copies come up elsewhere).
func (s *Server) runDemoRootfsCheckpoint(ctx context.Context, pod *corev1.Pod, log *logrus.Entry) {
	s.demoLog("Multi-GPU pod — using rootfs-only capture path...")
	s.demoUpdateState(func(st *DemoState) { st.Message = "Labeling pod for rootfs capture..." })

	hash, size, err := s.dispatchRootfsCapture(ctx, pod.Namespace, pod.Name, string(pod.UID), log)
	if err != nil {
		log.WithError(err).Error("Rootfs capture failed")
		s.demoSetError("Rootfs capture failed: " + err.Error())
		return
	}

	ckpt := DemoCheckpoint{
		ID:   hash,
		Size: size,
	}
	s.demoUpdateState(func(st *DemoState) {
		elapsed := 0.0
		if st.StartedAt != nil {
			elapsed = time.Since(*st.StartedAt).Seconds()
		}
		ckpt.Duration = elapsed
		st.Phase = PhaseCheckpointed
		// Source pod stays alive on the rootfs path — don't clear PodName.
		st.PodStatus = "Captured (rootfs)"
		st.Message = fmt.Sprintf("Rootfs capture committed (%.0fs)", elapsed)
		st.CheckpointDuration = elapsed
		st.StartedAt = nil
		st.Checkpoints = append(st.Checkpoints, ckpt)
	})
	s.demoLog(fmt.Sprintf("Rootfs capture committed: hash=%s size=%d", hash, size))
}

func (s *Server) runDemoRestore(ctx context.Context, workloadType, nodeName, checkpointID, targetNode string) {
	log := s.log.WithFields(logrus.Fields{"handler": "demo-restore", "checkpoint": checkpointID})

	cfg := workloadConfigs[workloadType]

	// Substitute placeholders in restore manifest. __NODE_NAME__ is the
	// pre-baked nodeAffinity hint to the capture-source node; the
	// webhook overrides this when nvsnap.io/target-node is set, so for
	// cross-node restore we keep __NODE_NAME__ pointing to the source
	// and let the annotation drive the actual pin.
	manifest := cfg.RestoreManifest
	manifest = strings.ReplaceAll(manifest, "__NODE_NAME__", nodeName)
	manifest = strings.ReplaceAll(manifest, "__CHECKPOINT_ID__", checkpointID)

	// Parse and create restore pod
	var pod corev1.Pod
	if err := sigsyaml.Unmarshal([]byte(manifest), &pod); err != nil {
		log.WithError(err).Error("Failed to parse restore manifest")
		s.demoSetError("Failed to parse restore manifest: " + err.Error())
		return
	}
	if targetNode != "" && targetNode != nodeName {
		if pod.Annotations == nil {
			pod.Annotations = map[string]string{}
		}
		pod.Annotations["nvsnap.io/target-node"] = targetNode
		// Clear any baked-in nodeSelector/nodeName so the webhook's
		// affinity injection (driven by the annotation) is what pins.
		pod.Spec.NodeName = ""
		pod.Spec.NodeSelector = nil
		log.WithFields(logrus.Fields{
			"source_node": nodeName,
			"target_node": targetNode,
		}).Info("Cross-node restore: setting nvsnap.io/target-node annotation")
		s.demoLog(fmt.Sprintf("Cross-node restore: %s → %s (cascade fetch on target)", nodeShortName(nodeName), nodeShortName(targetNode)))
	}

	s.demoLog("Creating restore pod " + cfg.RestorePodName + "...")
	s.demoUpdateState(func(st *DemoState) {
		st.PodName = cfg.RestorePodName
		st.Message = "Creating restore pod..."
	})

	_, err := s.kubeClient.CoreV1().Pods(demoNamespace).Create(ctx, &pod, metav1.CreateOptions{})
	if err != nil {
		log.WithError(err).Error("Failed to create restore pod")
		s.demoSetError("Failed to create restore pod: " + err.Error())
		return
	}

	s.demoLog("Restoring GPU state via CRIU...")
	s.demoUpdateState(func(st *DemoState) {
		st.Message = "Restoring GPU state..."
	})

	// Wait for readiness probe to pass
	if err := s.waitForPodReady(ctx, demoNamespace, cfg.RestorePodName, 10*time.Minute); err != nil {
		log.WithError(err).Error("Restore pod didn't become ready")
		s.demoSetError("Restore pod failed: " + err.Error())
		return
	}

	s.demoLog("Restore complete, model ready")
	s.demoUpdateState(func(st *DemoState) {
		elapsed := 0.0
		if st.StartedAt != nil {
			elapsed = time.Since(*st.StartedAt).Seconds()
		}
		st.Phase = PhaseRestored
		st.PodStatus = "Ready"
		st.Message = fmt.Sprintf("Restored (%.0fs)", elapsed)
		st.RestoreDuration = elapsed
		st.StartedAt = nil
	})

	log.Info("Restore completed")
}

// --- Helpers ---

// demoUpdateState atomically mutates demo state and broadcasts via WebSocket.
// Also persists to the DB catalog for crash recovery.
func (s *Server) demoUpdateState(fn func(*DemoState)) {
	s.demo.mu.Lock()
	fn(&s.demo.state)
	snapshot := s.demo.state
	// Copy checkpoints to avoid races
	snapshot.Checkpoints = make([]DemoCheckpoint, len(s.demo.state.Checkpoints))
	copy(snapshot.Checkpoints, s.demo.state.Checkpoints)
	s.demo.mu.Unlock()
	s.hub.BroadcastJSON("demo:state", snapshot)
	s.persistDemoState()
}

// demoLog broadcasts a log message to all WebSocket clients subscribed to demo:logs.
func (s *Server) demoLog(message string) {
	s.hub.BroadcastJSON("demo:logs", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339Nano),
		"message":   message,
	})
}

func (s *Server) demoSetError(msg string) {
	s.demoUpdateState(func(st *DemoState) {
		st.Phase = PhaseIdle
		st.Error = msg
		st.StartedAt = nil
	})
}

// demoPodPoller runs a goroutine that polls K8s pods in the demo namespace
// and broadcasts changes to WebSocket clients.
func (s *Server) demoPodPoller(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastJSON string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pods, err := s.kubeClient.CoreV1().Pods(demoNamespace).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}
			type podRow struct {
				Name     string `json:"name"`
				Ready    string `json:"ready"`
				Status   string `json:"status"`
				Restarts int32  `json:"restarts"`
				Age      string `json:"age"`
			}
			rows := make([]podRow, 0, len(pods.Items))
			for i := range pods.Items {
				pod := &pods.Items[i]
				readyCount := 0
				totalCount := len(pod.Spec.Containers)
				var restarts int32
				for j := range pod.Status.ContainerStatuses {
					cs := &pod.Status.ContainerStatuses[j]
					if cs.Ready {
						readyCount++
					}
					restarts += cs.RestartCount
				}
				status := string(pod.Status.Phase)
				for j := range pod.Status.ContainerStatuses {
					cs := &pod.Status.ContainerStatuses[j]
					if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
						status = cs.State.Waiting.Reason
					}
				}
				for j := range pod.Status.InitContainerStatuses {
					cs := &pod.Status.InitContainerStatuses[j]
					if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
						status = "Init:" + cs.State.Waiting.Reason
					} else if cs.State.Running != nil {
						completedInit := 0
						for k := range pod.Status.InitContainerStatuses {
							ics := &pod.Status.InitContainerStatuses[k]
							if ics.Ready || (ics.State.Terminated != nil && ics.State.Terminated.ExitCode == 0) {
								completedInit++
							}
						}
						status = fmt.Sprintf("Init:%d/%d", completedInit, len(pod.Spec.InitContainers))
					}
				}
				if pod.DeletionTimestamp != nil {
					status = "Terminating"
				}
				age := time.Since(pod.CreationTimestamp.Time)
				ageStr := ""
				switch {
				case age.Hours() >= 24:
					ageStr = fmt.Sprintf("%dd", int(age.Hours()/24))
				case age.Hours() >= 1:
					ageStr = fmt.Sprintf("%dh", int(age.Hours()))
				case age.Minutes() >= 1:
					ageStr = fmt.Sprintf("%dm", int(age.Minutes()))
				default:
					ageStr = fmt.Sprintf("%ds", int(age.Seconds()))
				}
				rows = append(rows, podRow{
					Name:     pod.Name,
					Ready:    fmt.Sprintf("%d/%d", readyCount, totalCount),
					Status:   status,
					Restarts: restarts,
					Age:      ageStr,
				})
			}
			// Only broadcast if changed
			currentJSON, _ := json.Marshal(rows)
			if string(currentJSON) != lastJSON {
				lastJSON = string(currentJSON)
				s.hub.BroadcastJSON("demo:pods", map[string]interface{}{
					"pods": rows,
				})
			}
		}
	}
}

func (s *Server) pickGPUNode(ctx context.Context) (string, error) {
	nodes, err := s.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "nvidia.com/gpu.present=true",
	})
	if err != nil {
		return "", err
	}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		for j := range node.Status.Conditions {
			cond := &node.Status.Conditions[j]
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				return node.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no ready GPU nodes found")
}

// waitForPodReady waits until all containers in the pod are ready.
func (s *Server) waitForPodReady(ctx context.Context, namespace, name string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timeout waiting for pod %s/%s to be ready", namespace, name)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pod, err := s.kubeClient.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				return fmt.Errorf("pod %s in terminal phase: %s", name, pod.Status.Phase)
			}
			// Check all containers ready
			if pod.Status.Phase == corev1.PodRunning && len(pod.Status.ContainerStatuses) > 0 {
				allReady := true
				for j := range pod.Status.ContainerStatuses {
					if !pod.Status.ContainerStatuses[j].Ready {
						allReady = false
						break
					}
				}
				if allReady {
					return nil
				}
			}
		}
	}
}

// persistDemoState writes the current demo state to the DB catalog.
func (s *Server) persistDemoState() {
	s.demo.mu.Lock()
	st := s.demo.state
	ckpts := make([]DemoCheckpoint, len(st.Checkpoints))
	copy(ckpts, st.Checkpoints)
	s.demo.mu.Unlock()

	ckptJSON, _ := json.Marshal(ckpts)
	var startedAt *string
	if st.StartedAt != nil {
		s := st.StartedAt.Format(time.RFC3339)
		startedAt = &s
	}

	if err := s.catalog.SaveDemoState(&db.DemoState{
		Phase:           string(st.Phase),
		WorkloadType:    st.WorkloadType,
		PodName:         st.PodName,
		NodeName:        st.NodeName,
		Message:         st.Message,
		Error:           st.Error,
		DeployDuration:  st.DeployDuration,
		CkptDuration:    st.CheckpointDuration,
		RestoreDuration: st.RestoreDuration,
		StartedAt:       startedAt,
		CheckpointsJSON: string(ckptJSON),
	}); err != nil {
		s.log.WithError(err).Warn("Failed to persist demo state")
	}
}

// LoadDemoState restores demo state from the DB catalog (called on startup).
func (s *Server) LoadDemoState() {
	saved, err := s.catalog.LoadDemoState()
	if err != nil {
		s.log.WithError(err).Warn("Failed to load demo state from DB")
		return
	}
	if saved == nil {
		return
	}

	s.demo.mu.Lock()
	defer s.demo.mu.Unlock()

	s.demo.state.Phase = DemoPhase(saved.Phase)
	s.demo.state.WorkloadType = saved.WorkloadType
	s.demo.state.PodName = saved.PodName
	s.demo.state.NodeName = saved.NodeName
	s.demo.state.Message = saved.Message
	s.demo.state.Error = saved.Error
	s.demo.state.DeployDuration = saved.DeployDuration
	s.demo.state.CheckpointDuration = saved.CkptDuration
	s.demo.state.RestoreDuration = saved.RestoreDuration

	if saved.StartedAt != nil {
		if t, err := time.Parse(time.RFC3339, *saved.StartedAt); err == nil {
			s.demo.state.StartedAt = &t
		}
	}

	var ckpts []DemoCheckpoint
	if err := json.Unmarshal([]byte(saved.CheckpointsJSON), &ckpts); err != nil {
		logrus.WithError(err).Warn("demo: decode saved checkpoints")
	}
	if ckpts == nil {
		ckpts = []DemoCheckpoint{}
	}
	s.demo.state.Checkpoints = ckpts

	s.log.WithField("phase", saved.Phase).Info("Restored demo state from DB")
}

// podStatusMessage returns a human-readable status for the demo UI.
func podStatusMessage(pod *corev1.Pod) string {
	// Check init containers
	totalInit := len(pod.Spec.InitContainers)
	if totalInit > 0 {
		completedInit := 0
		currentInit := ""
		for j := range pod.Status.InitContainerStatuses {
			cs := &pod.Status.InitContainerStatuses[j]
			switch {
			case cs.Ready || (cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0):
				completedInit++
			case cs.State.Running != nil:
				currentInit = cs.Name
			case cs.State.Waiting != nil:
				currentInit = cs.Name
			}
		}
		if completedInit < totalInit {
			if currentInit != "" {
				return fmt.Sprintf("Init: %d/%d (%s)", completedInit, totalInit, currentInit)
			}
			return fmt.Sprintf("Init: %d/%d", completedInit, totalInit)
		}
	}

	// Main containers
	if pod.Status.Phase == corev1.PodRunning {
		for j := range pod.Status.ContainerStatuses {
			if pod.Status.ContainerStatuses[j].Ready {
				return "Ready"
			}
		}
		return "Loading model..."
	}

	return string(pod.Status.Phase)
}
