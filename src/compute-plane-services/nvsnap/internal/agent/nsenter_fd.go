// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Entering a workload's namespaces. nsenter -t <pid> resolves the
// process's namespaces when it runs; a process can have moved by then. The
// agent opens them once, checks them, and has nsenter enter exactly those,
// by descriptor. The check: the process is in the initial user namespace
// and holds no CAP_SYS_ADMIN there, so it can change neither its mounts nor
// its mount namespace (bundle_tools.go relies on that).

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// nsKinds are the namespaces nsenter can enter by file, by its flag.
var nsKinds = map[string]string{"mnt": "--mount", "pid": "--pid", "net": "--net", "ipc": "--ipc", "uts": "--uts"}

// nsenterByFD opens host pid's namespaces of the given kinds (and, with
// rootAndWD, its root and working directory), checks the process, and
// returns nsenter's arguments naming them as the child's descriptors from
// first, with the files to pass at those descriptors (ExtraFiles, in
// order). The caller closes the files.
func nsenterByFD(procBase string, pid int, kinds []string, rootAndWD bool, first int) (args []string, files []*os.File, err error) {
	defer func() {
		if err != nil {
			closeFiles(files)
			files = nil
		}
	}()
	base := filepath.Join(procBase, strconv.Itoa(pid))
	user, err := os.Open(filepath.Join(base, "ns", "user"))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = user.Close() }()
	if uerr := initialUserNS(user); uerr != nil {
		return nil, nil, fmt.Errorf("process %d: %w", pid, uerr)
	}
	if admin, cerr := hasCapability(filepath.Join(base, "status"), capSysAdmin); cerr != nil {
		return nil, nil, cerr
	} else if admin {
		return nil, nil, fmt.Errorf("process %d holds CAP_SYS_ADMIN and can change its mounts", pid)
	}
	add := func(flag, path string, flags int) error {
		f, oerr := os.OpenFile(path, flags, 0)
		if oerr != nil {
			return oerr
		}
		files = append(files, f)
		args = append(args, flag+"=/proc/self/fd/"+strconv.Itoa(first+len(files)-1))
		return nil
	}
	for _, k := range kinds {
		flag, ok := nsKinds[k]
		if !ok {
			return nil, files, fmt.Errorf("unknown namespace %q", k)
		}
		if aerr := add(flag, filepath.Join(base, "ns", k), os.O_RDONLY); aerr != nil {
			return nil, files, aerr
		}
	}
	if rootAndWD {
		if aerr := add("--root", filepath.Join(base, "root"), os.O_RDONLY|syscall.O_DIRECTORY); aerr != nil {
			return nil, files, aerr
		}
		if aerr := add("--wd", filepath.Join(base, "cwd"), os.O_RDONLY|syscall.O_DIRECTORY); aerr != nil {
			return nil, files, aerr
		}
	}
	// The user namespace again: had the process moved while its
	// namespaces were opened, they could belong to another one.
	again, err := os.Open(filepath.Join(base, "ns", "user"))
	if err != nil {
		return nil, files, err
	}
	defer func() { _ = again.Close() }()
	if err := initialUserNS(again); err != nil {
		return nil, files, fmt.Errorf("process %d: %w", pid, err)
	}
	return args, files, nil
}

// initialUserNS checks that user namespace f is the agent's own: the
// initial one, where the agent runs.
func initialUserNS(f *os.File) error {
	var mine, theirs syscall.Stat_t
	if err := syscall.Stat("/proc/self/ns/user", &mine); err != nil {
		return err
	}
	if err := syscall.Fstat(int(f.Fd()), &theirs); err != nil {
		return err
	}
	if mine.Ino != theirs.Ino || mine.Dev != theirs.Dev {
		return fmt.Errorf("runs in a user namespace of its own, where it can change its mounts")
	}
	return nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}
