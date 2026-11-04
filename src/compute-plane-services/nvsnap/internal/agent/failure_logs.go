// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A failed capture's directory is removed at once: nothing in it can be
// restored. Its logs (criu's dump.log above all) are the only account of
// why it failed, and the error carries only their tail, so they are kept
// under <checkpoint dir>/.failed-logs/<capture>, the newest
// failureLogsKept of them.

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	failureLogsDir  = ".failed-logs"
	failureLogsKept = 20
	// failureLogMax bounds one kept log file.
	failureLogMax = 64 << 20
)

// keepFailureLogs copies the *.log files of the failed capture in dir into
// root's failed-logs directory and drops the oldest beyond
// failureLogsKept.
func keepFailureLogs(root, dir string, log *logrus.Entry) {
	dst := filepath.Join(root, failureLogsDir, filepath.Base(dir))
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".log") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		if cerr := copyLogFile(p, filepath.Join(dst, rel)); cerr != nil {
			log.WithError(cerr).WithField("file", p).Warn("cannot keep a failed capture's log")
			return nil
		}
		n++
		return nil
	})
	if n == 0 {
		return
	}
	log.WithFields(logrus.Fields{"logs": n, "dir": dst}).Info("kept the failed capture's logs")
	pruneFailureLogs(filepath.Join(root, failureLogsDir), failureLogsKept)
}

func copyLogFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if merr := os.MkdirAll(filepath.Dir(dst), 0o755); merr != nil {
		return merr
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, failureLogMax)); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// pruneFailureLogs keeps the keep most recently written entries of dir.
func pruneFailureLogs(dir string, keep int) {
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= keep {
		return
	}
	type entry struct {
		name string
		mod  int64
	}
	all := make([]entry, 0, len(ents))
	for _, e := range ents {
		if info, ierr := e.Info(); ierr == nil {
			all = append(all, entry{e.Name(), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod > all[j].mod })
	for _, e := range all[min(keep, len(all)):] {
		_ = os.RemoveAll(filepath.Join(dir, e.name))
	}
}
