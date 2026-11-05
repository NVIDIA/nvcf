// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// A node's rails pod (named its serve pod), shared by every rails fetch
// that reads from the node and running the node's own fetches. A rails pod
// holds every RDMA NIC of its node, so a node runs one: a pod per fetch
// made fetches from the same node queue for its NICs, and a node that
// served could not fetch. The pod serves the node's whole checkpoint
// directory to up to ServeMaxFetchers fetches at once; a fetch names its
// checkpoint with --prefix. bedrock-peer lists the directory per request,
// so checkpoints that land after the pod started are served too. The
// node's own fetches run beside the server (rails_jobs.go).
//
// Lifetime. A fetcher creates the pod, or finds it, and moves its
// nvsnap.io/rails-wanted-until annotation to cover the wait for the pod to
// be Ready and the whole fetch, whether it reads from the node or runs in
// it. The node's own agent deletes the pod once that time has passed and
// no fetch pod of an earlier release lists the node. The delete carries the resourceVersion the
// agent read, so a fetcher's annotation patch that lands after that read
// cancels it. See docs/design/RAILS-FETCH-CONCURRENCY.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	// railsLabelServes names, on a serve pod, the node it serves.
	railsLabelServes = "nvsnap.io/rails-serves"
	// railsAnnoWantedUntil keeps a serve pod until a fetcher that is
	// waiting for it has created its fetch pod.
	railsAnnoWantedUntil = "nvsnap.io/rails-wanted-until"
	// railsAnnoSources lists, on a fetch pod, the nodes it reads from
	// (railsLabelValue of each, comma-separated).
	railsAnnoSources = "nvsnap.io/rails-sources"

	// railsServeClaimSlack is how long past its Ready wait a fetcher's
	// claim on a serve pod lasts: time to create its fetch pod.
	railsServeClaimSlack = time.Minute
	// railsServeMaxLife bounds a serve pod no agent reaps (every agent
	// gone); the reaper ends it long before.
	railsServeMaxLife = 12 * time.Hour
	// railsServeReapInterval is how often an agent checks its node's
	// serve pod.
	railsServeReapInterval = 15 * time.Second
	// railsServeAcquireAttempts bounds the races a fetcher retries on a
	// serve pod that keeps changing under it.
	railsServeAcquireAttempts = 5
)

// railsServePodName is the fixed name of node's serve pod.
func railsServePodName(node string) string {
	return "nvsnap-rails-serve-" + railsLabelValue(node)
}

// railsServePod builds node's serve pod: the node's checkpoint directory,
// read-only, to up to ServeMaxFetchers fetches at once.
func railsServePod(cfg RailsConfig, node, hostRoot string, owner *metav1.OwnerReference, wantedUntil time.Time) *corev1.Pod {
	const ready = "/tmp/serve.ready"
	pod := railsPod(cfg, railsPodSpec{
		name:      railsServePodName(node),
		component: "rails-serve",
		node:      node,
		hostDir:   hostRoot,
		// Writable: the node's own fetches land here (rails_jobs.go).
		cpu:      largerQuantity(cfg.ServeCPU, cfg.FetchCPU),
		memory:   largerQuantity(cfg.ServeMemory, cfg.FetchMemory),
		deadline: railsServeMaxLife,
		owner:    owner,
		labels:   map[string]string{railsLabelServes: railsLabelValue(node)},
		annotations: map[string]string{
			railsAnnoWantedUntil: wantedUntil.UTC().Format(time.RFC3339),
		},
	})
	c := &pod.Spec.Containers[0]
	c.Name = "serve"
	c.Command = []string{"/bin/sh", "-c", railsPodScript(railsMountPath + "/" + railsJobsDir), "bedrock-peer"}
	c.Args = []string{"serve", "--root", railsMountPath, "--devices", "all",
		"--port", fmt.Sprint(cfg.port()), "--max-fetchers", fmt.Sprint(cfg.serveMaxFetchers()), "--ready-file", ready}
	c.ReadinessProbe = &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"test", "-f", ready}}},
		PeriodSeconds: 1,
	}
	return pod
}

// largerQuantity is the larger of two resource quantities ("" is none):
// the rails pod serves and fetches, so it gets the larger of the two
// budgets, not their sum.
func largerQuantity(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	qa, qb := resource.MustParse(a), resource.MustParse(b)
	if qb.Cmp(qa) > 0 {
		return b
	}
	return a
}

// wantedUntil reads a serve pod's claim; zero when it has none.
func wantedUntil(pod *corev1.Pod) time.Time {
	t, err := time.Parse(time.RFC3339, pod.Annotations[railsAnnoWantedUntil])
	if err != nil {
		return time.Time{}
	}
	return t
}

