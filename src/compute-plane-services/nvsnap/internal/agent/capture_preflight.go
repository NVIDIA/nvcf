// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Capture preflight. Before an instance capture is claimed, the agent
// checks every member pod for what would make the capture fail or the
// restore misbehave, and reports all of it at once: a workload shape the
// capture has not met before shows up as findings here, not as one failed
// capture after another (each of which stops the instance and uses one of
// its configuration's attempts).
//
// Spec checks read the pod (probes, sidecars, the pid namespace); host
// checks run on the member's node and read its container's processes
// (deleted files the dump must save, the memory it dumps). A finding that
// blocks skips the capture before it is claimed; the others are logged.
// GET /v1/capture-preflight/{namespace}/{pod} reports on the instance whose
// rank 0 is pod, on demand.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/modelvolume"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// Preflight severities.
const (
	preflightBlock = "block" // the capture would fail or harm the instance
	preflightWarn  = "warn"  // the capture may fail, or costs more than usual
	preflightInfo  = "info"
)

// largeGhostBytes is the deleted-file size from which a finding says how
// much it adds to the dump.
const largeGhostBytes = 1 << 30

// PreflightFinding is one thing the preflight found about a member.
type PreflightFinding struct {
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
	Severity  string `json:"severity"`
	Check     string `json:"check"`
	Detail    string `json:"detail"`
}

// PreflightReport is the preflight of one instance.
type PreflightReport struct {
	Namespace  string             `json:"namespace"`
	Ranks      []string           `json:"ranks"`
	Companions []string           `json:"companions"`
	Findings   []PreflightFinding `json:"findings"`
	Capturable bool               `json:"capturable"`
}

// blocking returns the findings that block the capture.
func blocking(fs []PreflightFinding) []PreflightFinding {
	var out []PreflightFinding
	for _, f := range fs {
		if f.Severity == preflightBlock {
			out = append(out, f)
		}
	}
	return out
}

// podPreflight is the spec checks of member pod, whose container is
// dumped; rank marks a GPU rank.
func (a *Agent) podPreflight(pod *corev1.Pod, container string, rank bool) []PreflightFinding {
	var fs []PreflightFinding
	add := func(c, sev, check, detail string) {
		fs = append(fs, PreflightFinding{Pod: pod.Name, Container: c, Severity: sev, Check: check, Detail: detail})
	}
	if err := freezeTolerated([]*corev1.Pod{pod}, a.config.Webhook.CaptureLivenessBudget); err != nil {
		detail := err.Error() + "; the kubelet would restart it during the capture"
		if !rank {
			// The webhook widens a companion's probes only when it or its
			// namespace is opted into capture.
			detail += " (opt the companion in: label its namespace " + criuCaptureAnnotation + "=true, or annotate the pod, and recreate it)"
		}
		add("", preflightBlock, "liveness", detail)
	}
	if rank && !a.gpushareEnabledFor(pod) {
		add(container, preflightBlock, "gpushare", "a GPU rank without the gpushare library: its GPU memory cannot be saved")
	}
	if pod.Spec.ShareProcessNamespace != nil && *pod.Spec.ShareProcessNamespace {
		add("", preflightInfo, "shared-pid-namespace", "the containers share one pid namespace: the restore recreates the dumped container's pids beside the sidecars' own")
	}
	for i := range pod.Spec.InitContainers {
		if c := &pod.Spec.InitContainers[i]; webhook.NativeSidecar(c) {
			add(c.Name, preflightInfo, "native-sidecar", "runs for the pod's life beside the main container; it keeps running in the restore placeholder")
		}
	}
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		for kind, p := range map[string]*corev1.Probe{"liveness": c.LivenessProbe, "readiness": c.ReadinessProbe, "startup": c.StartupProbe} {
			if p == nil || p.Exec == nil {
				continue
			}
			sev, why := preflightInfo, "runs a process in its container; it waits while the pod is held"
			if c.Name == container {
				sev, why = preflightWarn, "runs a process inside the dumped container; one started just before the freeze is in the dump's way"
			}
			add(c.Name, sev, "exec-probe", kind+" probe "+why)
		}
	}
	return fs
}

// hostPreflight is the host checks of the container of a pod on this
// node: the deleted files its processes hold (the dump saves their
// contents, up to the ghost limit) and the memory it dumps.
func (a *Agent) hostPreflight(ctx context.Context, ns, pod, container string) ([]PreflightFinding, error) {
	info, err := a.runtime.FindContainerByPod(ctx, ns, pod, container)
	if err != nil {
		return nil, fmt.Errorf("container %s of %s/%s: %w", container, ns, pod, err)
	}
	if info == nil || info.PID == 0 {
		return nil, fmt.Errorf("container %s of %s/%s not found", container, ns, pod)
	}
	procBase := hostProcBase()
	cg, err := processCgroup(procBase, int(info.PID))
	if err != nil {
		return nil, err
	}
	pids, err := cgroupPIDs(filepath.Join(cgroupRoot(), cg))
	if err != nil {
		return nil, err
	}
	ghosts, anon := map[string]ghostFile{}, int64(0)
	for _, pid := range pids {
		deletedFiles(procBase, pid, ghosts)
		anon += anonymousBytes(procBase, pid)
	}
	return ghostFindings(pod, container, ghosts, anon, a.ghostLimit()), nil
}

