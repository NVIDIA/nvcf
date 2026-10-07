/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package selfhosted

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

const (
	// inotifyProbeImage is the per-node probe pod's image when no probe
	// image is configured. Pinned to match the documented inotify-tuner
	// DaemonSet so customers do not need to mirror an extra image.
	inotifyProbeImage = "busybox:1.36"

	// inotifyProbeNamespace is where probe pods are created. "default" exists
	// on every cluster and matches kubectl debug node's default behavior.
	inotifyProbeNamespace = "default"

	// inotifyProbeContainer is the container name inside the probe pod.
	inotifyProbeContainer = "probe"

	// inotifyProbeAppLabel names the probe's pods apart from the validator's.
	inotifyProbeAppLabel = "nvcf-inotify-probe"

	// inotifyProbeRole is the role the probe's pods are labelled with: only
	// the compute-plane checks run it.
	inotifyProbeRole = clusterValidatorComputePlaneRole

	// inotifyProbeShellCmd reads both sysctls. Each cat runs independently
	// so a missing /proc entry on one path doesn't drop the other value;
	// the surviving integer reaches the parser, which errors clearly with
	// "expected two integers, got <stdout>" rather than failing the pod
	// and discarding all diagnostic info.
	inotifyProbeShellCmd = "cat /host/proc/sys/fs/inotify/max_user_instances; cat /host/proc/sys/fs/inotify/max_user_watches"

	// perNodePodTimeout caps the create+wait+logs sequence for one node.
	// Image pull on a cold node can take ~30s; pod schedule + cat is sub-second.
	perNodePodTimeout = 90 * time.Second

	// podPollInterval is how often we poll pod status while waiting for the
	// probe container to terminate.
	podPollInterval = 1 * time.Second

	// podDeleteGrace is the grace period when cleaning up a probe pod.
	podDeleteGrace = int64(0)

	// probeConcurrency caps how many nodes we probe in parallel. Image pull
	// dominates wall time, so probing all nodes serially can blow the
	// outer 2-minute check budget on multi-node clusters. 8 is a safe upper
	// bound: each goroutine creates exactly one short-lived pod, the API
	// server handles it trivially, and we stay well under default
	// per-namespace LimitRange / ResourceQuota ceilings.
	probeConcurrency = 8

	// inotifyClientQPS / inotifyClientBurst override client-go's default
	// rest.Config rate limits (QPS=5, Burst=10), which are too low for our
	// fan-out and produce "Waited before sending request" klog warnings
	// mid-probe. The values mirror kubectl's per-invocation client.
	inotifyClientQPS   = 50
	inotifyClientBurst = 100
)

// NewInotifyProber returns a NodeInotifyProber backed by the Kubernetes Go
// client. It lists nodes via the API and, for each node, creates a
// short-lived unprivileged pod pinned to that node with a read-only
// hostPath mount of /, reads /proc/sys/fs/inotify/max_user_{instances,watches}
// via the pod's logs, then deletes the pod. The inotify sysctls are
// world-readable, so neither hostPID nor a privileged container is
// required.
//
// Per-node failures (RBAC denials, scheduling errors, image-pull failures)
// surface as NodeInotifyLimits.Err entries; the inotify check downgrades
// those to a warning so preflight still fails loud on actual limit
// violations without blocking users whose RBAC forbids pod creation. On
// PSA-restricted clusters where pod create is denied for unprivileged
// users, customers may need to apply the documented node-inotify-tuner
// DaemonSet manually before this probe will succeed.
//
// image replaces the default busybox image, for clusters that pull from a
// mirror; empty keeps the default.
func NewInotifyProber(image string) NodeInotifyProber {
	return func(ctx context.Context, kubeContext string) ([]NodeInotifyLimits, error) {
		restCfg, err := loadKubeConfig(kubeContext)
		if err != nil {
			return nil, fmt.Errorf("building kubeconfig: %w", err)
		}
		// Raise client-go's default client-side rate limits. The defaults
		// (QPS=5, Burst=10) are tuned for in-cluster controllers, not for a
		// CLI tool that fans out probeConcurrency goroutines each polling
		// pod status every podPollInterval. Without this bump, klog emits
		// "Waited before sending request" lines mid-probe; the requests
		// still succeed but the log noise is misleading. These values
		// match kubectl's per-invocation client and stay well below any
		// realistic API-server priority/fairness ceiling for a short-lived
		// preflight.
		restCfg.QPS = inotifyClientQPS
		restCfg.Burst = inotifyClientBurst
		client, err := kubernetes.NewForConfig(restCfg)
		if err != nil {
			return nil, fmt.Errorf("building kubernetes client: %w", err)
		}
		return probeAllNodes(ctx, client, image)
	}
}