func podEnded(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// acquireServePod makes sure node has a serve pod that lasts until at
// least until, and returns its name. A pod being deleted is waited out and
// replaced; an ended one is deleted and replaced.
func (a *Agent) acquireServePod(ctx context.Context, node string, until time.Time) (string, error) {
	cfg := a.config.Rails
	pods := a.kubeClient.CoreV1().Pods(cfg.Namespace)
	name := railsServePodName(node)
	for range railsServeAcquireAttempts {
		pod, err := pods.Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			p := railsServePod(cfg, node, a.checkpointHostDir(), a.railsOwnerOn(ctx, node), until)
			if _, cerr := pods.Create(ctx, p, metav1.CreateOptions{}); cerr == nil || !apierrors.IsAlreadyExists(cerr) {
				return name, cerr
			}
			continue // another fetcher created it first
		case err != nil:
			return "", err
		case pod.DeletionTimestamp != nil:
			if werr := a.waitPodGone(ctx, name); werr != nil {
				return "", werr
			}
			continue
		case podEnded(pod):
			rv := pod.ResourceVersion
			derr := pods.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
			if derr != nil && !apierrors.IsNotFound(derr) && !apierrors.IsConflict(derr) {
				return "", derr
			}
			continue
		}
		if !wantedUntil(pod).Before(until) {
			return name, nil
		}
		// The resourceVersion makes the patch fail if the pod changed since
		// it was read, and makes the reaper's delete of what it read fail
		// once the patch is in.
		patch, _ := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{
			"resourceVersion": pod.ResourceVersion,
			"annotations":     map[string]string{railsAnnoWantedUntil: until.UTC().Format(time.RFC3339)},
		}})
		_, err = pods.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		switch {
		case err == nil:
			return name, nil
		case apierrors.IsConflict(err), apierrors.IsNotFound(err):
			continue
		default:
			return "", err
		}
	}
	return "", fmt.Errorf("serve pod %s kept changing", name)
}

func (a *Agent) waitPodGone(ctx context.Context, name string) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, railsServeReadyTimeout, true, func(ctx context.Context) (bool, error) {
		_, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, nil
	})
}

// reapServePod deletes this node's serve pod once nothing wants it, which
// frees the node's NICs for a fetch of its own or for a workload.
func (a *Agent) reapServePod(ctx context.Context, now time.Time) {
	cfg := a.config.Rails
	pods := a.kubeClient.CoreV1().Pods(cfg.Namespace)
	name := railsServePodName(a.config.NodeName)
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err != nil || pod.DeletionTimestamp != nil {
		return
	}
	if !podEnded(pod) {
		if wantedUntil(pod).After(now) {
			return
		}
		inUse, lerr := a.servePodInUse(ctx)
		if lerr != nil {
			a.log.WithError(lerr).Warn("rails: cannot list the fetch pods; keeping this node's serve pod")
			return
		}
		if inUse {
			return
		}
	}
	rv := pod.ResourceVersion
	err = pods.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
	switch {
	case err == nil:
		a.log.WithField("pod", name).Info("rails: deleted this node's serve pod; no fetch wants it")
	case apierrors.IsConflict(err), apierrors.IsNotFound(err):
		// A fetcher claimed it since it was read, or it is gone.
	default:
		a.log.WithError(err).WithField("pod", name).Warn("rails: cannot delete this node's serve pod")
	}
}

// servePodInUse reports whether a fetch pod that has not ended reads from
// this node.
func (a *Agent) servePodInUse(ctx context.Context) (bool, error) {
	list, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: railsLabelComponent + "=rails-fetch",
	})
	if err != nil {
		return false, err
	}
	self := railsLabelValue(a.config.NodeName)
	for i := range list.Items {
		p := &list.Items[i]
		if podEnded(p) || p.DeletionTimestamp != nil {
			continue
		}
		for _, s := range strings.Split(p.Annotations[railsAnnoSources], ",") {
			if s == self {
				return true, nil
			}
		}
	}
	return false, nil
}

func (a *Agent) startRailsServeReaper(ctx context.Context) {
	if !a.railsEnabled() {
		return
	}
	go func() {
		t := time.NewTicker(railsServeReapInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.reapServePod(ctx, time.Now())
			}
		}
	}()
}

// railsNodeStates reads which nodes run a rails fetch that has not ended,
// and which have a Ready serve pod, keyed by railsLabelValue.
func (a *Agent) railsNodeStates(ctx context.Context) (fetching, serving map[string]bool, err error) {
	list, err := a.kubeClient.CoreV1().Pods(a.config.Rails.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: railsLabelComponent + " in (rails-fetch,rails-serve)",
	})
	if err != nil {
		return nil, nil, err
	}
	fetching, serving = map[string]bool{}, map[string]bool{}
	for i := range list.Items {
		p := &list.Items[i]
		if podEnded(p) || p.DeletionTimestamp != nil {
			continue
		}
		switch p.Labels[railsLabelComponent] {
		case "rails-fetch":
			fetching[p.Labels[railsLabelFor]] = true
		case "rails-serve":
			if podReady(p) {
				serving[p.Labels[railsLabelServes]] = true
			}
		}
	}
	return fetching, serving, nil
}

func podReady(p *corev1.Pod) bool {
	if p.Status.Phase != corev1.PodRunning || len(p.Status.ContainerStatuses) == 0 {
		return false
	}
	for i := range p.Status.ContainerStatuses {
		if !p.Status.ContainerStatuses[i].Ready {
			return false
		}
	}
	return true
}
