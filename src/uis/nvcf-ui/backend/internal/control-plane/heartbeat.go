// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controlplane

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/nvcf-ui/backend/internal/utils"
)

const (
	heartbeatFileEnvVar  = "HEARTBEAT_FILE"
	defaultHeartbeatFile = "/tmp/backend-heartbeat"
)

// livenessMaxAge is how stale the heartbeat may be before the monitor is
// considered hung. The loop refreshes the heartbeat once per
// healthCheckInterval, so we tolerate three missed cycles — covering a slow
// run of sequential endpoint probes — before declaring the process dead.
func livenessMaxAge() time.Duration {
	return 3 * healthCheckInterval
}

// heartbeatPath resolves the heartbeat file location from heartbeatFileEnvVar
// or defaultHeartbeatFile.
func heartbeatPath() string {
	return utils.GetEnvOr(heartbeatFileEnvVar, defaultHeartbeatFile)
}

// writeHeartbeat records the current time in the heartbeat file. It writes to a
// sibling temp file and renames it into place so a probe reading concurrently
// never observes a partial write.
func writeHeartbeat(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create heartbeat temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.WriteString(time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write heartbeat: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close heartbeat temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename heartbeat into place: %w", err)
	}
	return nil
}

// CheckLiveness reports whether the monitor's heartbeat file exists and was
// refreshed within livenessMaxAge. It is the body of the exec liveness probe:
// the kubelet re-execs this binary in liveness mode, so the check reuses the
// same path resolution and staleness threshold as the loop that writes the
// file. A missing, unparseable, or stale heartbeat returns a non-nil error,
// which the caller turns into a non-zero exit so the kubelet restarts the pod.
func CheckLiveness() error {
	path := heartbeatPath()
	maxAge := livenessMaxAge()

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read heartbeat %s: %w", path, err)
	}
	beat, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("parse heartbeat %q: %w", strings.TrimSpace(string(raw)), err)
	}
	if age := time.Since(beat); age > maxAge {
		return fmt.Errorf("heartbeat is stale: last beat %s ago, max %s", age.Round(time.Second), maxAge)
	}
	return nil
}
