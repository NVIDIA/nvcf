// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Pod holds. A group capture stops the whole instance at one moment
// (criu_group.go): once its GPU ranks are quiesced, every container of
// every member pod is frozen, so nothing of the instance keeps time while
// the ranks save their GPU memory and the members are dumped. An etcd
// that kept running would expire the ranks' leases; a sidecar that kept
// running would count the frozen engine as failed and kill it (a Dynamo
// gang-health watchdog gives up after about 15 s).
//
// A hold freezes a pod's containers through their cgroup v2 freezers,
// except the one a GPU rank's agent is saving (it runs until its dump). A
// companion's dumped container is frozen too and thawed just before its
// dump, which stops it again. The hold ends with the member's capture;
// a hold is also written down, so an agent that restarts thaws what it
// froze.
//
// A rank's container keeps only its GPU processes running: gpushare
// suspends them, rank by rank, over seconds to minutes. Its other
// processes (vLLM's EngineCore, the Dynamo runtime and its health canary)
// would go on starting work the suspended ranks never run: a step begun
// after the freeze point is captured half done, and the restored engine
// waits for its result for good (seen 2026-10-17). So the hold moves them
// into a frozen child cgroup of the container (quietCgroup) and moves them
// back as the dump starts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

// podHoldTimeout ends a hold whose capture never ended it: its driver
// was lost.
const podHoldTimeout = criuGroupCaptureTimeout

// podHoldFreezeWait bounds the wait for a cgroup to report frozen.
const podHoldFreezeWait = 10 * time.Second

// podHold is one pod's hold.
type podHold struct {
	mu     sync.Mutex
	frozen []string // cgroup directories the hold froze
	target string   // the dumped container's directory, while frozen
	quiet  string   // a kept container's child cgroup of frozen processes
	// frozenAt is when target was frozen (frozen_clock.go); zero when the
	// hold left it running.
	frozenAt clocks
	record   string // the hold's record, for an agent that restarts
	timer    *time.Timer
}

type podHolds struct {
	mu sync.Mutex
	m  map[string]*podHold
}

// holdKey names one member's hold in one capture.
func holdKey(session, ns, pod string) string { return freezeKey(session, ns, pod) }

// cgroupRoot is the host's cgroup v2 hierarchy as the agent sees it.
func cgroupRoot() string {
	if _, err := os.Stat("/host/sys/fs/cgroup/cgroup.controllers"); err == nil {
		return "/host/sys/fs/cgroup"
	}
	return "/sys/fs/cgroup"
}