// ghostFile is a deleted file a container's processes hold.
type ghostFile struct {
	path       string
	allocated  int64 // what criu --ghost-limit is compared with
	apparently int64
	// readOnly: the file's mount is read-only in the process's mount
	// namespace. A restore recreates a ghost file beside its path, which
	// it cannot there: the capture would never restore. A node bundle
	// replaced under a running pod (an agent upgrade) leaves such files.
	readOnly bool
}

// ghostFindings turns what the host checks saw into findings.
func ghostFindings(pod, container string, ghosts map[string]ghostFile, anon, limit int64) []PreflightFinding {
	var fs []PreflightFinding
	for _, g := range ghosts {
		switch {
		case g.readOnly:
			fs = append(fs, PreflightFinding{Pod: pod, Container: container, Severity: preflightBlock, Check: "ghost-file",
				Detail: fmt.Sprintf("deleted file %s is on a read-only mount: a restore cannot recreate it (a file replaced under the running pod; restarting the pod clears it)", g.path)})
		case g.allocated > limit:
			fs = append(fs, PreflightFinding{Pod: pod, Container: container, Severity: preflightBlock, Check: "ghost-file",
				Detail: fmt.Sprintf("deleted file %s holds %s, over the dump's ghost limit (%s; agent.criuGhostLimit)", g.path, gib(g.allocated), gib(limit))})
		case g.allocated >= largeGhostBytes:
			fs = append(fs, PreflightFinding{Pod: pod, Container: container, Severity: preflightWarn, Check: "ghost-file",
				Detail: fmt.Sprintf("deleted file %s holds %s; the dump saves it whole", g.path, gib(g.allocated))})
		}
	}
	fs = append(fs, PreflightFinding{Pod: pod, Container: container, Severity: preflightInfo, Check: "memory",
		Detail: fmt.Sprintf("anonymous memory to dump: %s", gib(anon))})
	return fs
}

func gib(n int64) string { return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30)) }

// cgroupPIDs lists the processes of cgroup dir.
func cgroupPIDs(dir string) ([]int, error) {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, nil
}

