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
// placeholder's root, its network namespace, the listening unix sockets of
// the capture (bound in the placeholder's mount namespace by the node
// bundle's static agent binary), and the placeholder's stdout and stderr
// when the capture recorded the workload's.
func (a *Agent) pidNSRestoreFiles(ctx context.Context, procBase string, hostPID int, c pidNSCapture, tools bundleTools, log *logrus.Entry) (files []*os.File, fds PIDNSFDs, err error) {
	defer func() {
		if err != nil {
			for _, f := range files {
				_ = f.Close()
			}
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
	if oerr := open("root", unix.O_PATH|unix.O_DIRECTORY); oerr != nil {
		return files, fds, oerr
	}
	if oerr := open("ns/net", unix.O_RDONLY); oerr != nil {
		return files, fds, oerr
	}
	fds.Root, fds.Net = 3, 4
	if len(c.UnixListeners) > 0 {
		ls, lerr := bindPlaceholderListeners(ctx, hostPID, c.UnixListeners, tools)
		if lerr != nil {
			return files, fds, lerr
		}
		for _, l := range ls {
			fds.Listeners = append(fds.Listeners, 3+len(files))
			files = append(files, l)
		}
		log.WithField("listeners", len(ls)).Info("criu-v2: bound the capture's listening sockets in the placeholder")
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

// bindPlaceholderListeners binds the capture's listening unix sockets in
// the placeholder's mount namespace, where their paths are, and returns
// them. The binding runs there as the node bundle's agent binary (static:
// nothing from the placeholder's filesystem is loaded), and the sockets
// come back over a socket pair.
func bindPlaceholderListeners(ctx context.Context, hostPID int, listeners []UnixListener, tools bundleTools) ([]*os.File, error) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	ours, theirs := os.NewFile(uintptr(pair[0]), "listeners"), os.NewFile(uintptr(pair[1]), "listeners")
	defer func() { _ = ours.Close() }()
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "nsenter", "-t", strconv.Itoa(hostPID), "-m", "--", //nolint:gosec // the node bundle's agent binary, verified mounted read-only
		tools.path(pidNSRestoreHelperName), "bind-unix-listeners")
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Env = []string{PIDNSUnixListenersEnv + "=" + encodeUnixListeners(listeners)}
	out, err := cmd.CombinedOutput()
	_ = theirs.Close()
	if err != nil {
		return nil, fmt.Errorf("bind the listening sockets: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	buf, oob := make([]byte, 1), make([]byte, unix.CmsgSpace(4*len(listeners)))
	_, oobn, _, _, err := unix.Recvmsg(int(ours.Fd()), buf, oob, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("receive the listening sockets: %w", err)
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) != 1 {
		return nil, fmt.Errorf("receive the listening sockets: %d messages: %w", len(msgs), err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil {
		return nil, fmt.Errorf("receive the listening sockets: %w", err)
	}
	files := make([]*os.File, 0, len(fds))
	for _, fd := range fds {
		files = append(files, os.NewFile(uintptr(fd), "listener"))
	}
	if len(files) != len(listeners) {
		for _, f := range files {
			_ = f.Close()
		}
		return nil, fmt.Errorf("received %d listening sockets, want %d", len(files), len(listeners))
	}
	return files, nil
}
