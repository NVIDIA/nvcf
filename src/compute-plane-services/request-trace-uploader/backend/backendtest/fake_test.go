// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package backendtest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
)

var _ backend.Client = (*backendtest.Fake)(nil)

func writeFile(t *testing.T, content string) backend.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transport.jsonl.gz")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return backend.File{Path: path, Bytes: int64(len(content))}
}

func writeTransport(t *testing.T, content string) backend.SubmitRequest {
	t.Helper()
	return backend.SubmitRequest{Path: writeFile(t, content).Path}
}

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestFakeSubmitOutcomes(t *testing.T) {
	refused := errors.New("refused by test")
	cases := []struct {
		name       string
		script     []backendtest.SubmitStep
		wantID     bool
		wantErr    error
		wantJob    bool
		wantLanded bool
	}{
		{name: "unscripted submit is accepted and lands", wantID: true, wantJob: true, wantLanded: true},
		{name: "refuse returns the scripted error and makes no job", script: []backendtest.SubmitStep{{Outcome: backendtest.Refuse, Err: refused}}, wantErr: refused},
		{name: "refuse without an error returns ErrScripted", script: []backendtest.SubmitStep{{Outcome: backendtest.Refuse}}, wantErr: backendtest.ErrScripted},
		{name: "lost response makes a job the caller never hears of", script: []backendtest.SubmitStep{{Outcome: backendtest.LostResponse}}, wantErr: backendtest.ErrScripted, wantJob: true, wantLanded: true},
		{name: "accept with error returns both the id and the error", script: []backendtest.SubmitStep{{Outcome: backendtest.AcceptWithError, Err: refused}}, wantID: true, wantErr: refused, wantJob: true, wantLanded: true},
		{name: "a job whose script ends in failure does not land", script: []backendtest.SubmitStep{{Statuses: []backendtest.StatusStep{{Status: backend.StatusFailure}}}}, wantID: true, wantJob: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := backendtest.NewFake()
			fake.Script(tc.script...)
			request := writeTransport(t, "segment bytes")

			id, err := fake.Submit(context.Background(), request)

			if (id != "") != tc.wantID {
				t.Errorf("id = %q, want id returned = %v", id, tc.wantID)
			}
			if tc.wantErr == nil && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			jobs := fake.Jobs()
			if (len(jobs) == 1) != tc.wantJob || len(jobs) > 1 {
				t.Fatalf("jobs = %+v, want job created = %v", jobs, tc.wantJob)
			}
			if !tc.wantJob {
				return
			}
			job := jobs[0]
			if tc.wantID && job.ID != id {
				t.Errorf("job id = %q, want the returned id %q", job.ID, id)
			}
			if job.ID == "" {
				t.Error("job has no id")
			}
			want := []backendtest.JobFile{{Path: request.Path, SHA256: sha("segment bytes")}}
			if len(job.Files) != 1 || job.Files[0] != want[0] {
				t.Errorf("job files = %+v, want %+v", job.Files, want)
			}
			if job.Landed != tc.wantLanded {
				t.Errorf("landed = %v, want %v", job.Landed, tc.wantLanded)
			}
		})
	}
}

func TestFakeStatusScript(t *testing.T) {
	readFailed := errors.New("status read failed")
	reason := errors.New("rejected rows")
	pending := backendtest.StatusStep{Status: backend.StatusPending}
	success := backendtest.StatusStep{Status: backend.StatusSuccess}
	type reply struct {
		status backend.Status
		err    error
	}
	cases := []struct {
		name       string
		statuses   []backendtest.StatusStep
		want       []reply
		wantLanded bool
	}{
		{
			name:       "no script reports success",
			want:       []reply{{backend.StatusSuccess, nil}, {backend.StatusSuccess, nil}},
			wantLanded: true,
		},
		{
			name:       "steps play in order and the last repeats",
			statuses:   []backendtest.StatusStep{pending, pending, success},
			want:       []reply{{backend.StatusPending, nil}, {backend.StatusPending, nil}, {backend.StatusSuccess, nil}, {backend.StatusSuccess, nil}},
			wantLanded: true,
		},
		{
			name:     "stuck pending never finishes",
			statuses: []backendtest.StatusStep{pending},
			want:     []reply{{backend.StatusPending, nil}, {backend.StatusPending, nil}, {backend.StatusPending, nil}},
		},
		{
			name:       "a step can fail the status read",
			statuses:   []backendtest.StatusStep{{Err: readFailed}, success},
			want:       []reply{{"", readFailed}, {backend.StatusSuccess, nil}},
			wantLanded: true,
		},
		{
			name:     "a failure carries its reason",
			statuses: []backendtest.StatusStep{{Status: backend.StatusFailure, Err: reason}},
			want:     []reply{{backend.StatusFailure, reason}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := backendtest.NewFake()
			fake.Script(backendtest.SubmitStep{Statuses: tc.statuses})
			id, err := fake.Submit(context.Background(), writeTransport(t, "x"))
			if err != nil {
				t.Fatalf("submit: %v", err)
			}

			for i, want := range tc.want {
				got, err := fake.Status(context.Background(), id)
				if got != want.status || !errors.Is(err, want.err) || (want.err == nil && err != nil) {
					t.Errorf("call %d = %q, %v; want %q, %v", i, got, err, want.status, want.err)
				}
			}
			jobs := fake.Jobs()
			if len(jobs) != 1 {
				t.Fatalf("jobs = %+v, want one", jobs)
			}
			job := jobs[0]
			if job.StatusCalls != len(tc.want) {
				t.Errorf("status calls = %d, want %d", job.StatusCalls, len(tc.want))
			}
			if job.Landed != tc.wantLanded {
				t.Errorf("landed = %v, want %v", job.Landed, tc.wantLanded)
			}
		})
	}

	t.Run("an id never issued is gone", func(t *testing.T) {
		got, err := backendtest.NewFake().Status(context.Background(), "job-never-made")
		if got != backend.StatusGone || err != nil {
			t.Errorf("status = %q, %v; want %q, nil", got, err, backend.StatusGone)
		}
	})
}