// deletedFiles adds the deleted regular files process pid holds open or
// mapped to ghosts, keyed by device and inode. memfd files are not ghost
// files (criu dumps them as memfd) and are left out.
func deletedFiles(procBase string, pid int, ghosts map[string]ghostFile) {
	base := filepath.Join(procBase, strconv.Itoa(pid))
	mounts := readMountinfo(filepath.Join(base, "mountinfo"))
	see := func(link, target string) {
		if !strings.HasSuffix(target, " (deleted)") || strings.HasPrefix(target, "/memfd:") || !strings.HasPrefix(target, "/") {
			return
		}
		var st syscall.Stat_t
		if err := syscall.Stat(link, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
			return
		}
		key := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
		if _, ok := ghosts[key]; !ok {
			path := strings.TrimSuffix(target, " (deleted)")
			ghosts[key] = ghostFile{path: path, allocated: st.Blocks * 512, apparently: st.Size, readOnly: mountReadOnly(mounts, path)}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(base, "fd")); err == nil {
		for _, e := range ents {
			link := filepath.Join(base, "fd", e.Name())
			if t, err := os.Readlink(link); err == nil {
				see(link, t)
			}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(base, "map_files")); err == nil {
		for _, e := range ents {
			link := filepath.Join(base, "map_files", e.Name())
			if t, err := os.Readlink(link); err == nil {
				see(link, t)
			}
		}
	}
}

// anonymousBytes is the anonymous memory of process pid.
func anonymousBytes(procBase string, pid int) int64 {
	f, err := os.Open(filepath.Join(procBase, strconv.Itoa(pid), "smaps_rollup"))
	if err != nil {
		return 0
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Anonymous:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			return kb << 10
		}
	}
	return 0
}

// capturePreflight runs the spec checks of every member and the host
// checks on each member's node.
func (a *Agent) capturePreflight(ctx context.Context, ranks, companions []*corev1.Pod, companionContainers map[string]string, log *logrus.Entry) []PreflightFinding {
	type member struct {
		pod       *corev1.Pod
		container string
		rank      bool
	}
	members := make([]member, 0, len(ranks)+len(companions))
	for _, r := range ranks {
		members = append(members, member{r, gpuContainer(r), true})
	}
	for _, c := range companions {
		members = append(members, member{c, companionContainers[c.Name], false})
	}
	// The spec checks all run before any host check starts: the host
	// checks append from their goroutines, and the slice is theirs alone
	// (under mu) from then on.
	var fs []PreflightFinding
	for _, m := range members {
		fs = append(fs, a.podPreflight(m.pod, m.container, m.rank)...)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, m := range members {
		wg.Add(1)
		go func(m member) {
			defer wg.Done()
			host, err := a.fetchHostPreflight(ctx, m.pod, m.container)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				log.WithError(err).WithField("pod", m.pod.Name).Warn("capture preflight: cannot run the host checks of a member")
				fs = append(fs, PreflightFinding{Pod: m.pod.Name, Container: m.container, Severity: preflightWarn, Check: "host",
					Detail: "host checks did not run: " + err.Error()})
				return
			}
			fs = append(fs, host...)
		}(m)
	}
	wg.Wait()
	return fs
}

// fetchHostPreflight runs the host checks of a member on its node's agent.
func (a *Agent) fetchHostPreflight(ctx context.Context, pod *corev1.Pod, container string) ([]PreflightFinding, error) {
	base, err := a.peerAgentURL(ctx, pod.Spec.NodeName)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/v1/capture-preflight/pod/%s/%s?container=%s", base, url.PathEscape(pod.Namespace), url.PathEscape(pod.Name), url.QueryEscape(container))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := fabricHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var fs []PreflightFinding
	if err := json.Unmarshal(b, &fs); err != nil {
		return nil, err
	}
	return fs, nil
}

// podPreflightHandler serves the host checks of a pod on this node.
func (a *Agent) podPreflightHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	fs, err := a.hostPreflight(r.Context(), v["namespace"], v["pod"], r.URL.Query().Get("container"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fs)
}

// instancePreflightHandler reports on the instance whose rank 0 is the
// pod: its members as a capture would find them, and every finding.
func (a *Agent) instancePreflightHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	ctx := r.Context()
	log := a.log.WithFields(logrus.Fields{"pod": v["namespace"] + "/" + v["pod"], "op": "capture-preflight"})
	leader, err := a.kubeClient.CoreV1().Pods(v["namespace"]).Get(ctx, v["pod"], metav1.GetOptions{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	o, size, ok := podRank(leader, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation)
	if !ok || o != 0 {
		http.Error(w, "the pod is not rank 0 of an instance", http.StatusBadRequest)
		return
	}
	pods, err := a.kubeClient.CoreV1().Pods(leader.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ranks, err := instanceRanks(pods.Items, leader, size, modelvolume.CacheOrdinalAnnotation, modelvolume.CacheGroupSizeAnnotation, func(p *corev1.Pod) bool {
		return p.Annotations[webhook.CRIURestoreAnnotation] == "" && rootfsonly.IsPodReady(p)
	})
	if err != nil {
		http.Error(w, "the instance is not ready: "+err.Error(), http.StatusConflict)
		return
	}
	companions, containers, err := a.discoverCompanions(ctx, ranks, log)
	if err != nil {
		http.Error(w, "a companion cannot be captured: "+err.Error(), http.StatusConflict)
		return
	}
	rep := PreflightReport{Namespace: leader.Namespace, Findings: a.capturePreflight(ctx, ranks, companions, containers, log)}
	for _, p := range ranks {
		rep.Ranks = append(rep.Ranks, p.Name+"/"+gpuContainer(p))
	}
	for _, p := range companions {
		rep.Companions = append(rep.Companions, p.Name+"/"+containers[p.Name])
	}
	rep.Capturable = len(blocking(rep.Findings)) == 0
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rep)
}

// mountEntry is one line of a mountinfo: a mount point and whether it is
// read-only.
type mountEntry struct {
	point    string
	readOnly bool
}

// readMountinfo reads the mount points of a mount namespace; nil when it
// cannot.
func readMountinfo(path string) []mountEntry {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []mountEntry
	for _, line := range strings.Split(string(b), "\n") {
		// id parent major:minor root mount-point options ...
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		ro := false
		for _, o := range strings.Split(f[5], ",") {
			ro = ro || o == "ro"
		}
		out = append(out, mountEntry{point: unescapeMountinfo(f[4]), readOnly: ro})
	}
	return out
}

// mountReadOnly reports whether path's mount (the longest mount point
// above it; the last mounted wins) is read-only.
func mountReadOnly(mounts []mountEntry, path string) bool {
	best, ro := -1, false
	for _, m := range mounts {
		if (path == m.point || strings.HasPrefix(path, strings.TrimSuffix(m.point, "/")+"/") || m.point == "/") && len(m.point) >= best {
			best, ro = len(m.point), m.readOnly
		}
	}
	return ro
}
