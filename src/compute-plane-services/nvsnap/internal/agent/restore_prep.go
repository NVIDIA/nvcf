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

// restore_prep.go implements the async overlay-prep
// job manager that backs the nvsnap-mount-prep init container
// (docs/proposals/init-container-mount-prep.md, nvsnap#202).
//
// Architectural note: the webhook used to call OverlayPreparer.PrepareOverlay
// SYNCHRONOUSLY during K8s admission, which forced hundreds of
// mount(2) syscalls into the 10-30 s mutating-webhook budget. For
// workloads with many extract paths (DeepSeek-V4-Flash captured 355)
// the webhook would blow the apiserver TCP timeout, fall back to
// failurePolicy: Ignore, and admit the pod with NO patches —
// silently degrading to cold start.
//
// New model: webhook returns patches immediately (init container +
// expected volumeMounts). The init container in the pod calls
// POST /v1/restore/prep, polls GET /v1/restore/prep/{podUID} until
// state == "ready", then exits. Mount work runs here in goroutines
// with whatever wall-clock it needs — no K8s timeout.

package agent

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/checkpointstore"
)

// PrepState is the lifecycle of one per-pod overlay-prep job.
type PrepState string

const (
	// PrepStatePreparing — at least one mount is still in flight.
	PrepStatePreparing PrepState = "preparing"
	// PrepStateReady — every mount in Total succeeded. kubelet can
	// now safely bind the hostPath volumes into the main container.
	PrepStateReady PrepState = "ready"
	// PrepStateFailed — one or more mounts failed and the prep is
	// non-recoverable. Init container should exit non-zero so the
	// pod surfaces Init:Error.
	PrepStateFailed PrepState = "failed"
)

// PrepRequest is the JSON body of POST /v1/restore/prep.
//
// The webhook computes Mounts (the list of captured volumes that need
// overlays) from the manifest CM and passes the resolved list here.
// We don't re-derive on the agent side because (a) the webhook is the
// source of truth for "what got patched into the pod spec" and (b)
// passing the list avoids a races where the manifest CM changes
// between admission and prep.
type PrepRequest struct {
	PodUID      string `json:"podUID"`
	CaptureHash string `json:"captureHash"`
	// CaptureNode is the node that holds the captured tree on local
	// disk. When this agent is not on that node, each Mount is
	// peer-routed via the existing PrepareOverlay → prepareOverlayViaPeer
	// fallback. Empty = local-only.
	CaptureNode string                       `json:"captureNode,omitempty"`
	Mounts      []checkpointstore.VolumeMeta `json:"mounts"`
}

// PrepStatus is the JSON body of GET /v1/restore/prep/{podUID}.
//
// Init container polls until State is Ready or Failed. Counts let
// the init container log nice progress (e.g. "37/355 mounts ready")
// for kubectl describe pod / kubectl logs visibility.
type PrepStatus struct {
	JobID       string              `json:"jobID"`
	State       PrepState           `json:"state"`
	Total       int                 `json:"total"`
	Prepared    int                 `json:"prepared"`
	Failures    []PrepStatusFailure `json:"failures,omitempty"`
	StartedAt   time.Time           `json:"startedAt"`
	CompletedAt *time.Time          `json:"completedAt,omitempty"`
}

// PrepStatusFailure describes one mount that failed during prep.
// The init container's stderr surfaces these so operators can see
// the exact path + error in kubectl logs.
type PrepStatusFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// prepJob is the in-memory state for one in-flight or completed job.
// Protected by its own mutex; the outer PrepJobManager.jobs sync.Map
// handles cross-pod concurrency.
type prepJob struct {
	mu          sync.Mutex
	id          string
	state       PrepState
	total       int
	prepared    int
	failures    []PrepStatusFailure
	startedAt   time.Time
	completedAt *time.Time
}

func (j *prepJob) snapshot() PrepStatus {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := PrepStatus{
		JobID:     j.id,
		State:     j.state,
		Total:     j.total,
		Prepared:  j.prepared,
		StartedAt: j.startedAt,
	}
	if j.completedAt != nil {
		t := *j.completedAt
		out.CompletedAt = &t
	}
	if len(j.failures) > 0 {
		out.Failures = append([]PrepStatusFailure(nil), j.failures...)
	}
	return out
}

// preparer is the minimal Agent surface PrepJobManager calls.
// Defined as an interface so the manager is unit-testable without
// a live Agent (the prep_test.go file uses an in-memory fake).
type preparer interface {
	PrepareOverlay(podUID, captureHash string, vol checkpointstore.VolumeMeta, targetNode string) (string, error)
	CleanupOverlayForPod(podUID string) error
}

// PrepJobManager owns the async overlay-prep jobs. One job per
// podUID; second-arrival on the same podUID returns the in-flight
// job (idempotent — the init container POSTs every restart and we
// can't have the second POST kick a parallel job).
type PrepJobManager struct {
	prep          preparer
	log           logrus.FieldLogger
	jobs          sync.Map // podUID → *prepJob
	workersPerJob int      // bounded fan-out per job; default 16
}

