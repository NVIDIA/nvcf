// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transform

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// gzipFile writes a gzip stream to a temporary file that becomes visible
// under its final name only once it is complete and synced.
type gzipFile struct {
	file *os.File
	gz   *gzip.Writer
}

func createGzipFile(dir, pattern string) (*gzipFile, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	return &gzipFile{file: file, gz: gzip.NewWriter(file)}, nil
}

func (g *gzipFile) write(p []byte) (int, error) { return g.gz.Write(p) }

// publish finishes the stream, syncs it, and renames it to path.
func (g *gzipFile) publish(path string) error {
	err := g.gz.Close()
	if err == nil {
		err = g.file.Sync()
	}
	if closeErr := g.file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(g.file.Name(), path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	if err != nil {
		return errors.Join(err, removeIfPresent(g.file.Name()))
	}
	return nil
}

func (g *gzipFile) discard() error {
	closeErr := g.file.Close()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	return errors.Join(closeErr, removeIfPresent(g.file.Name()))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// JSONLFile is a Writer that stores each record's payload as one line of a
// gzip JSON-lines file, the format a backend uses when it keeps records as
// they are. The file appears at its path only after Close.
type JSONLFile struct {
	path string
	out  *gzipFile
}

// CreateJSONLFile starts a transport that Close publishes at path.
func CreateJSONLFile(path string) (*JSONLFile, error) {
	out, err := createGzipFile(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}
	return &JSONLFile{path: path, out: out}, nil
}

// Write appends one record.
func (j *JSONLFile) Write(r Record) error {
	if _, err := j.out.write(r.Payload); err != nil {
		return fmt.Errorf("write transport: %w", err)
	}
	if _, err := j.out.write([]byte{'\n'}); err != nil {
		return fmt.Errorf("write transport: %w", err)
	}
	return nil
}

// Close publishes the transport.
func (j *JSONLFile) Close() error {
	if err := j.out.publish(j.path); err != nil {
		return fmt.Errorf("publish transport: %w", err)
	}
	return nil
}

// Abort removes the unfinished transport.
func (j *JSONLFile) Abort() error {
	if err := j.out.discard(); err != nil {
		return fmt.Errorf("discard transport: %w", err)
	}
	return nil
}

// RejectFile collects the raw bytes of rejected lines for local inspection.
// It creates its directory and file only when the first line is rejected, so
// a clean segment leaves nothing behind. The bytes can hold user data: the
// file is readable by its owner only and must never be exported.
type RejectFile struct {
	dir string
	out *gzipFile
	err error
}

// NewRejectFile collects rejects into dir.
func NewRejectFile(dir string) *RejectFile { return &RejectFile{dir: dir} }

// Write appends raw rejected bytes.
func (r *RejectFile) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.out == nil {
		if err := os.MkdirAll(r.dir, 0o750); err != nil {
			r.err = fmt.Errorf("create reject directory: %w", err)
			return 0, r.err
		}
		out, err := createGzipFile(r.dir, ".reject.*.tmp")
		if err != nil {
			r.err = fmt.Errorf("create reject artifact: %w", err)
			return 0, r.err
		}
		r.out = out
	}
	n, err := r.out.write(p)
	if err != nil {
		r.err = fmt.Errorf("write reject artifact: %w", err)
	}
	return n, r.err
}

// Commit names the artifact <name>.jsonl.gz, where name identifies the
// segment, and reports whether anything was rejected.
func (r *RejectFile) Commit(name string) (bool, error) {
	if r.out == nil {
		return false, r.err
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false, fmt.Errorf("reject artifact name %q is not a plain file name", name)
	}
	if r.err != nil {
		return false, r.err
	}
	if err := r.out.publish(filepath.Join(r.dir, name+".jsonl.gz")); err != nil {
		return false, fmt.Errorf("publish reject artifact: %w", err)
	}
	r.out = nil
	return true, nil
}

// Discard removes an artifact that was not committed.
func (r *RejectFile) Discard() error {
	if r.out == nil {
		return nil
	}
	err := r.out.discard()
	r.out = nil
	if err != nil {
		return fmt.Errorf("discard reject artifact: %w", err)
	}
	return nil
}
