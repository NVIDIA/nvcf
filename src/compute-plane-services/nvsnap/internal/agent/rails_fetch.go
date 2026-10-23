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

// The rails tier of EnsureLocal: copy a checkpoint from nodes that hold it
// over the nodes' RDMA NICs with bedrock-peer, before the shared filesystem,
// L2 and HTTP peer tiers.
//
// The agent DaemonSet never holds the NICs. Rails pods claim every RDMA
// NIC of their node through DRA: a serve pod on each source node, shared by
// every fetch from it (rails_serve.go), and a fetch pod on this node, which
// writes into the checkpoint directory under the fetch marker EnsureLocal
// already set. The copy lands file by file in the existing directory, never
// by renaming the directory, because a placeholder may hold its
// subdirectories bind-mounted.
//
// A checkpoint under MinSize is left to the HTTP peers, which move it
// before a rails pod would have started; it then holds no NICs a large copy
// waits for. Sources that are not fetching themselves come first: a node
// runs one rails pod at a time, so serving would hold up its own fetch.
//
// Any failure (no source pod scheduled, no NICs free, bedrock-peer exiting
// non-zero) deletes the fetch pod and falls through to the next tier.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// RailsConfig configures the rails tier. It is off unless Image is set.
type RailsConfig struct {
	// Image is the bedrock-peer image, pinned by digest. Empty disables
	// the tier.
	Image string
	// Namespace is where the serve and fetch pods run: the namespace of
	// ClaimTemplate and SecretName.
	Namespace string
	// ClaimTemplate is the ResourceClaimTemplate that claims every RDMA
	// NIC of a node.
	ClaimTemplate string
	// SecretName is the Secret whose key "secret" both ends present
	// (BEDROCK_PEER_SECRET).
	SecretName string
	// PullSecrets are imagePullSecret names for the pods.
	PullSecrets []string
	// Port is the serve pods' bootstrap port.
	Port int
	// Connections is the fetch's parallel connections.
	Connections int
	// MaxSources caps the source nodes one fetch reads from.
	MaxSources int
	// MinSize is the smallest checkpoint fetched over the rails, as a
	// quantity ("16Gi"); a smaller one goes to the next tier. Empty: 16 GiB.
	MinSize string
	// ServeMaxFetchers is how many fetches a serve pod serves at once.
	ServeMaxFetchers int
	// ServeCPU and ServeMemory are the serve pod's requests and limits,
	// so a copy cannot starve the workload on a source node.
	ServeCPU, ServeMemory string
	// FetchCPU and FetchMemory are the fetch pod's requests and limits.
	FetchCPU, FetchMemory string
	// KeepFreePercent is the share of the checkpoint filesystem a copy
	// must leave free.
	KeepFreePercent int
}

const (
	railsDefaultPort        = 9890
	railsDefaultConnections = 32
	railsDefaultMaxSources  = 2
	railsDefaultKeepFree    = 10
	// railsDefaultMinBytes: the HTTP peers move 16 GiB in about 3 s; a
	// rails pod takes 10 s or more to start (docs/design/
	// RAILS-FETCH-CONCURRENCY.md, step 1).
	railsDefaultMinBytes    = 16 << 30
	railsDefaultMaxFetchers = 4

	// railsFetchFloorRate is the slowest copy the tier waits for: well
	// under a disk's write rate, so only a stuck copy reaches it.
	railsFetchFloorRate = 1 << 30 // bytes per second
	// railsFetchSlack covers the fetch's own start and its last syncs.
	railsFetchSlack = 5 * time.Minute

	railsMountPath = "/data"
	// railsOwnName prefixes bedrock-peer's own entries in a destination:
	// its staging directories and content record.
	railsOwnName = ".bedrock-peer-"

	railsLabelComponent = "app.kubernetes.io/component"
	railsLabelFor       = "nvsnap.io/rails-for"
	railsAnnoCheckpoint = "nvsnap.io/checkpoint"
)

// railsServeReadyTimeout bounds a serve pod's scheduling, NIC allocation,
// image pull and listen. A pod still not Ready (the node's NICs held by a
// workload) is dropped as a source. A variable for tests.
var railsServeReadyTimeout = 3 * time.Minute

// railsFetchStartTimeout bounds the fetch pod's start on this node: a pod
// that cannot get this node's NICs gives up then, not after the copy's
// whole budget. A variable for tests.
var railsFetchStartTimeout = 3 * time.Minute

