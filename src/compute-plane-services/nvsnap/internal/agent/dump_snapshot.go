// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// A dump's snapshot is the copy of the container's replay mounts (/dev/shm
// and the like) that a restore puts back before CRIU maps them. Processes
// that share memory through files there keep their place in those files in
// their own memory: vLLM's engine and its workers pass every step through
// rings in /dev/shm, each process holding its own ring index. The copy must
// therefore be taken while CRIU holds the tree stopped for its dump. Taken
// earlier, the engine's steps in between move the rings on, and the
// restored processes wait on each other for good (seen: a copy taken 36 s
// before the dump; every restore of it hung).
//
// CRIU runs its post-dump action with the tree still stopped. The action
// script asks for the snapshot ("snapshot" in its directory) and waits
// until the agent has taken it ("snapshot-taken"); "snapshot-failed", or no
// answer in snapshotWait, fails the script, and CRIU then rolls the dump
// back.

// snapshotWait bounds the post-dump wait for the snapshot.
const snapshotWait = 5 * time.Minute

// dumpHookDir holds a dump's action script and its snapshot request.
const dumpHookDir = ".dump-hook"

// snapshotNotTaken is the error of a dump that finished without its
// snapshot: its restore would map stale copies.
func snapshotNotTaken(cause error) error {
	if cause != nil {
		return fmt.Errorf("the dump's snapshot of the replay mounts failed: %w", cause)
	}
	return errors.New("criu's post-dump hook never asked for the snapshot of the replay mounts; the checkpoint would restore stale copies")
}

// snapshotHook is the action script's request for the snapshot, in dir as
// the script sees it.
func snapshotHook(dir string) string {
	return fmt.Sprintf(`S=%q
: > "$S/snapshot"
i=0
while [ ! -e "$S/snapshot-taken" ]; do
	[ -e "$S/snapshot-failed" ] && exit 1
	i=$((i+1)); [ "$i" -gt %d ] && exit 1
	sleep 0.05
done
`, dir, int(snapshotWait/(50*time.Millisecond)))
}

// writeDumpActionScript writes a criu action script to path: at post-dump
// it asks for the snapshot in snapDir, then waits at the group barrier in
// barrierDir (either "" for none). Directories are as the script sees them.
// Any other action passes. A non-zero exit makes criu roll the dump back
// and resume the tree.
func writeDumpActionScript(path, snapDir, barrierDir string) error {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n[ \"$CRTOOLS_SCRIPT_ACTION\" = post-dump ] || exit 0\n")
	if snapDir != "" {
		b.WriteString(snapshotHook(snapDir))
	}
	if barrierDir != "" {
		b.WriteString(postDumpBarrier(barrierDir))
	}
	return os.WriteFile(path, []byte(b.String()), 0o755)
}

// serveSnapshot takes the snapshot when the action script asks for it in
// dir. The returned function stops serving and reports whether the
// snapshot was taken; a snapshot under way is finished first.
func serveSnapshot(ctx context.Context, dir *confinedDir, take func() error) func() (bool, error) {
	var (
		mu    sync.Mutex
		taken bool
		err   error
	)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if !dir.exists("snapshot") {
				continue
			}
			terr := take()
			answer := "snapshot-taken"
			if terr != nil {
				answer = "snapshot-failed"
			}
			if werr := dir.writeFileAtomic(answer, nil); werr != nil && terr == nil {
				terr = fmt.Errorf("answer the post-dump hook: %w", werr)
			}
			mu.Lock()
			taken, err = terr == nil, terr
			mu.Unlock()
			return
		}
	}()
	return func() (bool, error) {
		close(stop)
		<-done
		mu.Lock()
		defer mu.Unlock()
		return taken, err
	}
}