// inotifyProbeBudget bounds the whole probe, inside the time the check sets
// aside for the checks that run before the validator. A var so tests can
// shorten it.
var inotifyProbeBudget = 100 * time.Second

// probePodCleanupTimeout bounds the deletion of a probe pod, which runs after
// the probe's own deadline. The probe share leaves room for it.
var probePodCleanupTimeout = 10 * time.Second

// probeAllNodes is the testable core: it takes a kubernetes.Interface so
// callers can inject fake.NewSimpleClientset. It probes nodes in parallel
// with a small concurrency cap so image-pull latency doesn't blow the
// outer check budget on multi-node clusters.
//
// Per-node failures (including context cancellation) populate the result
// element's Err field; the function itself only returns a non-nil error
// when the initial node list call fails. That way callers in
// nodeInotifyCheck can always inspect partial results, and limit
// violations on some nodes are never dropped when other nodes are
// concurrently unreachable.
func probeAllNodes(ctx context.Context, client kubernetes.Interface, image string) ([]NodeInotifyLimits, error) {
	// Nodes are probed probeConcurrency at a time, so on a cluster whose
	// probe pods stall each wave takes perNodePodTimeout: unbounded, the probe
	// ran past the time the check sets aside for the checks before the
	// validator, and cut the validator short. A node not reached in time is
	// reported as not probed.
	ctx, cancel := context.WithTimeout(ctx, inotifyProbeBudget)
	defer cancel()
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return nil, nil
	}
	runID, err := newValidatorRunID()
	if err != nil {
		return nil, err
	}
	run := &probeRun{id: runID}
	// Each pod is deleted once its node is probed, but a Create the budget or
	// an interrupt cut off may still have been applied, and then no per-node
	// delete runs. This reclaims those by the run's label on every return.
	probeDeadline, _ := ctx.Deadline()
	defer func() { run.reclaim(client, probeDeadline) }()
	results := make([]NodeInotifyLimits, len(nodes.Items))
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(probeConcurrency)
	for i, n := range nodes.Items {
		// Unschedulable nodes won't have control plane components deployed on them.
		if n.Spec.Unschedulable {
			continue
		}
		i, nodeName := i, n.Name
		eg.Go(func() error {
			results[i] = probeOneNode(egCtx, client, run, nodeName, image)
			// Per-node failures are encoded in results[i].Err; never
			// propagate them as the errgroup's error, since that would
			// cancel sibling probes still in flight.
			return nil
		})
	}
	_ = eg.Wait()
	// Remove any results from skipped nodes.
	results = slices.DeleteFunc(results, func(item NodeInotifyLimits) bool {
		return item.Err == nil && item.NodeName == ""
	})
	return results, nil
}

// probeOneNode creates, waits on, and tears down a single probe pod, returning
// the parsed limits or a per-node error.
func probeOneNode(
	ctx context.Context, client kubernetes.Interface, run *probeRun, nodeName, image string,
) (res NodeInotifyLimits) {
	res = NodeInotifyLimits{NodeName: nodeName}
	// ctx is the whole probe's, so a failure once it ended is the budget's,
	// not the node's.
	defer func() { res.OutOfBudget = res.Err != nil && ctx.Err() != nil }()
	if ctx.Err() != nil {
		res.Err = fmt.Errorf("not probed: the inotify probe's %s budget ran out", inotifyProbeBudget)
		return res
	}

	pctx, cancel := context.WithTimeout(ctx, perNodePodTimeout)
	defer cancel()

	pod, err := createProbePod(pctx, client, nodeName, run.id, image)
	if err != nil {
		res.Err = fmt.Errorf("create probe pod on node %s: %w", nodeName, err)
		return res
	}
	run.created(pod)
	defer cleanupProbePod(client, pod)

	if err := waitForPodTerminal(pctx, client, pod.Name); err != nil {
		res.Err = fmt.Errorf("waiting for probe pod on node %s: %w", nodeName, err)
		return res
	}

	logs, err := fetchPodLogs(pctx, client, pod.Name)
	if err != nil {
		res.Err = fmt.Errorf("fetching probe logs from node %s: %w", nodeName, err)
		return res
	}

	instances, watches, err := parseInotifyOutput(logs)
	if err != nil {
		res.Err = fmt.Errorf("parsing inotify output from node %s: %w", nodeName, err)
		return res
	}
	res.MaxUserInstances = instances
	res.MaxUserWatches = watches
	return res
}