// errRailsUnavailable marks a rails fetch that did not run: the tier is
// off, no source is reachable, or the pods could not start. EnsureLocal
// falls through on any error; this one is logged at Info.
var errRailsUnavailable = errors.New("rails tier unavailable")

// Validate checks the values the pods are built from, so a typo fails the
// agent's start rather than a restore.
func (c RailsConfig) Validate() error {
	if c.Image == "" {
		return nil
	}
	for name, v := range map[string]string{"serve CPU": c.ServeCPU, "serve memory": c.ServeMemory, "fetch CPU": c.FetchCPU, "fetch memory": c.FetchMemory, "min size": c.MinSize} {
		if v == "" {
			continue
		}
		if _, err := resource.ParseQuantity(v); err != nil {
			return fmt.Errorf("rails %s %q: %w", name, v, err)
		}
	}
	if c.Namespace == "" || c.ClaimTemplate == "" || c.SecretName == "" {
		return errors.New("rails: namespace, claim template and secret are required")
	}
	return nil
}

func (a *Agent) railsEnabled() bool {
	return a.config.Rails.Image != "" && a.kubeClient != nil && a.config.NodeName != ""
}

func (c RailsConfig) port() int {
	if c.Port > 0 {
		return c.Port
	}
	return railsDefaultPort
}

func (c RailsConfig) connections() int {
	if c.Connections > 0 {
		return c.Connections
	}
	return railsDefaultConnections
}

func (c RailsConfig) maxSources() int {
	if c.MaxSources > 0 {
		return c.MaxSources
	}
	return railsDefaultMaxSources
}

func (c RailsConfig) minBytes() int64 {
	if q, err := resource.ParseQuantity(c.MinSize); err == nil && q.Value() > 0 {
		return q.Value()
	}
	return railsDefaultMinBytes
}

func (c RailsConfig) serveMaxFetchers() int {
	if c.ServeMaxFetchers > 0 {
		return c.ServeMaxFetchers
	}
	return railsDefaultMaxFetchers
}

func (c RailsConfig) keepFreePercent() int {
	if c.KeepFreePercent > 0 && c.KeepFreePercent < 100 {
		return c.KeepFreePercent
	}
	return railsDefaultKeepFree
}

// railsFetchResult is the line `bedrock-peer fetch --json` prints.
type railsFetchResult struct {
	OK         bool    `json:"ok"`
	ExitCode   int     `json:"exit_code"`
	Error      *string `json:"error"`
	ErrorKind  *string `json:"error_kind"`
	Files      int64   `json:"files"`
	Bytes      int64   `json:"bytes"`
	Seconds    float64 `json:"seconds"`
	Retries    int64   `json:"retries"`
	Failovers  int64   `json:"failovers"`
	Source     string  `json:"source"`
	Attributes *string `json:"attributes"`
	Version    string  `json:"version"`
	Commit     string  `json:"commit"`
}

