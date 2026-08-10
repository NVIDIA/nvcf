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

package agent

import (
	"os"
	"reflect"
	"testing"
)

func TestClassifyMount(t *testing.T) {
	allowlist := []string{"/dev/shm"}

	tests := []struct {
		name   string
		mp     string
		fsType string
		opts   string
		want   MountClass
	}{
		// rootfs
		{"overlay root", "/", "overlay", "rw,relatime", MountClassRootfs},

		// replay (on allowlist, RW, non-virtual)
		{"dev/shm tmpfs rw", "/dev/shm", "tmpfs", "rw,nosuid,nodev", MountClassReplay},

		// skip: not on allowlist
		{"tmp tmpfs not allowlisted", "/tmp", "tmpfs", "rw,relatime", MountClassSkip},
		{"custom volume not allowlisted", "/data", "ext4", "rw,relatime", MountClassSkip},
		{"nvsnap-lib not allowlisted", "/nvsnap-lib", "ext4", "rw,relatime", MountClassSkip},

		// skip: read-only even though allowlisted
		{"dev/shm RO", "/dev/shm", "tmpfs", "ro,nosuid", MountClassSkip},

		// skip: virtual fs even on allowlist (defensive)
		{"misconfig: proc on allowlist", "/dev/shm", "proc", "rw", MountClassSkip},

		// skip: virtual fs not on allowlist
		{"proc", "/proc", "proc", "rw", MountClassSkip},
		{"sysfs", "/sys", "sysfs", "rw", MountClassSkip},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", "rw", MountClassSkip},
		{"devpts", "/dev/pts", "devpts", "rw", MountClassSkip},

		// skip: K8s identity files (not on allowlist)
		{"hostname bind", "/etc/hostname", "ext4", "rw,relatime", MountClassSkip},
		{"hosts bind", "/etc/hosts", "ext4", "rw,relatime", MountClassSkip},

		// skip: nvidia/CDI (not on allowlist)
		{"nvidia0", "/dev/nvidia0", "devtmpfs", "rw", MountClassSkip},

		// skip: empty allowlist → everything non-rootfs is skip
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyMount(tc.mp, tc.fsType, tc.opts, allowlist)
			if got != tc.want {
				t.Errorf("classifyMount(%q, %q, %q, %v) = %v; want %v",
					tc.mp, tc.fsType, tc.opts, allowlist, got, tc.want)
			}
		})
	}
}

func TestClassifyMount_EmptyAllowlist(t *testing.T) {
	// Empty allowlist disables the replay class entirely; only rootfs survives.
	if got := classifyMount("/", "overlay", "rw", nil); got != MountClassRootfs {
		t.Errorf("/ with empty allowlist: got %v, want rootfs", got)
	}
	if got := classifyMount("/dev/shm", "tmpfs", "rw", nil); got != MountClassSkip {
		t.Errorf("/dev/shm with empty allowlist: got %v, want skip", got)
	}
}

func TestClassifyMount_AllowlistMatchIsExact(t *testing.T) {
	// Substring matching would let "/dev/shm" allowlist "/dev/shm-foo".
	// We require exact path equality to keep the allowlist predictable.
	allowlist := []string{"/dev/shm"}
	if got := classifyMount("/dev/shm-foo", "tmpfs", "rw", allowlist); got != MountClassSkip {
		t.Errorf("/dev/shm-foo (substring of /dev/shm): got %v, want skip (exact match required)", got)
	}
	if got := classifyMount("/dev/shm/sub", "tmpfs", "rw", allowlist); got != MountClassSkip {
		t.Errorf("/dev/shm/sub (child of /dev/shm): got %v, want skip (exact match required)", got)
	}
}

func TestReplayMountAllowlist(t *testing.T) {
	// Save and restore env to avoid polluting other tests.
	prev, hadPrev := os.LookupEnv("NVSNAP_REPLAY_MOUNTS")
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv("NVSNAP_REPLAY_MOUNTS", prev)
		} else {
			_ = os.Unsetenv("NVSNAP_REPLAY_MOUNTS")
		}
	})

	t.Run("default", func(t *testing.T) {
		_ = os.Unsetenv("NVSNAP_REPLAY_MOUNTS")
		got := ReplayMountAllowlist()
		want := []string{"/dev/shm"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("default: got %v, want %v", got, want)
		}
	})

	t.Run("env override single", func(t *testing.T) {
		_ = os.Setenv("NVSNAP_REPLAY_MOUNTS", "/tmp")
		got := ReplayMountAllowlist()
		want := []string{"/tmp"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("override: got %v, want %v", got, want)
		}
	})

	t.Run("env override multiple with spaces", func(t *testing.T) {
		_ = os.Setenv("NVSNAP_REPLAY_MOUNTS", "/dev/shm, /tmp ,/var/run")
		got := ReplayMountAllowlist()
		want := []string{"/dev/shm", "/tmp", "/var/run"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("override: got %v, want %v", got, want)
		}
	})

	t.Run("env empty falls back to default", func(t *testing.T) {
		_ = os.Setenv("NVSNAP_REPLAY_MOUNTS", "")
		got := ReplayMountAllowlist()
		want := []string{"/dev/shm"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("empty env: got %v, want %v", got, want)
		}
	})

	t.Run("env whitespace-only falls back to default", func(t *testing.T) {
		_ = os.Setenv("NVSNAP_REPLAY_MOUNTS", "   ")
		got := ReplayMountAllowlist()
		want := []string{"/dev/shm"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("whitespace env: got %v, want %v", got, want)
		}
	})

	t.Run("env with empty entries are dropped", func(t *testing.T) {
		_ = os.Setenv("NVSNAP_REPLAY_MOUNTS", ",/dev/shm,,/tmp,")
		got := ReplayMountAllowlist()
		want := []string{"/dev/shm", "/tmp"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("empty entries: got %v, want %v", got, want)
		}
	})
}

func TestMountIsRW(t *testing.T) {
	cases := []struct {
		opts string
		want bool
	}{
		{"rw,nosuid,nodev,relatime", true},
		{"ro,nosuid", false},
		{"rw", true},
		{"ro", false},
		{"nosuid,nodev", false}, // neither rw nor ro present
		{"", false},
	}
	for _, tc := range cases {
		if got := mountIsRW(tc.opts); got != tc.want {
			t.Errorf("mountIsRW(%q) = %v; want %v", tc.opts, got, tc.want)
		}
	}
}
