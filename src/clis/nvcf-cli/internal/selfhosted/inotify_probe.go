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
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// inotifyProbeImage is the image used by the per-node probe pod. Pinned
	// to match the documented inotify-tuner DaemonSet so customers do not need
	// to mirror an extra image.
	inotifyProbeImage = "busybox:1.36"

	// inotifyProbeNamespace is where probe pods are created. "default" exists
	// on every cluster and matches kubectl debug node's default behavior.
	inotifyProbeNamespace = "default"

	// inotifyProbeContainer is the container name inside the probe pod.
	inotifyProbeContainer = "probe"

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
func NewInotifyProber() NodeInotifyProber {
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
		return probeAllNodes(ctx, client)
	}
}

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
func probeAllNodes(ctx context.Context, client kubernetes.Interface) ([]NodeInotifyLimits, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return nil, nil
	}
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
			results[i] = probeOneNode(egCtx, client, nodeName)
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
func probeOneNode(ctx context.Context, client kubernetes.Interface, nodeName string) NodeInotifyLimits {
	res := NodeInotifyLimits{NodeName: nodeName}

	pctx, cancel := context.WithTimeout(ctx, perNodePodTimeout)
	defer cancel()

	pod, err := client.CoreV1().Pods(inotifyProbeNamespace).Create(pctx, buildInotifyProbePod(nodeName), metav1.CreateOptions{})
	if err != nil {
		res.Err = fmt.Errorf("create probe pod on node %s: %w", nodeName, err)
		return res
	}
	defer cleanupProbePod(client, pod.Name)

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

// buildInotifyProbePod constructs a Pod that runs once on the named node and
// prints the two inotify sysctls to stdout. The container is unprivileged
// and runs in the default PID namespace: /proc/sys/fs/inotify/max_user_*
// are kernel-wide sysctls and are world-readable through the host's procfs
// via the read-only /host hostPath mount.
func buildInotifyProbePod(nodeName string) *corev1.Pod {
	hostPathDir := corev1.HostPathDirectory
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "nvcf-inotify-probe-",
			Namespace:    inotifyProbeNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "nvcf-inotify-probe",
				"app.kubernetes.io/managed-by": "nvcf-cli",
			},
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyNever,
			Tolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists},
			},
			Containers: []corev1.Container{{
				Name:    inotifyProbeContainer,
				Image:   inotifyProbeImage,
				Command: []string{"sh", "-c", inotifyProbeShellCmd},
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

// cleanupProbePod best-effort deletes the probe pod. Uses a background
// context so the pod is cleaned up even when the caller's context was
// canceled mid-probe.
func cleanupProbePod(client kubernetes.Interface, podName string) {
	delCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grace := podDeleteGrace
	err := client.CoreV1().Pods(inotifyProbeNamespace).Delete(delCtx, podName, metav1.DeleteOptions{
		GracePeriodSeconds: &grace,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		// Swallow; the caller already has its result and a leaked probe pod
		// is preferable to an error that masks the real check outcome.
		_ = err
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