// fetchOverRails copies checkpointID into localDir from the peers that
// hold it. localDir is empty of files and under the fetch marker.
func (a *Agent) fetchOverRails(ctx context.Context, checkpointID, localDir string, peers []peerInfo) error {
	cfg := a.config.Rails
	log := a.log.WithField("checkpoint_id", checkpointID)

	sources, size := a.railsSources(ctx, checkpointID, peers, log)
	if len(sources) == 0 {
		return fmt.Errorf("%w: no other node lists %s", errRailsUnavailable, checkpointID)
	}
	if size < cfg.minBytes() {
		return fmt.Errorf("%w: %d bytes is under the rails minimum of %d; the peers move it sooner", errRailsUnavailable, size, cfg.minBytes())
	}
	if err := checkFreeSpace(localDir, size, cfg.keepFreePercent()); err != nil {
		return fmt.Errorf("%w: %w", errRailsUnavailable, err)
	}

	run := railsRun{
		cfg:          cfg,
		forNode:      a.config.NodeName,
		checkpointID: checkpointID,
		hostDir:      filepath.Join(a.checkpointHostDir(), checkpointID),
		owner:        a.railsOwnerOn(ctx, a.config.NodeName),
	}
	created := make([]string, 0, 1)
	defer func() {
		// The fetch pod holds every RDMA NIC of this node: free them even
		// when the restore's context is gone. Serve pods are shared; their
		// nodes' agents reap them (rails_serve.go).
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		a.deleteRailsPods(dctx, created)
		removeRailsEntries(localDir)
	}()

	// The claim covers the wait for the serve pods; the fetch pod keeps
	// them from then on.
	until := time.Now().Add(railsServeReadyTimeout + railsServeClaimSlack)
	var serving, servingNodes []string
	for _, node := range sources {
		name, err := a.acquireServePod(ctx, node, until)
		if err != nil {
			log.WithError(err).WithField("source_node", node).Warn("rails: cannot get the serve pod; dropping that source")
			continue
		}
		serving = append(serving, name)
		servingNodes = append(servingNodes, node)
	}
	// One deadline for all serve pods: they start in parallel.
	readyCtx, cancelReady := context.WithTimeout(ctx, railsServeReadyTimeout)
	defer cancelReady()
	var addrs, sourceLabels []string
	for i, name := range serving {
		ip, err := a.waitRailsServeReady(readyCtx, name)
		if err != nil {
			log.WithError(err).WithField("pod", name).Warn("rails: serve pod not ready; dropping that source")
			continue
		}
		addrs = append(addrs, fmt.Sprintf("%s:%d", ip, cfg.port()))
		sourceLabels = append(sourceLabels, railsLabelValue(servingNodes[i]))
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: no serve pod became ready", errRailsUnavailable)
	}

	// This node's own serve pod, idle, would hold the NICs the fetch pod
	// needs.
	a.reapServePod(ctx, time.Now())
	timeout := railsFetchTimeout(size)
	pod, err := a.kubeClient.CoreV1().Pods(cfg.Namespace).Create(ctx, run.fetchPod(addrs, sourceLabels, timeout), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("%w: create the fetch pod: %w", errRailsUnavailable, err)
	}
	created = append(created, pod.Name)
	log.WithFields(map[string]interface{}{"sources": addrs, "bytes": size, "pod": pod.Name}).Info("rails: fetch started")

	res, err := a.waitRailsFetch(ctx, pod.Name, timeout)
	if err != nil {
		return err
	}
	log.WithFields(map[string]interface{}{
		"files": res.Files, "bytes": res.Bytes, "seconds": res.Seconds,
		"gib_per_s": gibPerSecond(res.Bytes, res.Seconds), "retries": res.Retries,
		"failovers": res.Failovers, "source": res.Source, "bedrock_peer": res.Version + " " + res.Commit,
	}).Info("rails: fetch complete")
	// localDir is CheckpointDir joined with an id EnsureLocal validated.
	if fi, err := os.Stat(filepath.Join(localDir, "inventory.img")); err != nil || fi.Size() == 0 { //nolint:gosec // see above
		return fmt.Errorf("rails fetch reported success but %s has no inventory.img", localDir)
	}
	return nil
}

// checkpointHostDir is the host path behind CheckpointDir: the pods mount
// it by hostPath.
func (a *Agent) checkpointHostDir() string {
	if a.config.CheckpointHostDir != "" {
		return a.config.CheckpointHostDir
	}
	return a.config.CheckpointDir
}

// railsSources picks the nodes to fetch checkpointID from, at most
// MaxSources, and its size. Candidates are the peers other than this node
// whose agent lists the checkpoint. A node running a fetch of its own is
// taken only when no other node lists it: its one rails pod at a time is
// busy, and serving would hold up its fetch. Among the rest, a node whose
// serve pod is Ready comes first.
func (a *Agent) railsSources(ctx context.Context, checkpointID string, peers []peerInfo, log *logrus.Entry) (sources []string, size int64) {
	fetching, serving, err := a.railsNodeStates(ctx)
	if err != nil {
		log.WithError(err).Info("rails: cannot read the nodes' rails pods; taking the sources in catalog order")
	}
	seen := map[string]bool{a.config.NodeName: true}
	var candidates []peerInfo
	for _, p := range peers {
		if p.NodeName == "" || seen[p.NodeName] || p.AgentURL == a.selfAgentURL() {
			continue
		}
		seen[p.NodeName] = true
		candidates = append(candidates, p)
	}
	rank := func(p peerInfo) int {
		switch v := railsLabelValue(p.NodeName); {
		case fetching[v]:
			return 2
		case serving[v]:
			return 0
		default:
			return 1
		}
	}
	slices.SortStableFunc(candidates, func(x, y peerInfo) int { return rank(x) - rank(y) })
	limit := a.config.Rails.maxSources()
	for _, p := range candidates {
		if len(sources) == limit || (len(sources) > 0 && rank(p) == 2) {
			break
		}
		m, err := railsPeerManifest(ctx, p.AgentURL, checkpointID)
		if err != nil {
			log.WithError(err).WithField("peer_node", p.NodeName).Info("rails: peer does not list the checkpoint; not a source")
			continue
		}
		sources = append(sources, p.NodeName)
		size = m.TotalSize
	}
	return sources, size
}

