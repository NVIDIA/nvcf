// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A dump of a pod's container stops it: a GPU suspend releases its GPU
// memory, CRIU locks its network and seizes its tasks. The agent settles
// the source when the dump ends, or reports it unusable to the caller,
// which replaces it. An agent that stops mid-dump (a rollout, a crash)
// does neither, and the pod may read Ready while it serves nothing.
//
// So each dump leaves a marker on the node, in the agent's checkpoint
// directory, until it ends; and so does each restore, which changes its
// placeholder from the CRIU restore until the GPU resume. A marker that
// outlives the agent's run names a pod the agent may have left stopped or
// half restored: the next run deletes it, so its controller replaces it. A group capture also recovers through its
// record (criu_group_recover.go); the marker covers each member's own part
// and a single pod's capture.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// captureMarker names the pod a dump in progress stopped.
type captureMarker struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	UID       string `json:"uid"`
}

// validPodUID is the shape of a pod uid, which names the marker's file.
var validPodUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (a *Agent) captureMarkerDir() string { return filepath.Join(a.config.CheckpointDir, ".capturing") }

// markCapturing records that a dump is about to stop pod m. The returned
// func removes the record, once the dump has ended.
func (a *Agent) markCapturing(m captureMarker) (func(), error) {
	if !validPodUID.MatchString(m.UID) {
		return nil, fmt.Errorf("capture marker: invalid pod uid %q", m.UID)
	}
	if err := os.MkdirAll(a.captureMarkerDir(), 0o755); err != nil {
		return nil, fmt.Errorf("capture marker: %w", err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	p := filepath.Join(a.captureMarkerDir(), m.UID+".json")
	if err := os.WriteFile(p, b, 0o644); err != nil { //nolint:gosec // a pod's name, read back by the agent
		return nil, fmt.Errorf("capture marker: %w", err)
	}
	return func() { _ = os.Remove(p) }, nil
}

// recoverInterruptedSources deletes the pods an earlier run of the agent
// stopped for a dump it did not finish. The delete is bound to the pod's
// uid: a pod since replaced under the same name is left alone.
func (a *Agent) recoverInterruptedSources(ctx context.Context, log *logrus.Entry) {
	entries, err := os.ReadDir(a.captureMarkerDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(a.captureMarkerDir(), e.Name())
		var m captureMarker
		b, rerr := os.ReadFile(p)
		if rerr != nil || json.Unmarshal(b, &m) != nil || !validPodUID.MatchString(m.UID) || m.UID+".json" != e.Name() {
			log.WithField("marker", e.Name()).Warn("capture marker: unreadable; removing it")
			_ = os.Remove(p)
			continue
		}
		if a.kubeClient == nil {
			continue
		}
		uid := types.UID(m.UID)
		err := a.kubeClient.CoreV1().Pods(m.Namespace).Delete(ctx, m.Pod, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		fl := log.WithField("pod", m.Namespace+"/"+m.Pod)
		switch {
		case err == nil:
			fl.Error("a capture was interrupted by this agent's restart and may have left the workload stopped; deleted its pod, which its controller replaces")
		case apierrors.IsNotFound(err) || apierrors.IsConflict(err):
			// Gone, or replaced already: nothing left to recover.
		default:
			fl.WithError(err).Warn("capture marker: cannot delete the pod of an interrupted capture; trying again at the next start")
			continue
		}
		_ = os.Remove(p)
	}
}
