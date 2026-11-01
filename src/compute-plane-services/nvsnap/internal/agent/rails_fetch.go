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
// The agent DaemonSet never holds the NICs. One rails pod per node claims
// every RDMA NIC of its node through DRA (rails_serve.go): it serves the
// node's checkpoints to every fetch from it, and runs the node's own
// fetches beside the server (rails_jobs.go), so a node sends and receives
// at once. A fetch writes into the checkpoint directory under the fetch
// marker EnsureLocal already set. The copy lands file by file in the
// existing directory, never by renaming the directory, because a
// placeholder may hold its subdirectories bind-mounted.
//
// A checkpoint under MinSize is left to the HTTP peers, which move it
// before a rails pod would have started; it then holds no NICs a large copy
// waits for. Sources that are not fetching themselves come first: a node
// that is fetching may be busier.
//
// Any failure (no rails pod scheduled, no NICs free, bedrock-peer exiting
// non-zero) stops the fetch and falls through to the next tier.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// Namespace is where the rails pods run: the namespace of
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
	// FetchCPU and FetchMemory are a fetch's budget. A node's rails pod
	// both serves and fetches, and gets the larger of the two budgets.
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

// railsPodUnschedulableGrace is how long the scheduler may report a rails
// pod unschedulable before the tier gives up on it, well before
// railsServeReadyTimeout: a pod no node can take (its node's memory taken
// by a workload) would otherwise hold the restore for the whole wait, while
// the next tier starts at once. The grace rides out a report made before
// the pod's NIC claim exists. A variable for tests.
var railsPodUnschedulableGrace = 15 * time.Second

// errRailsUnavailable marks a rails fetch that did not run: the tier is
// off, no source is reachable, or the pods could not start. EnsureLocal
// falls through on any error; this one is logged at Info.
var errRailsUnavailable = errors.New("rails tier unavailable")

// errRailsSmall marks a checkpoint under the rails minimum: EnsureLocal
// asks the HTTP peers for it first.
var errRailsSmall = fmt.Errorf("%w: the checkpoint is under the rails minimum", errRailsUnavailable)

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
func (a *Agent) fetchOverRails(ctx context.Context, checkpointID, localDir string, peers []peerInfo) (err error) {
	cfg := a.config.Rails
	log := a.log.WithField("checkpoint_id", checkpointID)

	sources, size := a.railsSources(ctx, checkpointID, peers, log)
	if len(sources) == 0 {
		return fmt.Errorf("%w: no other node lists %s", errRailsUnavailable, checkpointID)
	}
	if size < cfg.minBytes() {
		return fmt.Errorf("%w (%d bytes, minimum %d); the peers move it sooner", errRailsSmall, size, cfg.minBytes())
	}
	if err := checkFreeSpace(localDir, size, cfg.keepFreePercent()); err != nil {
		return fmt.Errorf("%w: %w", errRailsUnavailable, err)
	}

	timeout := railsFetchTimeout(size)
	defer removeRailsEntries(localDir)

	// The claims cover the wait for the pods and the whole fetch: a source
	// node's agent deletes its rails pod only once every claim has passed.
	until := time.Now().Add(railsServeReadyTimeout + timeout + railsServeClaimSlack)
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
	// This node's own rails pod runs the fetch, beside serving this node's
	// checkpoints to other nodes (rails_jobs.go).
	self, err := a.acquireServePod(ctx, a.config.NodeName, until)
	if err != nil {
		return fmt.Errorf("%w: this node's rails pod: %w", errRailsUnavailable, err)
	}
	// One deadline for all the pods: they start in parallel.
	readyCtx, cancelReady := context.WithTimeout(ctx, railsServeReadyTimeout)
	defer cancelReady()
	if _, err := a.waitRailsServeReady(readyCtx, self); err != nil {
		return fmt.Errorf("%w: this node's rails pod %s: %w", errRailsUnavailable, self, err)
	}
	var addrs []string
	for _, name := range serving {
		ip, err := a.waitRailsServeReady(readyCtx, name)
		if err != nil {
			log.WithError(err).WithField("pod", name).Warn("rails: serve pod not ready; dropping that source")
			continue
		}
		addrs = append(addrs, fmt.Sprintf("%s:%d", ip, cfg.port()))
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: no serve pod became ready", errRailsUnavailable)
	}

	job, err := a.submitRailsJob(checkpointID, railsFetchArgs(cfg, addrs, checkpointID))
	if err != nil {
		return fmt.Errorf("%w: hand the fetch to %s: %w", errRailsUnavailable, self, err)
	}
	// A fetch that may still run keeps its files: the pod stops it by
	// the pid it recorded.
	defer func() {
		if errors.Is(err, errRailsFetchRunning) {
			a.railsRunning.Store(checkpointID, railsRunningJob{pod: self, job: job})
			return
		}
		job.remove()
	}()
	defer a.railsFetchStarted(ctx, self)()
	log.WithFields(map[string]interface{}{"sources": addrs, "source_nodes": servingNodes, "bytes": size, "pod": self, "job": job.id}).Info("rails: fetch started")

	res, err := a.waitRailsJob(ctx, self, job, timeout)
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

// railsRunningJob is a cancelled fetch that may still be writing into its
// checkpoint directory.
type railsRunningJob struct {
	pod string
	job railsJob
}

// railsFetchStillRunning reports a cancelled fetch of checkpointID that
// may still write into its directory; one that has ended is forgotten and
// its files removed.
func (a *Agent) railsFetchStillRunning(ctx context.Context, checkpointID string) bool {
	v, ok := a.railsRunning.Load(checkpointID)
	if !ok {
		return false
	}
	r := v.(railsRunningJob)
	once, cancel := context.WithTimeout(ctx, railsJobPoll)
	defer cancel()
	if !a.waitRailsJobGone(once, r.pod, r.job) {
		return true
	}
	r.job.remove()
	a.railsRunning.Delete(checkpointID)
	return false
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
	var unschedulableSince time.Time
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
		if msg, ok := podUnschedulable(pod); !ok {
			unschedulableSince = time.Time{}
		} else if unschedulableSince.IsZero() {
			unschedulableSince = time.Now()
		} else if time.Since(unschedulableSince) >= railsPodUnschedulableGrace {
			return false, fmt.Errorf("rails pod %s unschedulable for %s: %s", name, railsPodUnschedulableGrace, msg)
		}
		if !podReady(pod) || pod.Status.PodIP == "" {
			return false, nil
		}
		ip = pod.Status.PodIP
		return true, nil
	})
	return ip, err
}

// podUnschedulable reports whether the scheduler found no node for pod,
// with its message.
func podUnschedulable(pod *corev1.Pod) (string, bool) {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c.Message, true
		}
	}
	return "", false
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
	// Their fetches went with them; their job files are stale.
	if a.config.CheckpointDir != "" {
		_ = os.RemoveAll(filepath.Join(a.config.CheckpointDir, railsJobsDir))
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

// railsFetchArgs are a fetch's arguments: the checkpoint from sources
// (serve pod addresses) into its directory. The serve pods serve every
// checkpoint of their node; the prefix picks this one.
func railsFetchArgs(cfg RailsConfig, sources []string, checkpointID string) []string {
	args := make([]string, 0, 2*len(sources)+10)
	args = append(args, "fetch")
	for _, s := range sources {
		args = append(args, "--source", s)
	}
	return append(args, "--prefix", checkpointID, "--devices", "all", "--dest", railsMountPath+"/"+checkpointID,
		"--connections", strconv.Itoa(cfg.connections()), "--json")
}

// railsPodSpec is what a rails pod is built from.
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