// railsPeerManifest is getPeerManifest with a deadline, refusing an empty
// listing.
func railsPeerManifest(ctx context.Context, peerURL, checkpointID string) (*peerManifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m, err := getPeerManifest(ctx, peerURL, checkpointID)
	if err != nil {
		return nil, err
	}
	if m.TotalSize <= 0 {
		return nil, fmt.Errorf("peer lists %s with no data", checkpointID)
	}
	return m, nil
}

// checkFreeSpace refuses a copy of size bytes that would leave less than
// keepPercent of dir's filesystem free.
func checkFreeSpace(dir string, size int64, keepPercent int) error {
	avail, total, err := diskSpace(dir)
	if err != nil {
		return err
	}
	return freeSpaceAllows(avail, total, size, keepPercent, dir)
}

// diskSpace returns the available and total bytes of dir's filesystem; a
// variable for tests, which must not depend on the disk they run on.
var diskSpace = func(dir string) (avail, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), nil //nolint:gosec // block size is positive
}

func freeSpaceAllows(avail, total uint64, size int64, keepPercent int, dir string) error {
	keep := total / 100 * uint64(keepPercent) //nolint:gosec // percent checked to be 1..99
	if size < 0 || uint64(size) > avail || avail-uint64(size) < keep {
		return fmt.Errorf("copying %d bytes would leave less than %d%% of %s free (%d of %d bytes free)", size, keepPercent, dir, avail, total)
	}
	return nil
}

func railsFetchTimeout(size int64) time.Duration {
	return time.Duration(size/railsFetchFloorRate)*time.Second + railsFetchSlack
}

func gibPerSecond(bytes int64, seconds float64) string {
	if seconds <= 0 {
		return "n/a"
	}
	return strconv.FormatFloat(float64(bytes)/(1<<30)/seconds, 'f', 2, 64)
}

// removeRailsEntries removes what bedrock-peer keeps in a destination's
// top directory (its content record, staging left by a killed fetch), so
// the checkpoint directory holds only the checkpoint.
func removeRailsEntries(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), railsOwnName) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name())) //nolint:gosec // dir is the agent's own checkpoint directory; the name comes from its listing
		}
	}
}

// railsOwnerOn is node's agent pod, set as the owner of the rails pods on
// that node so they go with it; nil when it cannot be found.
func (a *Agent) railsOwnerOn(ctx context.Context, node string) *metav1.OwnerReference {
	pods, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: agentLabelSelector,
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil || len(pods.Items) != 1 {
		return nil
	}
	p := pods.Items[0]
	return &metav1.OwnerReference{APIVersion: "v1", Kind: "Pod", Name: p.Name, UID: p.UID}
}

func (a *Agent) waitRailsServeReady(ctx context.Context, name string) (string, error) {
	var ip string
	err := wait.PollUntilContextTimeout(ctx, time.Second, railsServeReadyTimeout, true, func(ctx context.Context) (bool, error) {
		pod, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("serve pod %s disappeared", name)
			}
			return false, nil
		}
		if podEnded(pod) {
			return false, fmt.Errorf("serve pod %s ended: %s", name, terminationSummary(pod))
		}
		if pod.DeletionTimestamp != nil {
			return false, fmt.Errorf("serve pod %s is being deleted", name)
		}
		if !podReady(pod) || pod.Status.PodIP == "" {
			return false, nil
		}
		ip = pod.Status.PodIP
		return true, nil
	})
	return ip, err
}

// waitRailsFetch waits for the fetch pod to end and reads its result.
func (a *Agent) waitRailsFetch(ctx context.Context, name string, timeout time.Duration) (*railsFetchResult, error) {
	var pod *corev1.Pod
	started := false
	begin := time.Now()
	errNotStarted := errors.New("not running yet")
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		p, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("fetch pod %s disappeared", name)
			}
			return false, nil
		}
		switch p.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			pod = p
			return true, nil
		case corev1.PodRunning:
			started = true
		}
		if !started && time.Since(begin) > railsFetchStartTimeout {
			return false, fmt.Errorf("%w after %s", errNotStarted, railsFetchStartTimeout)
		}
		return false, nil
	})
	if err != nil {
		if !started {
			return nil, fmt.Errorf("%w: fetch pod %s did not start: %w", errRailsUnavailable, name, err)
		}
		return nil, fmt.Errorf("rails fetch pod %s: %w", name, err)
	}
	return parseRailsFetch(pod)
}

