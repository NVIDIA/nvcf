// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/transform"
)

// jsonlExt is the file extension of a gzip JSON-lines transport.
const jsonlExt = ".jsonl.gz"

var errWriterDone = errors.New("format writer is already closed or aborted")

// GzipJSONL is the Factory for backend.FormatGzipJSONL. Its writer stores each
// record's payload as one line, so records keep their source bytes except
// where suppression or redaction changed them.
type GzipJSONL struct{}

// Format reports backend.FormatGzipJSONL.
func (GzipJSONL) Format() backend.Format { return backend.FormatGzipJSONL }

// NewWriter starts a writer for target, which must name exactly one
// destination. The file is written under a temporary name that starts with
// the segment id and appears under its final name only once Close has
// synced it.
func (GzipJSONL) NewWriter(target Target) (Writer, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if len(target.Destinations) != 1 {
		return nil, fmt.Errorf("gzip JSON lines writes one destination, got %d", len(target.Destinations))
	}
	destination := target.Destinations[0]
	path := target.Path(destination, jsonlExt)
	file, err := os.CreateTemp(target.Dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create transport: %w", err)
	}
	out := &hashingFile{file: file, hash: sha256.New()}
	return &jsonlWriter{
		destination: destination,
		path:        path,
		out:         out,
		gz:          gzip.NewWriter(out),
	}, nil
}

// hashingFile hashes and counts the bytes that reach the file, so Close can
// describe the transport without reading it back.
type hashingFile struct {
	file  *os.File
	hash  hash.Hash
	bytes int64
}

func (h *hashingFile) Write(p []byte) (int, error) {
	n, err := h.file.Write(p)
	h.hash.Write(p[:n])
	h.bytes += int64(n)
	return n, err
}

type jsonlWriter struct {
	destination backend.Destination
	path        string
	out         *hashingFile
	gz          *gzip.Writer
	rows        int64
	outputs     []Output
	done        bool
}

var _ Writer = (*jsonlWriter)(nil)

// Write appends one record's payload and a newline.
func (w *jsonlWriter) Write(r transform.Record) error {
	if w.done {
		return errWriterDone
	}
	if _, err := w.gz.Write(r.Payload); err != nil {
		return fmt.Errorf("write transport: %w", err)
	}
	if _, err := w.gz.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("write transport: %w", err)
	}
	w.rows++
	return nil
}

// Close publishes the file. With no records it publishes nothing: an empty
// file is not an upload.
func (w *jsonlWriter) Close() error {
	if w.done {
		return errWriterDone
	}
	if w.rows == 0 {
		return w.Abort()
	}
	w.done = true
	if err := w.publish(); err != nil {
		return errors.Join(fmt.Errorf("publish transport: %w", err), w.discard(), removeIfPresent(w.path))
	}
	w.outputs = []Output{{
		Destination: w.destination,
		Path:        w.path,
		SHA256:      hex.EncodeToString(w.out.hash.Sum(nil)),
		Bytes:       w.out.bytes,
		Rows:        w.rows,
	}}
	return nil
}

func (w *jsonlWriter) publish() error {
	err := w.gz.Close()
	if err == nil {
		err = w.out.file.Sync()
	}
	if closeErr := w.out.file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(w.out.file.Name(), w.path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(w.path))
	}
	return err
}

// Abort removes the unfinished file. It is safe to call more than once and
// after a Close that failed.
func (w *jsonlWriter) Abort() error {
	w.done = true
	w.outputs = nil
	if err := w.discard(); err != nil {
		return fmt.Errorf("discard transport: %w", err)
	}
	return nil
}

func (w *jsonlWriter) discard() error {
	closeErr := w.out.file.Close()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	return errors.Join(closeErr, removeIfPresent(w.out.file.Name()))
}

// Outputs lists the published file after a Close that wrote records.
func (w *jsonlWriter) Outputs() []Output {
	return append([]Output(nil), w.outputs...)
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