func TestFakeSubmitsEveryFileOfAJob(t *testing.T) {
	scripted := errors.New("scripted for the first request that fits")
	fake := backendtest.NewFake()
	fake.SetCapabilities(backend.Capabilities{
		MaxBatchFiles: 2,
		Destinations:  []backend.Destination{"primary", "secondary"},
		Exports:       true,
	})
	fake.Script(backendtest.SubmitStep{Outcome: backendtest.AcceptWithError, Err: scripted})
	a, b, c := writeFile(t, "a"), writeFile(t, "b"), writeFile(t, "c")

	rejected := []backend.SubmitRequest{
		{Destination: "primary", Files: []backend.File{a, b, c}},
		{Destination: "elsewhere", Files: []backend.File{a}},
		{Destination: "primary"},
	}
	for _, request := range rejected {
		if id, err := fake.Submit(context.Background(), request); id != "" || err == nil || errors.Is(err, scripted) {
			t.Errorf("submit %+v = %q, %v; want a capability error that leaves the script alone", request, id, err)
		}
	}

	id, err := fake.Submit(context.Background(), backend.SubmitRequest{Destination: "secondary", Files: []backend.File{a, b}})
	if id == "" || !errors.Is(err, scripted) {
		t.Fatalf("submit = %q, %v; want an id and the scripted error", id, err)
	}
	jobs := fake.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("jobs = %+v, want one", jobs)
	}
	want := []backendtest.JobFile{{Path: a.Path, SHA256: sha("a")}, {Path: b.Path, SHA256: sha("b")}}
	got := jobs[0]
	if got.Destination != "secondary" || len(got.Files) != 2 || got.Files[0] != want[0] || got.Files[1] != want[1] {
		t.Errorf("job = %+v, want destination secondary and files %+v", got, want)
	}
}

func TestFakeCountsHiddenDuplicates(t *testing.T) {
	lost := backendtest.SubmitStep{Outcome: backendtest.LostResponse}
	cases := []struct {
		name     string
		script   []backendtest.SubmitStep
		requests func(a, b backend.File) []backend.SubmitRequest
		want     int
	}{
		{
			name:   "resubmit after a lost response lands twice",
			script: []backendtest.SubmitStep{lost},
			requests: func(a, _ backend.File) []backend.SubmitRequest {
				return []backend.SubmitRequest{{Files: []backend.File{a}}, {Files: []backend.File{a}}}
			},
			want: 1,
		},
		{
			name:   "resubmit after a refusal lands once",
			script: []backendtest.SubmitStep{{Outcome: backendtest.Refuse}},
			requests: func(a, _ backend.File) []backend.SubmitRequest {
				return []backend.SubmitRequest{{Files: []backend.File{a}}, {Files: []backend.File{a}}}
			},
		},
		{
			name:   "resubmit after a failed job lands once",
			script: []backendtest.SubmitStep{{Statuses: []backendtest.StatusStep{{Status: backend.StatusFailure}}}},
			requests: func(a, _ backend.File) []backend.SubmitRequest {
				return []backend.SubmitRequest{{Files: []backend.File{a}}, {Files: []backend.File{a}}}
			},
		},
		{
			name:   "a resubmitted batch duplicates every file",
			script: []backendtest.SubmitStep{lost},
			requests: func(a, b backend.File) []backend.SubmitRequest {
				return []backend.SubmitRequest{{Files: []backend.File{a, b}}, {Files: []backend.File{a, b}}}
			},
			want: 2,
		},
		{
			name: "the same file in two destinations is not a duplicate",
			requests: func(a, _ backend.File) []backend.SubmitRequest {
				return []backend.SubmitRequest{{Destination: "primary", Files: []backend.File{a}}, {Destination: "secondary", Files: []backend.File{a}}}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := backendtest.NewFake()
			fake.SetCapabilities(backend.Capabilities{
				MaxBatchFiles: 2,
				Destinations:  []backend.Destination{"", "primary", "secondary"},
				Exports:       true,
			})
			fake.Script(tc.script...)
			a, b := writeFile(t, "same segment"), writeFile(t, "another segment")
			for _, request := range tc.requests(a, b) {
				_, _ = fake.Submit(context.Background(), request)
			}

			if got := fake.Duplicates(); got != tc.want {
				t.Errorf("duplicates = %d, want %d (jobs %+v)", got, tc.want, fake.Jobs())
			}
		})
	}
}