// parseRailsFetch reads the fetch's exit code and its --json line, which
// the pod writes as its termination message.
func parseRailsFetch(pod *corev1.Pod) (*railsFetchResult, error) {
	var term *corev1.ContainerStateTerminated
	for i := range pod.Status.ContainerStatuses {
		if c := &pod.Status.ContainerStatuses[i]; c.Name == "fetch" && c.State.Terminated != nil {
			term = c.State.Terminated
		}
	}
	if term == nil {
		return nil, fmt.Errorf("rails fetch pod %s ended (%s) without a container status", pod.Name, pod.Status.Reason)
	}
	var res railsFetchResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(term.Message)), &res); err != nil {
		// A fetch killed by a signal prints no line: the exit code decides.
		res = railsFetchResult{ExitCode: int(term.ExitCode)}
	}
	if term.ExitCode != 0 || !res.OK {
		kind, msg := "other", term.Reason
		if res.ErrorKind != nil {
			kind = *res.ErrorKind
		}
		if res.Error != nil {
			msg = *res.Error
		}
		return nil, fmt.Errorf("bedrock-peer fetch exited %d (%s): %s", term.ExitCode, kind, msg)
	}
	return &res, nil
}

func terminationSummary(pod *corev1.Pod) string {
	for i := range pod.Status.ContainerStatuses {
		if t := pod.Status.ContainerStatuses[i].State.Terminated; t != nil {
			return fmt.Sprintf("exit %d %s %s", t.ExitCode, t.Reason, strings.TrimSpace(t.Message))
		}
	}
	return string(pod.Status.Phase) + " " + pod.Status.Reason
}

// deleteRailsPods deletes the named rails pods, freeing their NICs.
func (a *Agent) deleteRailsPods(ctx context.Context, names []string) {
	zero := int64(0)
	for _, n := range names {
		err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).Delete(ctx, n, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if err != nil && !apierrors.IsNotFound(err) {
			a.log.WithError(err).WithField("pod", n).Warn("rails: cannot delete a rails pod; it ends at its deadline")
		}
	}
}

// sweepRailsPods deletes rails pods an earlier run of this node's agent
// left: its fetch pods, whose fetch is gone with it, and this node's serve
// pod, whose fetches fall over to their other sources or tiers.
func (a *Agent) sweepRailsPods(ctx context.Context) {
	if !a.railsEnabled() {
		return
	}
	self := railsLabelValue(a.config.NodeName)
	var names []string
	for _, sel := range []string{railsLabelFor + "=" + self, railsLabelServes + "=" + self} {
		pods, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if err != nil {
			a.log.WithError(err).Warn("rails: cannot list rails pods left by an earlier run")
			return
		}
		for i := range pods.Items {
			names = append(names, pods.Items[i].Name)
		}
	}
	if len(names) > 0 {
		a.log.WithField("pods", names).Info("rails: deleting rails pods left by an earlier run")
		a.deleteRailsPods(ctx, names)
	}
}

// railsLabelValue fits a node name into a label value (63 characters).
func railsLabelValue(node string) string {
	if len(node) <= 63 {
		return node
	}
	sum := sha256.Sum256([]byte(node))
	return node[:46] + "-" + hex.EncodeToString(sum[:8])
}

// railsRun builds the fetch pod of one rails fetch.
type railsRun struct {
	cfg          RailsConfig
	forNode      string // the fetching node
	checkpointID string
	hostDir      string // the checkpoint's host path, the same on every node
	owner        *metav1.OwnerReference
}

