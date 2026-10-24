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
	"time"

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
// stopped for a dump (or a restore) it did not finish, retrying until each
// delete goes through. The delete is bound to the pod's uid: a pod since
// replaced under the same name is left alone.
func (a *Agent) recoverInterruptedSources(ctx context.Context, log *logrus.Entry) {
	_ = retryUntil(ctx, recoverRetry.first, recoverRetry.max, recoverRetry.total, func() (bool, error) {
		return a.recoverInterruptedSourcesOnce(ctx, log), nil
	})
}

// recoverInterruptedSourcesOnce is one pass of recoverInterruptedSources;
// it reports whether every marker was handled.
func (a *Agent) recoverInterruptedSourcesOnce(ctx context.Context, log *logrus.Entry) bool {
	entries, err := os.ReadDir(a.captureMarkerDir())
	if err != nil {
		return true
	}
	done := true
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
			done = false
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
			fl.WithError(err).Warn("capture marker: cannot delete the pod of an interrupted capture yet; retrying")
			done = false
			continue
		}
		_ = os.Remove(p)
	}
	return done
}

// recoverRetry paces the recovery of what an earlier run left: it goes on
// until done, for as long as the agent runs.
var recoverRetry = struct{ first, max, total time.Duration }{time.Second, 30 * time.Second, 24 * time.Hour}

// deleteRetry bounds the retries of a pod delete that failed for a reason
// that may pass (the API server unreachable for a moment).
var deleteRetry = struct{ first, max, total time.Duration }{time.Second, 15 * time.Second, 2 * time.Minute}

// deletePodRetrying deletes pod ns/name, bound to uid when it is known, and
// retries a failure that may pass. A pod gone, or replaced under the same
// name, is done.
func (a *Agent) deletePodRetrying(ctx context.Context, ns, name string, uid types.UID) error {
	opts := metav1.DeleteOptions{}
	if uid != "" {
		opts.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	return retryUntil(ctx, deleteRetry.first, deleteRetry.max, deleteRetry.total, func() (bool, error) {
		err := a.kubeClient.CoreV1().Pods(ns).Delete(ctx, name, opts)
		if err == nil || apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return true, nil
		}
		return false, err
	})
}

// retryUntil runs step until it is done, waiting first, then twice as long
// each time up to most, for at most total or until ctx ends. It returns
// the last step's error when it never got done.
func retryUntil(ctx context.Context, first, most, total time.Duration, step func() (bool, error)) error {
	deadline := time.Now().Add(total)
	for wait := first; ; wait = min(2*wait, most) {
		done, err := step()
		if done {
			return nil
		}
		if time.Now().Add(wait).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}
