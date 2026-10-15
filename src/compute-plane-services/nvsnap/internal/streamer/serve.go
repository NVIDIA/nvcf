/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package streamer

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/pierrec/lz4/v4"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// StartServe creates a Unix socket at {imagesDir}/streamer-serve.sock
// and waits for CRIU to connect. For each file CRIU requests, the streamer
// decompresses from disk and streams via pipe.
//
// compression may be nil for uncompressed checkpoints (pass-through mode).
func StartServe(imagesDir string, compression *CompressionInfo) (<-chan error, error) {
	socketPath := filepath.Join(imagesDir, serveSocketName)
	_ = os.Remove(socketPath)

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", socketPath, err)
	}

	done := make(chan error, 1)
	go runServe(listener, socketPath, imagesDir, compression, done)
	return done, nil
}

func runServe(listener *net.UnixListener, socketPath, imagesDir string, compression *CompressionInfo, done chan<- error) {
	defer func() { _ = listener.Close() }()
	defer func() { _ = os.Remove(socketPath) }()

	log.WithField("socket", socketPath).Info("Serve streamer waiting for CRIU")

	conn, err := listener.AcceptUnix()
	if err != nil {
		done <- fmt.Errorf("accept: %w", err)
		return
	}
	defer func() { _ = conn.Close() }()
	cs, err := newControlSocket(conn)
	if err != nil {
		done <- fmt.Errorf("control socket: %w", err)
		return
	}

	log.Info("Serve streamer: CRIU connected")

	fileCount := 0
	for {
		filename, err := cs.readRequest()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			done <- fmt.Errorf("read request: %w", err)
			return
		}

		filePath := filepath.Join(imagesDir, filename)
		_, statErr := os.Stat(filePath)
		exists := statErr == nil

		if replyErr := cs.writeReply(exists); replyErr != nil {
			done <- fmt.Errorf("write reply for %s: %w", filename, replyErr)
			return
		}

		if !exists {
			continue
		}

		// CRIU creates pipe and sends write-end to us via SCM_RIGHTS.
		// We write decompressed data to it; CRIU reads from read-end.
		pipeFD, err := cs.recvFD()
		if err != nil {
			done <- fmt.Errorf("recv fd for %s: %w", filename, err)
			return
		}

		setPipeSize(pipeFD, pipeBufSize)
		pipeWriter := os.NewFile(uintptr(pipeFD), "criu-pipe")

		isCompressed := false
		if compression != nil {
			_, isCompressed = compression.Files[filename]
		}

		if err := serveFile(filePath, pipeWriter, isCompressed); err != nil {
			if !isEPIPE(err) {
				log.WithError(err).WithField("file", filename).Warn("Serve streamer: error")
			}
		}
		_ = pipeWriter.Close()

		fileCount++
		if fileCount%50 == 0 {
			log.WithField("files", fileCount).Debug("Serve progress")
		}
	}

	log.WithField("files", fileCount).Info("Serve streamer complete")
	done <- nil
}

// serveFile decompresses (if needed) and streams to w using pooled buffer.
func serveFile(path string, w io.Writer, decompress bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if decompress {
		r = lz4.NewReader(f)
	}

	buf := getCopyBuf()
	_, err = io.CopyBuffer(w, r, buf)
	putCopyBuf(buf)
	return err
}

func isEPIPE(err error) bool {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return errors.Is(pe.Err, unix.EPIPE)
	}
	return false
}
