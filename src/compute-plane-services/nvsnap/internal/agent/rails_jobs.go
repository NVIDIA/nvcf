// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Fetches run inside the node's rails pod. A rails pod holds every RDMA NIC
// of its node, so a node with a serve pod could not also start a fetch pod:
// a node that had to send one checkpoint and receive another (an instance
// restored onto its own nodes with its ranks swapped) waited out the
// fetch pod's start and copied over HTTP. bedrock-peer processes share the
// NICs, so each node runs one rails pod: it serves the node's checkpoint
// directory, and runs the node's fetches beside the server.
//
// The agent hands a fetch to the pod through files in the checkpoint
// directory, which both mount: <dir>/.rails-jobs/<job>.args holds the
// fetch's arguments, one per line. The pod's loop renames it to .run,
// starts bedrock-peer, writes its pid to .pid, its stdout (the --json line)
// to .out and its exit code to .exit. A .cancel file stops it. No exec into
// the pod, no permission beyond creating the pod.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// railsJobsDir is the jobs directory under the checkpoint directory.
const railsJobsDir = ".rails-jobs"

// railsJobPoll is how often the agent looks for a job's result, and
// railsJobPodCheck how often it checks the pod running it is still there.
var (
	railsJobPoll     = time.Second
	railsJobPodCheck = 5 * time.Second
)

// railsPodScript is the rails pod's command: bedrock-peer serve in the
// background ("$0" "$@"), and the loop that runs the jobs in jobsDir (the
// pod's path) until it exits.
func railsPodScript(jobsDir string) string {
	return `J=` + jobsDir + `
mkdir -p "$J" || exit 1
ulimit -l unlimited 2>/dev/null
"$0" "$@" &
S=$!
trap 'kill $S 2>/dev/null' TERM INT
while kill -0 $S 2>/dev/null; do
  for a in "$J"/*.args; do
    [ -e "$a" ] || continue
    j=${a%.args}
    mv "$a" "$j.run" || continue
    (
      IFS='
'
      set -f
      "$0" $(cat "$j.run") >"$j.out" 2>"$j.log" &
      echo $! >"$j.pid"
      wait $!
      echo $? >"$j.code"
      mv "$j.code" "$j.exit"
    ) &
  done
  for c in "$J"/*.cancel; do
    [ -e "$c" ] || continue
    j=${c%.cancel}
    [ -e "$j.pid" ] || continue
    kill "$(cat "$j.pid")" 2>/dev/null
    mv "$c" "$j.cancelled"
  done
  sleep 0.2
done
wait $S`
}

// railsJob is one fetch handed to this node's rails pod.
type railsJob struct {
	dir string // the agent's path of the jobs directory
	id  string
}

func (j railsJob) path(ext string) string { return filepath.Join(j.dir, j.id+ext) }

// submitRailsJob hands the pod a fetch with args. The arguments file is
// written beside its final name and renamed into place, so the pod never
// reads it half written.
func (a *Agent) submitRailsJob(checkpointID string, args []string) (railsJob, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return railsJob{}, err
	}
	j := railsJob{dir: filepath.Join(a.config.CheckpointDir, railsJobsDir), id: checkpointID + "-" + hex.EncodeToString(b[:])}
	if err := os.MkdirAll(j.dir, 0o700); err != nil {
		return railsJob{}, err
	}
	for _, s := range args {
		if s == "" || strings.ContainsAny(s, "\n\r") {
			return railsJob{}, fmt.Errorf("rails job argument %q", s)
		}
	}
	tmp := j.path(".tmp")
	if err := os.WriteFile(tmp, []byte(strings.Join(args, "\n")+"\n"), 0o600); err != nil {
		return railsJob{}, err
	}
	if err := os.Rename(tmp, j.path(".args")); err != nil {
		_ = os.Remove(tmp)
		return railsJob{}, err
	}
	return j, nil
}

// removeRailsJob removes a job's files. A job still running is asked to
// stop first.
func (j railsJob) remove() {
	for _, ext := range []string{".tmp", ".args", ".run", ".pid", ".out", ".log", ".code", ".exit", ".cancel", ".cancelled"} {
		_ = os.Remove(j.path(ext))
	}
}

// errRailsPodGone marks a fetch whose rails pod ended or was deleted
// before it finished.
var errRailsPodGone = errors.New("the rails pod running the fetch is gone")

// waitRailsJob waits for job, run by rails pod pod, to end and reads its
// result. On timeout or a cancelled ctx it asks the pod to stop the fetch.
func (a *Agent) waitRailsJob(ctx context.Context, pod string, j railsJob, timeout time.Duration) (*railsFetchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	poll := time.NewTicker(railsJobPoll)
	defer poll.Stop()
	lastPodCheck := time.Now()
	for {
		if code, err := os.ReadFile(j.path(".exit")); err == nil {
			out, _ := os.ReadFile(j.path(".out"))
			exit, cerr := strconv.Atoi(strings.TrimSpace(string(code)))
			if cerr != nil {
				return nil, fmt.Errorf("rails fetch %s: exit code %q", j.id, strings.TrimSpace(string(code)))
			}
			res, perr := parseFetchOutput(exit, string(out), "")
			if perr != nil {
				if log, _ := os.ReadFile(j.path(".log")); len(log) > 0 {
					perr = fmt.Errorf("%w; log: %s", perr, truncate(strings.TrimSpace(string(log)), 400))
				}
			}
			return res, perr
		}
		if time.Since(lastPodCheck) >= railsJobPodCheck {
			lastPodCheck = time.Now()
			p, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).Get(ctx, pod, metav1.GetOptions{})
			switch {
			case apierrors.IsNotFound(err):
				return nil, fmt.Errorf("%w: %s deleted", errRailsPodGone, pod)
			case err == nil && (podEnded(p) || p.DeletionTimestamp != nil):
				return nil, fmt.Errorf("%w: %s %s", errRailsPodGone, pod, terminationSummary(p))
			}
		}
		select {
		case <-ctx.Done():
			_ = os.WriteFile(j.path(".cancel"), nil, 0o600)
			return nil, fmt.Errorf("rails fetch %s: %w", j.id, ctx.Err())
		case <-poll.C:
		}
	}
}

// parseFetchOutput reads a bedrock-peer fetch's result from its exit code
// and its --json line (the last line of out). reason names how it ended
// when it printed nothing (a signal).
func parseFetchOutput(exit int, out, reason string) (*railsFetchResult, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var res railsFetchResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), &res); err != nil {
		// A fetch killed by a signal prints no line: the exit code decides.
		res = railsFetchResult{ExitCode: exit}
	}
	if exit != 0 || !res.OK {
		kind, msg := "other", reason
		if res.ErrorKind != nil {
			kind = *res.ErrorKind
		}
		if res.Error != nil {
			msg = *res.Error
		}
		return nil, fmt.Errorf("bedrock-peer fetch exited %d (%s): %s", exit, kind, msg)
	}
	return &res, nil
}
