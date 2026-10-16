// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// A workload's stdout and stderr are pipes the container runtime reads for
// the pod log. A dump holds only their write ends (the runtime holds the
// read ends), so CRIU restores each as a fresh pipe whose read end it
// closes: the restored workload writes its log into a pipe nobody reads,
// and the new pod's log stays empty. The restore hands CRIU the
// placeholder's own stdout and stderr in their place (--inherit-fd), so the
// restored workload logs to the new pod.
//
// A checkpoint that carries its pid namespace records the pipes in its
// pidns marker (pidns_capture.go); an in-namespace one records them here.

// stdioMarkerFile records an in-namespace checkpoint's stdio pipes.
const stdioMarkerFile = "stdio.json"

// stdioPipes are the inodes of a dumped tree's stdout and stderr pipes
// (0: not a pipe).
type stdioPipes struct {
	Stdout uint64 `json:"stdout,omitempty"`
	Stderr uint64 `json:"stderr,omitempty"`
}

// readStdio returns pid's stdout and stderr pipes. Read it while the tree
// still runs: a dump that does not leave it running takes them away.
func readStdio(procBase string, pid int) stdioPipes {
	return stdioPipes{Stdout: pipeInode(procBase, pid, 1), Stderr: pipeInode(procBase, pid, 2)}
}

func writeStdioMarker(checkpointDir string, p stdioPipes) error {
	if p == (stdioPipes{}) {
		return nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(checkpointDir, stdioMarkerFile), b, 0o644)
}

// readStdioMarker returns the pipes an in-namespace checkpoint recorded;
// none for a checkpoint taken before they were recorded.
func readStdioMarker(checkpointDir string) (stdioPipes, error) {
	var p stdioPipes
	b, err := os.ReadFile(filepath.Join(checkpointDir, stdioMarkerFile))
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

// stdioFirstFD is the fd the first of the placeholder's stdio files lands
// on in criu: exec.Cmd.ExtraFiles start at fd 3.
const stdioFirstFD = 3

// placeholderStdio opens the placeholder's stdout and stderr in place of
// the recorded pipes, for the restore to pass to CRIU as extra files (from
// stdioFirstFD on), and returns the --inherit-fd declarations naming them.
// The caller closes the files.
func placeholderStdio(procBase string, placeholderPID int, p stdioPipes) ([]*os.File, []string, error) {
	var (
		files []*os.File
		args  []string
	)
	add := func(fd int, pipe uint64) error {
		f, err := os.OpenFile(filepath.Join(procBase, strconv.Itoa(placeholderPID), "fd", strconv.Itoa(fd)), os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("open the placeholder's fd %d: %w", fd, err)
		}
		args = append(args, "--inherit-fd", fmt.Sprintf("fd[%d]:pipe:[%d]", stdioFirstFD+len(files), pipe))
		files = append(files, f)
		return nil
	}
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	if p.Stdout != 0 {
		if err := add(1, p.Stdout); err != nil {
			return nil, nil, err
		}
	}
	if p.Stderr != 0 && p.Stderr != p.Stdout {
		if err := add(2, p.Stderr); err != nil {
			closeAll()
			return nil, nil, err
		}
	}
	return files, args, nil
}
