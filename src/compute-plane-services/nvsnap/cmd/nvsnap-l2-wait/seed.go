/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Seed mode: fetch a compile-cache seed from the node agent and unpack it
// into the cachedir before the engine starts. Best effort by contract:
// every outcome exits 0, because a missing seed only costs the compile
// the engine would have done without nvsnap, while a failing init would
// take the pod down.
type seedConfig struct {
	URL      string
	Dest     string
	Token    string
	Timeout  time.Duration
	Interval time.Duration
	Client   *http.Client
}

const (
	seedDefaultTimeout  = 3 * time.Minute
	seedDefaultInterval = 5 * time.Second
	seedTokenHeader     = "X-Nvsnap-Seed-Token"
)

func runSeed(ctx context.Context, cfg seedConfig, out io.Writer) int {
	if cfg.URL == "" || cfg.Dest == "" {
		logTo(out, "warn", "seed: url and dest are required; skipping", logEvent{})
		return exitReady
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = seedDefaultTimeout
	}
	if cfg.Interval <= 0 {
		cfg.Interval = seedDefaultInterval
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	start := time.Now()
	deadline := start.Add(cfg.Timeout)
	logTo(out, "info", "seed: fetching compile cache from the node agent", logEvent{})
	for attempt := 1; ; attempt++ {
		if ctx.Err() != nil {
			logTo(out, "warn", "seed: cancelled; engine compiles locally", logEvent{Err: ctx.Err().Error(), Elapsed: time.Since(start).Seconds()})
			return exitReady
		}
		code, files, bytes, err := fetchSeed(ctx, client, cfg)
		switch {
		case err == nil && code == http.StatusOK:
			logTo(out, "info", fmt.Sprintf("seed: unpacked %d files (%d MiB) in %.0fs", files, bytes>>20, time.Since(start).Seconds()), logEvent{HTTPCode: code, Elapsed: time.Since(start).Seconds()})
			return exitReady
		case code == http.StatusServiceUnavailable || code == 0:
			// Mirror still filling on this node, or the agent is not
			// reachable yet: wait and retry until the deadline.
		default:
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			logTo(out, "warn", "seed: agent refused the request; engine compiles locally", logEvent{HTTPCode: code, Err: msg, Elapsed: time.Since(start).Seconds()})
			return exitReady
		}
		if time.Now().Add(cfg.Interval).After(deadline) {
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			logTo(out, "warn", "seed: not available within the deadline; engine compiles locally", logEvent{HTTPCode: code, Err: msg, Elapsed: time.Since(start).Seconds()})
			return exitReady
		}
		if attempt == 1 || attempt%6 == 0 {
			logTo(out, "info", "seed: waiting for the node mirror", logEvent{HTTPCode: code, Elapsed: time.Since(start).Seconds(), NextIn: cfg.Interval.Seconds()})
		}
		select {
		case <-ctx.Done():
		case <-time.After(cfg.Interval):
		}
	}
}

// fetchSeed performs one GET and, on 200, unpacks the tar into Dest.
// code is 0 when the request itself failed.
func fetchSeed(ctx context.Context, client *http.Client, cfg seedConfig) (code int, files int, bytes int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	if cfg.Token != "" {
		req.Header.Set(seedTokenHeader, cfg.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, 0, 0, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	files, bytes, err = untar(resp.Body, cfg.Dest)
	if err != nil {
		return http.StatusOK, files, bytes, err
	}
	return http.StatusOK, files, bytes, nil
}

// untar unpacks a tar stream into dest: directories, regular files and
// relative symlinks, nothing that escapes dest. Everything is made
// world-readable and writable, like the volume seed's chmod, because the
// engine may run as a different user than this init.
func untar(r io.Reader, dest string) (int, int64, error) {
	if err := os.MkdirAll(dest, 0o777); err != nil {
		return 0, 0, err
	}
	tr := tar.NewReader(r)
	files, total := 0, int64(0)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files, total, nil
		}
		if err != nil {
			return files, total, err
		}
		rel := filepath.Clean(hdr.Name)
		if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			continue
		}
		target := filepath.Join(dest, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o777); err != nil {
				return files, total, err
			}
			_ = os.Chmod(target, 0o777)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
				return files, total, err
			}
			mode := os.FileMode(0o666)
			if hdr.FileInfo().Mode()&0o111 != 0 {
				mode = 0o777
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return files, total, err
			}
			n, cerr := io.Copy(f, tr)
			f.Close()
			if cerr != nil {
				return files, total, cerr
			}
			_ = os.Chmod(target, mode)
			files++
			total += n
		case tar.TypeSymlink:
			if filepath.IsAbs(hdr.Linkname) || strings.HasPrefix(filepath.Clean(filepath.Join(filepath.Dir(rel), hdr.Linkname)), "..") {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
				return files, total, err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return files, total, err
			}
		}
	}
}