// createProbePod creates the probe pod for nodeName. A ResourceQuota on cpu or
// memory in the namespace rejects a pod that states no requests or limits for
// them, so on that rejection the pod is created once more with small ones.
func createProbePod(
	ctx context.Context, client kubernetes.Interface, nodeName, runID, image string,
) (*corev1.Pod, error) {
	pods := client.CoreV1().Pods(inotifyProbeNamespace)
	pod := buildInotifyProbePod(nodeName, runID, image)
	created, err := pods.Create(ctx, pod, metav1.CreateOptions{})
	if !quotaRequiresResources(err) {
		return created, err
	}
	// A quota may cover limits as well as requests. Equal values keep the pod
	// Guaranteed.
	pod.Spec.Containers[0].Resources = corev1.ResourceRequirements{
		Requests: inotifyProbeQuotaResources(),
		Limits:   inotifyProbeQuotaResources(),
	}
	return pods.Create(ctx, pod, metav1.CreateOptions{})
}

// quotaRequiresResources reports a create a ResourceQuota refused because the
// pod states no request or limit for a resource the quota covers.
func quotaRequiresResources(err error) bool {
	return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "must specify")
}

// inotifyProbeQuotaResources are the probe container's requests, and its
// limits, in a namespace whose quota requires them.
func inotifyProbeQuotaResources() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("50m"),
		corev1.ResourceMemory: resource.MustParse("32Mi"),
	}
}

// inotifyProbeLabels are the labels on every probe pod of one run: the
// validator's managed labels under the probe's own name, the role, and the
// run ID. With an empty runID they select every run's probe pods.
func inotifyProbeLabels(runID string) map[string]string {
	l := clusterValidatorRunLabels(inotifyProbeRole, runID, false)
	l["app.kubernetes.io/name"] = inotifyProbeAppLabel
	return l
}

// buildInotifyProbePod constructs a Pod that runs once on the named node and
// prints the two inotify sysctls to stdout. The container is unprivileged
// and runs in the default PID namespace: /proc/sys/fs/inotify/max_user_*
// are kernel-wide sysctls and are world-readable through the host's procfs
// via the read-only /host hostPath mount, so the container also runs as a
// non-root user with no capabilities. An empty image selects the default.
func buildInotifyProbePod(nodeName, runID, image string) *corev1.Pod {
	if image == "" {
		image = inotifyProbeImage
	}
	hostPathDir := corev1.HostPathDirectory
	nobody := int64(65534)
	runAsNonRoot, readOnlyRoot, allowPrivEsc := true, true, false
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: inotifyProbeAppLabel + "-",
			Namespace:    inotifyProbeNamespace,
			Labels:       inotifyProbeLabels(runID),
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			Tolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists},
			},
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:  &nobody,
				RunAsGroup: &nobody,
			},
			Containers: []corev1.Container{{
				Name:    inotifyProbeContainer,
				Image:   image,
				Command: []string{"sh", "-c", inotifyProbeShellCmd},
				// No requests: the pod is pinned with NodeName, so the
				// kubelet admits it against the node's free capacity, and
				// any request has it rejected OutOfcpu or OutOfmemory on a
				// fully requested node, the busiest ones. No limits either:
				// a limit without a request sets the request to it.
				// createProbePod adds both where a quota requires them.
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &runAsNonRoot,
					ReadOnlyRootFilesystem:   &readOnlyRoot,
					AllowPrivilegeEscalation: &allowPrivEsc,
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "host",
					MountPath: "/host",
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "host",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: "/", Type: &hostPathDir},
				},
			}},
		},
	}
}

