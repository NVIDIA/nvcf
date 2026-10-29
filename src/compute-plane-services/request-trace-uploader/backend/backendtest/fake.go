// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package backendtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

// ErrScripted is the error a failing SubmitStep returns when it names none.
var ErrScripted = errors.New("backendtest: scripted failure")

// Outcome is what the backend does with one Submit call.
type Outcome int

const (
	// Accept creates the job and returns its id. It is the zero value.
	Accept Outcome = iota
	// Refuse creates no job and returns the step's error.
	Refuse
	// LostResponse creates the job but returns no id and the step's error:
	// the upload committed and the caller never heard. Resubmitting the same
	// files then lands them twice.
	LostResponse
	// AcceptWithError creates the job and returns both its id and the step's
	// error. The caller must keep the id before looking at the error.
	AcceptWithError
)

// SubmitStep scripts one Submit call.
type SubmitStep struct {
	Outcome Outcome
	// Err is returned for every outcome except Accept. Nil means ErrScripted.
	Err error
	// Delay holds the call before the backend acts, honoring the context. A
	// call canceled during the delay creates no job.
	Delay time.Duration
	// Statuses scripts Status calls for the job this step creates. Each call
	// plays the next step and the last step repeats, so a lone pending step
	// is a job stuck forever. Empty means one success step.
	Statuses []StatusStep
}

// StatusStep scripts one Status call. Status and Err are returned as given,
// so a step can be a read failure (no Status, an error) or a failure with its
// reason (StatusFailure and an error).
type StatusStep struct {
	Status backend.Status
	Err    error
	// Delay holds the call, honoring the context.
	Delay time.Duration
}

// Job is one job the backend created, including jobs whose id the caller
// never received.
type Job struct {
	ID          string
	Destination backend.Destination
	Files       []JobFile
	// Landed reports that the job's last status step is a success with no
	// error, so its files reach the destination whether or not anyone polls.
	Landed      bool
	StatusCalls int
}

// JobFile is one submitted file and the SHA-256 of its content when it was
// submitted.
type JobFile struct {
	Path   string
	SHA256 string
}

// Fake is an in-memory asynchronous backend.Client whose outcomes a
// test scripts in advance with Script. It rejects requests that do not fit
// its capabilities, and reads every submitted file to fingerprint it, so a
// resubmit that lands content twice in one destination shows in Duplicates.
// It is safe for concurrent use and registers nowhere; tests pass it to the
// code under test directly.
type Fake struct {
	mu          sync.Mutex
	caps        backend.Capabilities
	script      []SubmitStep
	jobs        []*fakeJob
	byID        map[string]*fakeJob
	submitCalls int
}

type fakeJob struct {
	job      Job
	statuses []StatusStep
}

// NewFake returns a backend that accepts every unscripted submit and
// reports success on the first poll. Its capabilities are those of an
// asynchronous exporting backend that takes one file per job, has one
// destination, and is not safe to resubmit to.
func NewFake() *Fake {
	return &Fake{
		caps: backend.Capabilities{
			AcceptedFormats: []backend.Format{backend.FormatGzipJSONL},
			Exports:         true,
		},
		byID: map[string]*fakeJob{},
	}
}

// SetCapabilities replaces what Capabilities reports and what Submit
// enforces.
func (f *Fake) SetCapabilities(caps backend.Capabilities) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = cloneCapabilities(caps)
}

// Script queues steps for the next Submit calls, in order. Calls past the end
// of the script behave as an Accept step with no status script.
func (f *Fake) Script(steps ...SubmitStep) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, step := range steps {
		step.Statuses = slices.Clone(step.Statuses)
		f.script = append(f.script, step)
	}
}

// Submit plays the next scripted step for request. A request that does not
// fit the capabilities, or a file that cannot be read, fails the call without
// using a step.
func (f *Fake) Submit(ctx context.Context, request backend.SubmitRequest) (string, error) {
	f.mu.Lock()
	f.submitCalls++
	caps := f.caps
	f.mu.Unlock()

	if err := caps.Check(request); err != nil {
		return "", fmt.Errorf("backendtest: %w", err)
	}
	var files []JobFile
	for _, transport := range request.Transports() {
		content, err := os.ReadFile(transport.Path)
		if err != nil {
			return "", fmt.Errorf("backendtest: read submitted file: %w", err)
		}
		sum := sha256.Sum256(content)
		files = append(files, JobFile{Path: transport.Path, SHA256: hex.EncodeToString(sum[:])})
	}

	f.mu.Lock()
	var step SubmitStep
	if len(f.script) > 0 {
		step = f.script[0]
		f.script = f.script[1:]
	}
	f.mu.Unlock()

	if err := wait(ctx, step.Delay); err != nil {
		return "", err
	}

	stepErr := step.Err
	if stepErr == nil {
		stepErr = ErrScripted
	}
	if step.Outcome == Refuse {
		return "", stepErr
	}

	statuses := step.Statuses
	if len(statuses) == 0 {
		statuses = []StatusStep{{Status: backend.StatusSuccess}}
	}
	last := statuses[len(statuses)-1]

	f.mu.Lock()
	job := &fakeJob{
		job: Job{
			ID:          fmt.Sprintf("job-%06d", len(f.jobs)+1),
			Destination: request.Destination,
			Files:       files,
			Landed:      last.Err == nil && last.Status == backend.StatusSuccess,
		},
		statuses: statuses,
	}
	f.jobs = append(f.jobs, job)
	f.byID[job.job.ID] = job
	f.mu.Unlock()

	switch step.Outcome {
	case LostResponse:
		return "", stepErr
	case AcceptWithError:
		return job.job.ID, stepErr
	default:
		return job.job.ID, nil
	}
}

// Status plays the job's next status step. An id the backend never issued
// reads as backend.StatusGone.
func (f *Fake) Status(ctx context.Context, id string) (backend.Status, error) {
	f.mu.Lock()
	job, ok := f.byID[id]
	if !ok {
		f.mu.Unlock()
		return backend.StatusGone, nil
	}
	step := job.statuses[min(job.job.StatusCalls, len(job.statuses)-1)]
	job.job.StatusCalls++
	f.mu.Unlock()

	if err := wait(ctx, step.Delay); err != nil {
		return "", err
	}
	return step.Status, step.Err
}

// Capabilities reports the configured capabilities.
func (f *Fake) Capabilities() backend.Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneCapabilities(f.caps)
}

// Jobs returns every job created so far, in creation order.
func (f *Fake) Jobs() []Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := make([]Job, 0, len(f.jobs))
	for _, job := range f.jobs {
		copied := job.job
		copied.Files = slices.Clone(copied.Files)
		jobs = append(jobs, copied)
	}
	return jobs
}

// Duplicates counts files in landed jobs whose content had already landed in
// the same destination in an earlier job.
func (f *Fake) Duplicates() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	type key struct {
		destination backend.Destination
		sha256      string
	}
	seen := map[key]bool{}
	duplicates := 0
	for _, job := range f.jobs {
		if !job.job.Landed {
			continue
		}
		for _, file := range job.job.Files {
			k := key{job.job.Destination, file.SHA256}
			if seen[k] {
				duplicates++
			}
			seen[k] = true
		}
	}
	return duplicates
}

// SubmitCalls counts every Submit call, including ones that failed.
func (f *Fake) SubmitCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submitCalls
}

func cloneCapabilities(caps backend.Capabilities) backend.Capabilities {
	caps.AcceptedFormats = slices.Clone(caps.AcceptedFormats)
	caps.Destinations = slices.Clone(caps.Destinations)
	return caps
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