// processCgroup returns the cgroup v2 path of host pid pid ("/kubepods/...").
func processCgroup(procBase string, pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(procBase, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok && rest != "" {
			return rest, nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry for pid %d", pid)
}

// podHoldDirs returns the cgroups to freeze to hold the pod whose
// container's cgroup is target: every container cgroup of the pod (the
// children of its pod cgroup), target included unless keep.
func podHoldDirs(target string, keep bool) ([]string, error) {
	podDir := filepath.Dir(target)
	entries, err := os.ReadDir(podDir)
	if err != nil {
		return nil, fmt.Errorf("read pod cgroup %s: %w", podDir, err)
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(podDir, e.Name())
		if _, err := os.Stat(filepath.Join(d, "cgroup.freeze")); err != nil {
			continue
		}
		if keep && d == target {
			continue
		}
		dirs = append(dirs, d)
	}
	return dirs, nil
}

// setFrozen freezes or thaws cgroup dir. A freeze waits, bounded, for
// the cgroup to report every task stopped.
func setFrozen(dir string, frozen bool) error {
	v := "0"
	if frozen {
		v = "1"
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.freeze"), []byte(v), 0o644); err != nil {
		return err
	}
	if !frozen {
		return nil
	}
	for deadline := time.Now().Add(podHoldFreezeWait); ; {
		b, err := os.ReadFile(filepath.Join(dir, "cgroup.events"))
		if err != nil || strings.Contains(string(b), "frozen 1") {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup %s not frozen after %s", dir, podHoldFreezeWait)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *podHolds) get(key string) *podHold {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.m[key]
}

// holdPod holds the pod of container (host pid pid): it freezes the pod's
// containers, the container itself too unless keep.
func (a *Agent) holdPod(key string, pid int, keep bool, log *logrus.Entry) error {
	cg, err := processCgroup(hostProcBase(), pid)
	if err != nil {
		return err
	}
	return a.holdCgroups(key, filepath.Join(cgroupRoot(), cg), keep, log)
}

// holdCgroups holds the pod whose container's cgroup is target.
func (a *Agent) holdCgroups(key, target string, keep bool, log *logrus.Entry) error {
	dirs, err := podHoldDirs(target, keep)
	if err != nil {
		return err
	}
	a.holds.mu.Lock()
	if a.holds.m == nil {
		a.holds.m = map[string]*podHold{}
	}
	if a.holds.m[key] != nil {
		a.holds.mu.Unlock()
		return nil // held already
	}
	h := &podHold{record: a.holdRecordPath(key)}
	a.holds.m[key] = h
	a.holds.mu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	if err := writeHoldRecord(h.record, dirs); err != nil {
		log.WithError(err).Warn("pod hold: cannot record the hold; an agent restart would not thaw it")
	}
	var errs []error
	for _, d := range dirs {
		if err := setFrozen(d, true); err != nil {
			errs = append(errs, err)
		}
		h.frozen = append(h.frozen, d)
		if d == target {
			h.target = d
			if c, cerr := readClocks(); cerr == nil {
				h.frozenAt = c
			}
		}
	}
	if keep {
		if q, qerr := quietContainer(target, log); qerr != nil {
			log.WithError(qerr).Warn("pod hold: cannot stop the rank's processes without GPU state; they run until the dump")
		} else if q != "" {
			h.quiet = q
			if err := writeHoldRecord(h.record, append(append([]string{}, dirs...), q)); err != nil {
				log.WithError(err).Warn("pod hold: cannot record the hold; an agent restart would not thaw it")
			}
		}
	}
	h.timer = time.AfterFunc(podHoldTimeout, func() {
		log.Warn("pod hold: the capture never ended it; thawing the pod")
		a.releaseHold(key, log)
	})
	log.WithFields(logrus.Fields{"containers": len(dirs), "keep": keep}).Info("pod hold: pod frozen for the capture")
	return errors.Join(errs...)
}

// releaseQuiet puts a kept container's stopped processes back as its dump
// starts (the dump stops them again).
func (a *Agent) releaseQuiet(key string, log *logrus.Entry) {
	h := a.holds.get(key)
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.quiet != "" {
		unquietContainer(h.quiet, log)
		h.quiet = ""
	}
}

// thawHoldTarget thaws the dumped container of a hold just before its
// dump (the dump stops it again).
func (a *Agent) thawHoldTarget(key string, log *logrus.Entry) {
	h := a.holds.get(key)
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.target == "" {
		return
	}
	if err := setFrozen(h.target, false); err != nil {
		log.WithError(err).Warn("pod hold: cannot thaw the container to dump")
		return
	}
	h.target = ""
}

// heldTargetDir is the cgroup of a hold's dumped container while it is
// frozen (a companion's), "" otherwise. A dump of it from outside seizes
// its tasks straight from the frozen cgroup (criu --freeze-cgroup), so the
// container never runs between the freeze point and its dump.
func (a *Agent) heldTargetDir(key string) string {
	h := a.holds.get(key)
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.target
}

// heldTargetSince is when the hold froze its dumped container, if it did.
func (a *Agent) heldTargetSince(key string) (clocks, bool) {
	h := a.holds.get(key)
	if h == nil {
		return clocks{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.frozenAt, h.frozenAt != (clocks{})
}

// releaseHold thaws every container a hold froze; a key without a hold
// is a no-op.
func (a *Agent) releaseHold(key string, log *logrus.Entry) {
	a.holds.mu.Lock()
	h := a.holds.m[key]
	delete(a.holds.m, key)
	a.holds.mu.Unlock()
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
	}
	if h.quiet != "" {
		unquietContainer(h.quiet, log)
		h.quiet = ""
	}
	thawAll(h.frozen, log)
	_ = os.Remove(h.record)
	log.WithField("containers", len(h.frozen)).Info("pod hold: pod thawed")
}

func thawAll(dirs []string, log *logrus.Entry) {
	for _, d := range dirs {
		if filepath.Base(d) == quietCgroup {
			unquietContainer(d, log)
			continue
		}
		if err := setFrozen(d, false); err != nil && !os.IsNotExist(err) {
			log.WithError(err).WithField("cgroup", d).Warn("pod hold: cannot thaw a container")
		}
	}
}

func (a *Agent) holdRecordDir() string { return filepath.Join(a.config.CheckpointDir, ".pod-holds") }

func (a *Agent) holdRecordPath(key string) string {
	return filepath.Join(a.holdRecordDir(), strings.NewReplacer("/", "_").Replace(key)+".json")
}

func writeHoldRecord(path string, dirs []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(dirs)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// recoverPodHolds thaws the pods an earlier run of the agent held: their
// captures ended with it.
func (a *Agent) recoverPodHolds(log *logrus.Entry) {
	entries, err := os.ReadDir(a.holdRecordDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(a.holdRecordDir(), e.Name())
		var dirs []string
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &dirs) == nil {
			thawAll(dirs, log)
			log.WithField("hold", e.Name()).Warn("pod hold: thawed a pod held by an earlier run of the agent")
		}
		_ = os.Remove(p)
	}
}

// holdMember holds a member pod of capture session (the freeze handler's
// hold phase): it finds the member's container and freezes its pod.
func (a *Agent) holdMember(ctx context.Context, session, ns, pod, container string, keep bool) error {
	info, err := a.runtime.FindContainerByPod(ctx, ns, pod, container)
	if err != nil {
		return fmt.Errorf("find container %s of %s/%s: %w", container, ns, pod, err)
	}
	log := a.log.WithFields(logrus.Fields{"pod": ns + "/" + pod, "session": session})
	return a.holdPod(holdKey(session, ns, pod), int(info.PID), keep, log)
}

// quietCgroup is a kept container's child cgroup that holds its stopped
// processes.
const quietCgroup = "nvsnap-quiet"

// usesGPU reports whether host pid pid holds GPU state.
var usesGPU = func(pid int) bool {
	dir := filepath.Join(hostProcBase(), strconv.Itoa(pid))
	if _, gpu, _ := scanMaps(filepath.Join(dir, "maps")); gpu {
		return true
	}
	return holdsNvidiaFD(filepath.Join(dir, "fd"))
}

// quietContainer stops the processes of container cgroup target that hold
// no GPU state: it moves them into a frozen child cgroup, which it returns
// ("" when there are none).
func quietContainer(target string, log *logrus.Entry) (string, error) {
	pids, err := cgroupPIDs(target)
	if err != nil {
		return "", err
	}
	var move []int
	for _, pid := range pids {
		if !usesGPU(pid) {
			move = append(move, pid)
		}
	}
	if len(move) == 0 {
		return "", nil
	}
	q := filepath.Join(target, quietCgroup)
	if err := os.Mkdir(q, 0o755); err != nil && !os.IsExist(err) {
		return "", err
	}
	for _, pid := range move {
		if err := addToCgroup(q, pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			unquietContainer(q, log)
			return "", fmt.Errorf("move pid %d: %w", pid, err)
		}
	}
	if err := setFrozen(q, true); err != nil {
		unquietContainer(q, log)
		return "", err
	}
	log.WithField("processes", len(move)).Info("pod hold: the rank's processes without GPU state are stopped")
	return q, nil
}

// unquietContainer thaws a kept container's stopped processes and moves
// them back into it.
func unquietContainer(q string, log *logrus.Entry) {
	if err := setFrozen(q, false); err != nil && !os.IsNotExist(err) {
		log.WithError(err).Warn("pod hold: cannot thaw the rank's stopped processes")
	}
	pids, err := cgroupPIDs(q)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("pod hold: cannot list the rank's stopped processes")
		}
		return
	}
	for _, pid := range pids {
		if err := addToCgroup(filepath.Dir(q), pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			log.WithError(err).WithField("pid", pid).Warn("pod hold: cannot move a stopped process back into its container")
		}
	}
	// An empty cgroup is removed with rmdir; its interface files go with it.
	_ = syscall.Rmdir(q)
}

// addToCgroup moves pid into cgroup dir.
func addToCgroup(dir string, pid int) error {
	f, err := os.OpenFile(filepath.Join(dir, "cgroup.procs"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strconv.Itoa(pid) + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