func TestFakeDelayHonorsContext(t *testing.T) {
	slow := []backendtest.StatusStep{{Status: backend.StatusSuccess, Delay: time.Hour}}

	t.Run("submit", func(t *testing.T) {
		fake := backendtest.NewFake()
		fake.Script(backendtest.SubmitStep{Delay: time.Hour})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		id, err := fake.Submit(ctx, writeTransport(t, "x"))
		if id != "" || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("submit = %q, %v; want no id and the deadline error", id, err)
		}
		if len(fake.Jobs()) != 0 {
			t.Errorf("a submit that never reached the backend made a job")
		}
	})

	t.Run("status", func(t *testing.T) {
		fake := backendtest.NewFake()
		fake.Script(backendtest.SubmitStep{Statuses: slow})
		id, err := fake.Submit(context.Background(), writeTransport(t, "x"))
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		if _, err := fake.Status(ctx, id); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("status err = %v, want the deadline error", err)
		}
	})
}

func TestFakeMissingFileKeepsTheScript(t *testing.T) {
	scripted := errors.New("scripted for the second call")
	fake := backendtest.NewFake()
	fake.Script(backendtest.SubmitStep{Outcome: backendtest.Refuse, Err: scripted})

	_, err := fake.Submit(context.Background(), backend.SubmitRequest{Path: filepath.Join(t.TempDir(), "gone.jsonl.gz")})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file err = %v, want not exist", err)
	}
	_, err = fake.Submit(context.Background(), writeTransport(t, "x"))
	if !errors.Is(err, scripted) {
		t.Errorf("second submit err = %v, want the scripted step it was saved for", err)
	}
	if got := fake.SubmitCalls(); got != 2 {
		t.Errorf("submit calls = %d, want 2", got)
	}
}

func TestFakeKeepsItsOwnCopies(t *testing.T) {
	fake := backendtest.NewFake()
	statuses := []backendtest.StatusStep{{Status: backend.StatusPending}}
	fake.Script(backendtest.SubmitStep{Statuses: statuses})
	formats := []backend.Format{backend.FormatGzipJSONL}
	fake.SetCapabilities(backend.Capabilities{AcceptedFormats: formats, Exports: true})

	statuses[0].Status = backend.StatusSuccess
	formats[0] = backend.FormatParquet
	fake.Capabilities().AcceptedFormats[0] = backend.FormatParquet

	id, err := fake.Submit(context.Background(), writeTransport(t, "x"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got, _ := fake.Status(context.Background(), id); got != backend.StatusPending {
		t.Errorf("status = %q, want the pending step as scripted before the caller changed its slice", got)
	}
	if got := fake.Capabilities().AcceptedFormats[0]; got != backend.FormatGzipJSONL {
		t.Errorf("accepted format = %q, want the value set before callers changed their slices", got)
	}
}

func TestFakeConcurrentUse(t *testing.T) {
	const workers = 16
	fake := backendtest.NewFake()
	request := writeTransport(t, "x")

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			id, err := fake.Submit(context.Background(), request)
			if err != nil {
				t.Errorf("submit: %v", err)
				return
			}
			if _, err := fake.Status(context.Background(), id); err != nil {
				t.Errorf("status: %v", err)
			}
		})
	}
	wg.Wait()

	ids := map[string]bool{}
	for _, job := range fake.Jobs() {
		ids[job.ID] = true
	}
	if len(ids) != workers {
		t.Errorf("distinct job ids = %d, want %d", len(ids), workers)
	}
}

func TestFakeCapabilities(t *testing.T) {
	fake := backendtest.NewFake()
	got := fake.Capabilities()
	if got.TerminalOutcomeSync || got.ResubmitSafe || !got.Exports {
		t.Errorf("default capabilities = %+v, want an exporting asynchronous backend that is not resubmit safe", got)
	}

	fake.SetCapabilities(backend.Capabilities{ResubmitSafe: true})
	if !fake.Capabilities().ResubmitSafe {
		t.Error("SetCapabilities did not take effect")
	}
}
