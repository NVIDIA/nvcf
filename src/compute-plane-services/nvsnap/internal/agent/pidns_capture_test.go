// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The mounts of a plain pod's container, as the node shows them (GB300,
// containerd, 2026-10-08), trimmed of super options.
const containerMountinfo = `100 90 0:300 / / rw - overlay overlay rw
101 100 0:301 / /proc rw - proc proc rw
102 100 0:302 / /dev rw - tmpfs tmpfs rw
103 102 0:303 / /dev/pts rw - devpts devpts rw
104 102 0:304 / /dev/mqueue rw - mqueue mqueue rw
105 100 0:305 / /sys ro - sysfs sysfs ro
106 105 0:306 /kubepods.slice/x.scope /sys/fs/cgroup rw - cgroup2 cgroup rw
107 100 9:127 /kubelet/pods/u/etc-hosts /etc/hosts rw - xfs /dev/md127 rw
108 102 9:127 /kubelet/pods/u/containers/app/1 /dev/termination-log rw - xfs /dev/md127 rw
109 100 9:127 /containerd/sandboxes/s/hostname /etc/hostname rw - xfs /dev/md127 rw
110 102 0:307 / /dev/shm rw - tmpfs shm rw
111 100 0:308 / /run/secrets/kubernetes.io/serviceaccount ro - tmpfs tmpfs rw
112 101 0:301 /bus /proc/bus ro - proc proc rw
113 101 0:302 /null /proc/kcore rw - tmpfs tmpfs rw
114 100 9:127 /kubelet/pods/u/volumes/my\040vol /data rw - xfs /dev/md127 rw
115 102 0:5 /nvidia0 /dev/nvidia0 rw - devtmpfs udev rw
`

func TestPIDNSExternalMounts(t *testing.T) {
	got := pidNSExternalMounts(parseMountinfo(containerMountinfo))
	want := []string{"/sys/fs/cgroup", "/etc/hosts", "/dev/termination-log", "/etc/hostname", "/data", "/dev/nvidia0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("external mounts\\n got %v\\nwant %v", got, want)
	}
}

func TestPIDNSArgs(t *testing.T) {
	dump := strings.Join(pidNSDumpArgs(4242, "/proc", "/ck/x", "/criu-bundle/plugins", 4026538391,
		[]string{"/etc/hosts"}, []string{"dev[195/0]:nvidia0"}, true, true, false), " ")
	for _, want := range []string{"dump -t 4242 --root /proc/4242/root -D /ck/x", "--external net[4026538391]:extNetNs",
		"--external mnt[/etc/hosts]:/etc/hosts", "--external dev[195/0]:nvidia0", "--leave-running", "--tcp-established"} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump argv lacks %q: %s", want, dump)
		}
	}
	if strings.Contains(dump, "--libdir") {
		t.Error("a gpushare dump must not load the CUDA plugin")
	}
	restore := strings.Join(pidNSRestoreArgs("/checkpoints/x", []string{"/etc/hosts"}, false, false, "10.0.0.1=10.0.0.2"), " ")
	for _, want := range []string{"restore -D /checkpoints/x", "--root /", "--inherit-fd fd[3]:extNetNs",
		"--join-ns ipc:/proc/1/ns/ipc", "--external mnt[/etc/hosts]:/etc/hosts", "--restore-detached",
		"--inet-addr-map 10.0.0.1=10.0.0.2 --keep-network-lock", "--libdir"} {
		if !strings.Contains(restore, want) {
			t.Errorf("restore argv lacks %q: %s", want, restore)
		}
	}
	if strings.Contains(restore, "uts:") {
		t.Error("the restore must not join the placeholder's UTS namespace: its /proc/sys is read-only")
	}
}

func TestPIDNSMarker(t *testing.T) {
	dir := t.TempDir()
	if ok, _, err := readPIDNSMarkerOrFalse(dir); ok || err != nil {
		t.Fatalf("no marker: ok=%v err=%v", ok, err)
	}
	if err := writePIDNSMarker(dir, pidNSCapture{Mounts: []string{"/etc/hosts"}}); err != nil {
		t.Fatal(err)
	}
	ok, c, err := readPIDNSMarkerOrFalse(dir)
	if !ok || err != nil || !reflect.DeepEqual(c.Mounts, []string{"/etc/hosts"}) {
		t.Errorf("marker: ok=%v err=%v %+v", ok, err, c)
	}
}

func TestNestedInitHostPID(t *testing.T) {
	proc := t.TempDir()
	write := func(pid int, nspid string, ppid int) {
		d := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		status := "Name:\tx\nPPid:\t" + strconv.Itoa(ppid) + "\nNSpid:\t" + nspid + "\n"
		if err := os.WriteFile(filepath.Join(d, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(100, "100\t1", 50)      // the placeholder's pid 1
	write(200, "200\t7\t1", 999)  // a nested pid 1 of another placeholder
	write(300, "300\t9\t1", 100)  // the restored init, reparented to the placeholder
	write(301, "301\t10\t2", 300) // its child
	write(400, "400\t1", 50)      // another container's pid 1
	got, err := nestedInitHostPID(proc, 100)
	if err != nil || got != 300 {
		t.Errorf("nested init = %d, %v; want 300", got, err)
	}
	if _, err := nestedInitHostPID(proc, 400); err == nil {
		t.Error("found a nested init where none was restored")
	}
}