// fetchPod copies the checkpoint from sources (serve pod addresses) into
// its directory. sourceNodes (railsLabelValue of each source's node) keep
// those nodes' serve pods while it runs.
func (r railsRun) fetchPod(sources, sourceNodes []string, timeout time.Duration) *corev1.Pod {
	pod := railsPod(r.cfg, railsPodSpec{
		generateName: "nvsnap-rails-fetch-",
		component:    "rails-fetch",
		node:         r.forNode,
		hostDir:      r.hostDir,
		cpu:          r.cfg.FetchCPU,
		memory:       r.cfg.FetchMemory,
		deadline:     timeout + railsFetchStartTimeout,
		owner:        r.owner,
		labels:       map[string]string{railsLabelFor: railsLabelValue(r.forNode)},
		annotations: map[string]string{
			railsAnnoCheckpoint: r.checkpointID,
			railsAnnoSources:    strings.Join(sourceNodes, ","),
		},
	})
	c := &pod.Spec.Containers[0]
	c.Name = "fetch"
	args := make([]string, 0, 2*len(sources)+10)
	args = append(args, "fetch")
	for _, s := range sources {
		args = append(args, "--source", s)
	}
	// The serve pods serve every checkpoint of their node; the prefix
	// picks this one, whose files land at the top of --dest.
	args = append(args, "--prefix", r.checkpointID, "--devices", "all", "--dest", railsMountPath,
		"--connections", strconv.Itoa(r.cfg.connections()), "--json")
	// The --json line becomes the termination message; the human summary
	// and logs stay in the container log.
	c.Command = []string{"/bin/sh", "-c", `ulimit -l unlimited 2>/dev/null; exec "$0" "$@" >/dev/termination-log`, "bedrock-peer"}
	c.Args = args
	c.TerminationMessagePath = corev1.TerminationMessagePathDefault
	c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	return pod
}

// railsPodSpec is what differs between a serve pod and a fetch pod.
type railsPodSpec struct {
	name, generateName  string
	component, node     string
	hostDir             string
	readOnly            bool
	cpu, memory         string
	deadline            time.Duration
	owner               *metav1.OwnerReference
	labels, annotations map[string]string
}

func railsPod(cfg RailsConfig, spec railsPodSpec) *corev1.Pod {
	secs := int64(spec.deadline / time.Second)
	zero := int64(0)
	noEscalate := false
	dirType := corev1.HostPathDirectory
	res := corev1.ResourceList{}
	if spec.cpu != "" {
		res[corev1.ResourceCPU] = resource.MustParse(spec.cpu)
	}
	if spec.memory != "" {
		res[corev1.ResourceMemory] = resource.MustParse(spec.memory)
	}
	pullSecrets := make([]corev1.LocalObjectReference, 0, len(cfg.PullSecrets))
	for _, s := range cfg.PullSecrets {
		if s != "" {
			pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: s})
		}
	}
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "nvsnap-agent",
		railsLabelComponent:            spec.component,
	}
	for k, v := range spec.labels {
		labels[k] = v
	}
	meta := metav1.ObjectMeta{
		Name:         spec.name,
		GenerateName: spec.generateName,
		Namespace:    cfg.Namespace,
		Labels:       labels,
		Annotations:  spec.annotations,
	}
	if spec.owner != nil {
		meta.OwnerReferences = []metav1.OwnerReference{*spec.owner}
	}
	return &corev1.Pod{
		ObjectMeta: meta,
		Spec: corev1.PodSpec{
			// nodeSelector, not nodeName: the scheduler allocates the DRA
			// claim, and a pod bound with nodeName never gets its NICs.
			NodeSelector: map[string]string{"kubernetes.io/hostname": spec.node},
			// GPU nodes carry taints, and a source node may be cordoned.
			Tolerations:                   []corev1.Toleration{{Operator: corev1.TolerationOpExists}},
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &zero,
			ActiveDeadlineSeconds:         &secs,
			ImagePullSecrets:              pullSecrets,
			ResourceClaims: []corev1.PodResourceClaim{{
				Name:                      "rails",
				ResourceClaimTemplateName: &cfg.ClaimTemplate,
			}},
			Volumes: []corev1.Volume{{
				Name: "checkpoint",
				VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
					Path: spec.hostDir, Type: &dirType,
				}},
			}},
			Containers: []corev1.Container{{
				Image: cfg.Image,
				Env: []corev1.EnvVar{{
					Name: "BEDROCK_PEER_SECRET",
					ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: cfg.SecretName},
						Key:                  "secret",
					}},
				}},
				Resources: corev1.ResourceRequirements{
					Requests: res,
					Limits:   res,
					Claims:   []corev1.ResourceClaim{{Name: "rails"}},
				},
				// Root, so the source reads 0600 files and the destination
				// keeps their owners; IPC_LOCK registers memory for RDMA.
				// Not privileged, and no NET_ADMIN: bedrock-peer pairs rails
				// without routes.
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: &noEscalate,
					Capabilities: &corev1.Capabilities{
						Add:  []corev1.Capability{"IPC_LOCK"},
						Drop: []corev1.Capability{"NET_RAW"},
					},
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "checkpoint", MountPath: railsMountPath, ReadOnly: spec.readOnly,
				}},
			}},
		},
	}
}