// NewPrepJobManager wires the manager up. workersPerJob caps the
// parallelism inside a single job — same shape as the
// MR !66 sync.WaitGroup pool that the webhook used to use, just
// moved off the admission path.
func NewPrepJobManager(prep preparer, log logrus.FieldLogger) *PrepJobManager {
	if log == nil {
		log = logrus.New()
	}
	return &PrepJobManager{prep: prep, log: log, workersPerJob: 16}
}

// Start kicks off (or no-ops on) the prep job for req.PodUID.
//
// Returns the status of the job (PrepStatePreparing on first call,
// or whatever the in-flight job is currently at on a re-POST).
//
// Idempotency: the init container POSTs on every restart (or the
// webhook may have already POSTed via a peer route). Two concurrent
// POSTs for the same podUID must NOT spawn two prep goroutines —
// they'd race on the same merged/upper/work paths inside
// OverlayManager. We use sync.Map.LoadOrStore for that guarantee.
func (m *PrepJobManager) Start(ctx context.Context, req PrepRequest) PrepStatus {
	job := &prepJob{
		id:        req.PodUID + "@" + checkpointstore.ShortHash(req.CaptureHash),
		state:     PrepStatePreparing,
		total:     len(req.Mounts),
		startedAt: time.Now().UTC(),
	}
	existing, loaded := m.jobs.LoadOrStore(req.PodUID, job)
	if loaded {
		// Second POST for the same pod. Return the current
		// state without starting another goroutine.
		return existing.(*prepJob).snapshot()
	}

	// Empty Mounts is a valid case (manifest had no rootfs-extract
	// paths AND no user-data volumes) — mark Ready immediately so
	// the init container completes without polling.
	if len(req.Mounts) == 0 {
		t := time.Now().UTC()
		job.mu.Lock()
		job.state = PrepStateReady
		job.completedAt = &t
		job.mu.Unlock()
		return job.snapshot()
	}

	go m.runJob(req, job)
	return job.snapshot()
}

// runJob fans Mounts across m.workersPerJob goroutines and updates
// the job state as each one completes. Runs in its own goroutine
// (spawned by Start) — no caller waits on this.
func (m *PrepJobManager) runJob(req PrepRequest, job *prepJob) {
	sem := make(chan struct{}, m.workersPerJob)
	var wg sync.WaitGroup
	log := m.log.WithFields(logrus.Fields{
		"podUID": req.PodUID,
		"hash":   checkpointstore.ShortHash(req.CaptureHash),
		"total":  len(req.Mounts),
	})
	log.Info("restore-prep job started")

	for i := range req.Mounts {
		vol := req.Mounts[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := m.prep.PrepareOverlay(req.PodUID, req.CaptureHash, vol, req.CaptureNode)
			job.mu.Lock()
			if err != nil {
				job.failures = append(job.failures, PrepStatusFailure{
					Path:  vol.MountPath,
					Error: err.Error(),
				})
			} else {
				job.prepared++
			}
			job.mu.Unlock()
		}()
	}
	wg.Wait()

	t := time.Now().UTC()
	job.mu.Lock()
	job.completedAt = &t
	if len(job.failures) > 0 {
		job.state = PrepStateFailed
	} else {
		job.state = PrepStateReady
	}
	final := job.state
	prepared := job.prepared
	failed := len(job.failures)
	job.mu.Unlock()

	log.WithFields(logrus.Fields{
		"state":    final,
		"prepared": prepared,
		"failed":   failed,
		"duration": time.Since(job.startedAt).Round(time.Millisecond),
	}).Info("restore-prep job finished")
}

// Status returns the current state of the job for podUID. Returns
// (PrepStatus{}, false) if no job exists for this podUID — the init
// container should treat that as "not started yet, retry" rather
// than as a hard failure.
func (m *PrepJobManager) Status(podUID string) (PrepStatus, bool) {
	v, ok := m.jobs.Load(podUID)
	if !ok {
		return PrepStatus{}, false
	}
	return v.(*prepJob).snapshot(), true
}

// Cleanup removes the job's tracking entry AND tears down the
// per-pod OverlayFS scratch. Called by the pod-DELETE watcher
// (existing handleOverlayPodDelete) so prep state doesn't leak after
// the pod's gone.
func (m *PrepJobManager) Cleanup(podUID string) error {
	m.jobs.Delete(podUID)
	if m.prep != nil {
		return m.prep.CleanupOverlayForPod(podUID)
	}
	return nil
}

// PendingJobs returns a snapshot of every podUID currently tracked.
// Used by the agent's startup sweep to surface jobs that were
// in-flight when the agent crashed — those are tombstoned to
// PrepStateFailed so the init container doesn't poll forever
// against an agent that lost its goroutine. Caller is expected to
// have re-mounted the OverlayManager's existing state before
// inspecting jobs.
func (m *PrepJobManager) PendingJobs() []string {
	var out []string
	m.jobs.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	return out
}
