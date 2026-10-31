// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// pidNSRestoreFiles opens what the restore helper hands the agent's CRIU
// (cmd/agent pidns-restore-exec), as descriptors from 3 in order: the
// placeholder's root (a clone of its mount tree, made in its mount
// namespace by the node bundle's static agent binary), its network
// namespace, the listening unix sockets of the capture (bound there by the
// same binary), and the placeholder's stdout and stderr when the capture
// recorded the workload's.
func (a *Agent) pidNSRestoreFiles(ctx context.Context, procBase string, hostPID int, c pidNSCapture, tools bundleTools, log *logrus.Entry) (files []*os.File, fds PIDNSFDs, err error) {
	defer func() {
		if err != nil {
			closeFiles(files)
			files = nil
		}
	}()
	base := filepath.Join(procBase, strconv.Itoa(hostPID))
	open := func(name string, flags int) error {
		fd, oerr := unix.Open(filepath.Join(base, name), flags|unix.O_CLOEXEC, 0)
		if oerr != nil {
			return fmt.Errorf("open the placeholder's %s: %w", name, oerr)
		}
		files = append(files, os.NewFile(uintptr(fd), name))
		return nil
	}
	got, perr := placeholderFiles(ctx, hostPID, c.UnixListeners, tools)
	if perr != nil {
		return files, fds, perr
	}
	files = append(files, got[0])
	if oerr := open("ns/net", unix.O_RDONLY); oerr != nil {
		closeFiles(got[1:])
		return files, fds, oerr
	}
	fds.Root, fds.Net = 3, 4
	for _, l := range got[1:] {
		fds.Listeners = append(fds.Listeners, 3+len(files))
		files = append(files, l)
	}
	if len(c.UnixListeners) > 0 {
		log.WithField("listeners", len(c.UnixListeners)).Info("criu-v2: bound the capture's listening sockets in the placeholder")
	}
	if c.StdoutPipe != 0 || c.StderrPipe != 0 {
		for _, name := range []string{"fd/1", "fd/2"} {
			if oerr := open(name, unix.O_WRONLY); oerr != nil {
				return files, fds, oerr
			}
			fds.Stdio = append(fds.Stdio, 2+len(files))
		}
	}
	return files, fds, nil
}

// placeholderFiles runs the node bundle's agent binary (static: nothing
// from the placeholder's filesystem is loaded) in the placeholder's mount
// namespace, where it clones the mount tree and binds the capture's
// listening unix sockets at their paths, and returns them: the tree first,
// then one socket per listener. They come back over a socket pair.
func placeholderFiles(ctx context.Context, hostPID int, listeners []UnixListener, tools bundleTools) ([]*os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	ours, theirs := os.NewFile(uintptr(pair[0]), "placeholder-files"), os.NewFile(uintptr(pair[1]), "placeholder-files")
	defer func() { _ = ours.Close() }()
	// The socket pair's end is the child's descriptor 3, the mount
	// namespace 4.
	nsArgs, nsFiles, err := nsenterByFD(hostProcBase(), hostPID, []string{"mnt"}, false, 4)
	if err != nil {
		_ = theirs.Close()
		return nil, fmt.Errorf("enter the placeholder's mount namespace: %w", err)
	}
	defer closeFiles(nsFiles)
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "nsenter", append(nsArgs, "--", //nolint:gosec // the node bundle's agent binary, verified mounted read-only
		tools.path(pidNSRestoreHelperName), "placeholder-files")...)
	cmd.ExtraFiles = append([]*os.File{theirs}, nsFiles...)
	cmd.Env = []string{PIDNSUnixListenersEnv + "=" + encodeUnixListeners(listeners)}
	out, err := cmd.CombinedOutput()
	_ = theirs.Close()
	if err != nil {
		return nil, fmt.Errorf("the placeholder's root and listening sockets: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	want := 1 + len(listeners)
	buf, oob := make([]byte, 1), make([]byte, unix.CmsgSpace(4*want))
	_, oobn, _, _, err := unix.Recvmsg(int(ours.Fd()), buf, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("receive the placeholder's root and listening sockets: %w", err)
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		return nil, fmt.Errorf("receive the placeholder's root and listening sockets: %d messages: %w", len(msgs), err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil {
		return nil, fmt.Errorf("receive the placeholder's root and listening sockets: %w", err)
	}
	files := make([]*os.File, 0, len(fds))
	for _, fd := range fds {
		files = append(files, os.NewFile(uintptr(fd), "placeholder-file"))
	}
	if len(files) != want {
		closeFiles(files)
		return nil, fmt.Errorf("received %d descriptors from the placeholder, want %d", len(files), want)
	}
	return files, nil
}