// waitForPodTerminal polls the pod until its phase is Succeeded or Failed.
// The probe container is `cat`, so success is the normal exit path; Failed
// surfaces image-pull errors, scheduling rejections, etc.
func waitForPodTerminal(ctx context.Context, client kubernetes.Interface, podName string) error {
	ticker := time.NewTicker(podPollInterval)
	defer ticker.Stop()
	for {
		pod, err := client.CoreV1().Pods(inotifyProbeNamespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get pod %s: %w", podName, err)
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return nil
		case corev1.PodFailed:
			return fmt.Errorf("probe pod %s failed: %s", podName, podFailureMessage(pod))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// podFailureMessage extracts a useful single-line failure reason from a
// terminated pod's container status (or falls back to the pod's status
// message/reason).
func podFailureMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated != nil {
			t := cs.State.Terminated
			if t.Reason != "" || t.Message != "" {
				return strings.TrimSpace(t.Reason + " " + t.Message)
			}
		}
		if cs.State.Waiting != nil {
			w := cs.State.Waiting
			if w.Reason != "" || w.Message != "" {
				return strings.TrimSpace(w.Reason + " " + w.Message)
			}
		}
	}
	if pod.Status.Reason != "" || pod.Status.Message != "" {
		return strings.TrimSpace(pod.Status.Reason + " " + pod.Status.Message)
	}
	return "unknown"
}

// fetchPodLogs returns the probe container's stdout as a string.
func fetchPodLogs(ctx context.Context, client kubernetes.Interface, podName string) (string, error) {
	req := client.CoreV1().Pods(inotifyProbeNamespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: inotifyProbeContainer,
	})
	rc, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("open log stream: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", fmt.Errorf("read logs: %w", err)
	}
	return string(b), nil
}

// cleanupProbePod best-effort deletes the probe pod this run created, pinned
// to its UID. Uses a background context so the pod is cleaned up even when
// the caller's context was canceled mid-probe.
func cleanupProbePod(client kubernetes.Interface, pod *corev1.Pod) {
	delCtx, cancel := context.WithTimeout(context.Background(), probePodCleanupTimeout)
	defer cancel()
	deleteProbePod(delCtx, client, pod)
}

// deleteProbePod deletes one probe pod at once, pinned to its UID. Errors are
// swallowed: the caller already has its result, and a leaked probe pod is
// preferable to an error that masks the real check outcome.
func deleteProbePod(ctx context.Context, client kubernetes.Interface, pod *corev1.Pod) {
	opts := deleteExactly(pod)
	grace := podDeleteGrace
	opts.GracePeriodSeconds = &grace
	err := client.CoreV1().Pods(inotifyProbeNamespace).Delete(ctx, pod.Name, opts)
	if err != nil && !apierrors.IsNotFound(err) {
		_ = err
	}
}

// probeRun is one probe's identity and what it learned of the apiserver's
// clock from the pods it created.
type probeRun struct {
	id string
	mu sync.Mutex
	// serverNow is the latest creationTimestamp among this run's pods.
	serverNow time.Time
}

func (r *probeRun) created(pod *corev1.Pod) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ts := pod.CreationTimestamp.Time; ts.After(r.serverNow) {
		r.serverNow = ts
	}
}

// reclaim deletes, on a fresh bounded context, every probe pod still carrying
// this run's ID, and every other run's probe pod older than
// orphanValidatorRBACTTL on the apiserver's clock: a run killed outright, or
// interrupted twice, leaves pods that mount the host's root and tolerate
// every taint, and nothing else removes them. Another run's younger pods may
// belong to a probe still in progress, so they are kept.
//
// It ends within probePodCleanupTimeout of the probe's deadline, the room the
// probe share leaves for the cleanup after it, which the per-node deletes may
// already have used.
func (r *probeRun) reclaim(client kubernetes.Interface, probeDeadline time.Time) {
	start := time.Now()
	if !probeDeadline.IsZero() && probeDeadline.Before(start) {
		start = probeDeadline
	}
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(probePodCleanupTimeout))
	defer cancel()
	pods, err := client.CoreV1().Pods(inotifyProbeNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(inotifyProbeLabels("")).String(),
	})
	if err != nil {
		return
	}
	r.mu.Lock()
	now := r.serverNow
	r.mu.Unlock()
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !strings.HasPrefix(pod.Name, inotifyProbeAppLabel+"-") {
			continue
		}
		mine := pod.Labels[clusterValidatorRunLabel] == r.id
		stale := !now.IsZero() && pod.CreationTimestamp.Time.Before(now.Add(-orphanValidatorRBACTTL))
		if mine || stale {
			deleteProbePod(ctx, client, pod)
		}
	}
}

// parseInotifyOutput extracts max_user_instances then max_user_watches from
// the two-line probe-container stdout. Tolerates leading/trailing whitespace
// and ignores any non-numeric lines so the parse is robust to stray container
// preamble.
func parseInotifyOutput(out string) (instances, watches int64, err error) {
	var nums []int64
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v, parseErr := strconv.ParseInt(line, 10, 64)
		if parseErr != nil {
			continue
		}
		nums = append(nums, v)
		if len(nums) == 2 {
			break
		}
	}
	if len(nums) < 2 {
		return 0, 0, fmt.Errorf("expected two integers, got %q", strings.TrimSpace(out))
	}
	return nums[0], nums[1], nil
}
