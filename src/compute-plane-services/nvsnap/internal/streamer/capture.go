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
)

// StartCapture creates a Unix socket at {imagesDir}/streamer-capture.sock
// and waits for CRIU to connect. For each file CRIU dumps, the streamer
// receives the data via a pipe, compresses it with lz4, and writes to disk.
//
// Returns a channel that delivers the result when CRIU closes the connection.
// The caller must read from the channel to avoid goroutine leaks.
func StartCapture(imagesDir string) (<-chan CaptureResult, error) {
	socketPath := filepath.Join(imagesDir, captureSocketName)
	_ = os.Remove(socketPath)

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", socketPath, err)
	}

	result := make(chan CaptureResult, 1)
	go runCapture(listener, socketPath, imagesDir, result)
	return result, nil
}

func runCapture(listener *net.UnixListener, socketPath, imagesDir string, result chan<- CaptureResult) {
	defer func() { _ = listener.Close() }()
	defer func() { _ = os.Remove(socketPath) }()

	log.WithField("socket", socketPath).Info("Capture streamer waiting for CRIU")

	conn, err := listener.AcceptUnix()
	if err != nil {
		result <- CaptureResult{Err: fmt.Errorf("accept: %w", err)}
		return
	}
	defer func() { _ = conn.Close() }()
	cs, err := newControlSocket(conn)
	if err != nil {
		result <- CaptureResult{Err: fmt.Errorf("control socket: %w", err)}
		return
	}

	log.Info("Capture streamer: CRIU connected")

	info := &CompressionInfo{
		Algorithm: "lz4",
		Level:     1,
		Files:     make(map[string]CompressedFile),
	}

	fileCount := 0
	for {
		// Read file request — uses recvmsg internally, won't lose FDs
		filename, err := cs.readRequest()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			result <- CaptureResult{Err: fmt.Errorf("read request: %w", err)}
			return
		}

		// Get pipe FD — may have been received during readRequest
		pipeFD, err := cs.recvFD()
		if err != nil {
			result <- CaptureResult{Err: fmt.Errorf("recv fd for %s: %w", filename, err)}
			return
		}

		setPipeSize(pipeFD, pipeBufSize)
		pipeReader := os.NewFile(uintptr(pipeFD), "criu-pipe")

		// Compress pipe → disk with pooled buffer
		origSize, compSize, err := compressFile(imagesDir, filename, pipeReader)
		_ = pipeReader.Close()
		if err != nil {
			result <- CaptureResult{Err: fmt.Errorf("compress %s: %w", filename, err)}
			return
		}

		info.Files[filename] = CompressedFile{
			OriginalSize:   origSize,
			CompressedSize: compSize,
		}
		info.TotalOriginalSize += origSize
		info.TotalCompressedSize += compSize
		fileCount++

		if fileCount%50 == 0 || origSize > 100<<20 {
			log.WithFields(log.Fields{
				"file":       filename,
				"files":      fileCount,
				"original":   info.TotalOriginalSize,
				"compressed": info.TotalCompressedSize,
			}).Debug("Capture progress")
		}
	}

	if info.TotalOriginalSize > 0 {
		info.Ratio = float64(info.TotalOriginalSize) / float64(info.TotalCompressedSize)
	}

	log.WithFields(log.Fields{
		"files":      fileCount,
		"original":   info.TotalOriginalSize,
		"compressed": info.TotalCompressedSize,
		"ratio":      fmt.Sprintf("%.1fx", info.Ratio),
	}).Info("Capture streamer complete")

	result <- CaptureResult{Compression: info}
}

// compressFile reads from r, compresses with lz4, writes to {dir}/{filename}.
// Uses pooled 1MB buffer for io.CopyBuffer.
func compressFile(dir, filename string, r io.Reader) (originalSize, compressedSize int64, err error) {
	outPath := filepath.Join(dir, filename)
	outFile, err := os.Create(outPath)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = outFile.Close() }()
	lz4w := lz4.NewWriter(outFile)
	_ = lz4w.Apply(
		lz4.CompressionLevelOption(lz4.Fast),
		lz4.BlockSizeOption(lz4.Block4Mb), // larger blocks = better ratio
	)

	buf := getCopyBuf()
	originalSize, err = io.CopyBuffer(lz4w, r, buf)
	putCopyBuf(buf)

	if err != nil {
		_ = lz4w.Close()
		return 0, 0, fmt.Errorf("copy: %w", err)
	}
	if closeErr := lz4w.Close(); closeErr != nil {
		return 0, 0, fmt.Errorf("lz4 close: %w", closeErr)
	}

	fi, err := outFile.Stat()
	if err != nil {
		return originalSize, 0, err
	}
	return originalSize, fi.Size(), nil
}
