// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// gpuToolWorkload is a /proc entry for pid 7 as the GPU tool's launch reads
// it: its namespaces (this process's own), its root (the versions
// directory, so /criu-bundle there is version "criu-bundle" as a bind
// mount's inode would be), its mountinfo and its status. It returns the
// proc base and the versions directory, whose version holds the tool.
func gpuToolWorkload(t *testing.T, user, mountinfo, status string) (procBase, versions string) {
	t.Helper()
	versions = t.TempDir()
	if err := os.MkdirAll(filepath.Join(versions, "criu-bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versions, "criu-bundle", gpushareToolName), []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	procBase = t.TempDir()
	dir := filepath.Join(procBase, "7")
	if err := os.MkdirAll(filepath.Join(dir, "ns"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, k := range gpushareNamespaces {
		if err := os.Symlink("/proc/self/ns/"+k, filepath.Join(dir, "ns", k)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(user, filepath.Join(dir, "ns", "user")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(versions, filepath.Join(dir, "root")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "cwd"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"mountinfo": mountinfo, "status": status} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return procBase, versions
}

const workerStatus = "Name:\tvllm\nUid:\t1000\t1000\t1000\t1000\nGid:\t1001\t1001\t1001\t1001\nCapEff:\t" + capsWorker + "\n"

func sameFile(t *testing.T, f *os.File, path string) bool {
	t.Helper()
	var a, b syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &a); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Stat(path, &b); err != nil {
		t.Fatal(err)
	}
	return a.Dev == b.Dev && a.Ino == b.Ino
}

// As the workload, the launcher gets the workload's ids, its namespaces by
// descriptor and the tool opened from the node's copy of the bundle
// version mounted in the workload; the tool's in-container path is only
// its argv[0].
func TestGPUShareToolCommandAsWorkload(t *testing.T) {
	procBase, versions := gpuToolWorkload(t, "/proc/self/ns/user", bundleRO, workerStatus)
	cmd, files, err := gpushareToolCommand(context.Background(), procBase, 7, versions, []string{"suspend", "1", "2"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles(files)
	want := []string{gpushareLauncher, "--uid", "1000", "--gid", "1001",
		"--ns", "mnt=3", "--ns", "pid=4", "--ns", "net=5", "--ns", "ipc=6", "--ns", "uts=7",
		"--exec-fd", "8", "--", "/criu-bundle/" + gpushareToolName, "suspend", "1", "2"}
	if cmd.Path != gpushareLauncher || strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("command %s %q, want %q", cmd.Path, cmd.Args, want)
	}
	if len(files) != len(gpushareNamespaces)+1 {
		t.Fatalf("%d descriptors, want %d", len(files), len(gpushareNamespaces)+1)
	}
	for i, k := range gpushareNamespaces {
		if !sameFile(t, files[i], "/proc/self/ns/"+k) {
			t.Errorf("descriptor %d is not the %s namespace", 3+i, k)
		}
	}
	if !sameFile(t, files[len(files)-1], filepath.Join(versions, "criu-bundle", gpushareToolName)) {
		t.Error("the program passed is not the version's tool")
	}
}

// Without the setting the tool runs as before: nsenter as root, the tool
// from the bundle mounted in the workload.
func TestGPUShareToolCommandAsRoot(t *testing.T) {
	procBase, _ := gpuToolWorkload(t, "/proc/self/ns/user", bundleRO, workerStatus)
	cmd, files, err := gpushareToolCommand(context.Background(), procBase, 7, "", []string{"gpus"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles(files)
	want := "nsenter --mount=/proc/self/fd/3 --pid=/proc/self/fd/4 --net=/proc/self/fd/5 --ipc=/proc/self/fd/6 " +
		"--uts=/proc/self/fd/7 --root=/proc/self/fd/8 --wd=/proc/self/fd/9 -- /criu-bundle/" + gpushareToolName + " gpus"
	if got := strings.Join(cmd.Args, " "); got != want {
		t.Fatalf("command %q, want %q", got, want)
	}
	if len(files) != 7 {
		t.Fatalf("%d descriptors", len(files))
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

// The tool is not run as the workload for a workload the agent must not
// enter, whose bundle it cannot trust, or whose user it cannot read; and
// nothing is left open.
func TestGPUShareToolCommandAsWorkloadRefuses(t *testing.T) {
	other := filepath.Join(t.TempDir(), "user")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	admin := strings.Replace(workerStatus, capsWorker, capsAdmin, 1)
	for name, c := range map[string]struct {
		user, mountinfo, status string
		noTool                  bool
		want                    string
	}{
		"own user namespace":  {other, bundleRO, workerStatus, false, "user namespace of its own"},
		"CAP_SYS_ADMIN":       {"/proc/self/ns/user", bundleRO, admin, false, "CAP_SYS_ADMIN"},
		"bundle writable":     {"/proc/self/ns/user", bundleRW, workerStatus, false, "not mounted read-only"},
		"no bundle":           {"/proc/self/ns/user", "22 1 0:1 / / rw - overlay overlay rw\n", workerStatus, false, "no node bundle mounted"},
		"no user":             {"/proc/self/ns/user", bundleRO, "Name:\tvllm\nCapEff:\t" + capsWorker + "\n", false, "no Uid/Gid"},
		"tool not in version": {"/proc/self/ns/user", bundleRO, workerStatus, true, "open the tool of bundle criu-bundle"},
	} {
		procBase, versions := gpuToolWorkload(t, c.user, c.mountinfo, c.status)
		if c.noTool {
			if err := os.Remove(filepath.Join(versions, "criu-bundle", gpushareToolName)); err != nil {
				t.Fatal(err)
			}
		}
		before := openFDs(t)
		cmd, files, err := gpushareToolCommand(context.Background(), procBase, 7, versions, []string{"gpus"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
		if cmd != nil || files != nil {
			t.Errorf("%s: returned a command or %d descriptors", name, len(files))
		}
		if after := openFDs(t); after != before {
			t.Errorf("%s: %d descriptors left open", name, after-before)
		}
	}
}

// The agent refuses to start with the setting when the launcher is not an
// executable file.
func TestGPUShareLauncherUsable(t *testing.T) {
	dir := t.TempDir()
	exe, plain := filepath.Join(dir, "exe"), filepath.Join(dir, "plain")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gpushareLauncherUsable(exe); err != nil {
		t.Errorf("an executable: %v", err)
	}
	for _, p := range []string{filepath.Join(dir, "missing"), plain, dir} {
		if err := gpushareLauncherUsable(p); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}
