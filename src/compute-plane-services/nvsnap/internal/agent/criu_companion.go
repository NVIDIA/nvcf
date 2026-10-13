// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

// Instance companions. An engine's state is not always its own: a Dynamo
// worker holds a lease in its instance's etcd and keeps registrations
// there, and a restored worker whose lease is gone shuts down. So an
// instance's capture also takes the pods without GPUs that its GPU ranks
// hold connections to in their namespace (the companions: an etcd, a
// NATS), and its restore brings them back with the ranks.
//
// Restored processes run in a time namespace that continues their clocks
// from the capture (CRIU restores the dumped clock offsets), so neither a
// worker nor its etcd sees the time the instance was down. The two must
// also resume together: a companion is restored stopped and continued once
// every GPU rank has resumed, so the leases its etcd holds do not run out
// before the workers renew them.
//
// A companion's connections are not kept: the ranks reach it through a
// Service whose address is new in the new instance. Once restored, every
// pod of the group closes its connections to peers outside the group
// (resetDeadPeers), so the engine sees them fail at once and reconnects by
// name.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/rootfsonly"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/webhook"
)

// isCompanion reports a pod admitted as a group restore's companion.
func isCompanion(p *corev1.Pod) bool {
	return p.Annotations[webhook.CRIUGroupCompanionAnnotation] == "true"
}

// groupMember reports whether p belongs to leader's instance: the same
// instance by its workload labels, or a companion of the same group
// restore.
func groupMember(leader, p *corev1.Pod) bool {
	if sameInstance(leader, p) {
		return true
	}
	key := leader.Annotations[webhook.CRIUGroupAnnotation]
	return key != "" && isCompanion(p) && p.Namespace == leader.Namespace && p.Annotations[webhook.CRIUGroupAnnotation] == key
}

// hasGPU reports a pod that requests GPUs.
func hasGPU(p *corev1.Pod) bool { return gpuContainer(p) != "" }

// discoverCompanions returns the companions of an instance: the pods of
// its namespace without GPUs that its ranks hold TCP connections to,
// directly or through a Service, in a stable order. It fails when one
// cannot be captured, rather than capture an instance without it.
func (a *Agent) discoverCompanions(ctx context.Context, ranks []*corev1.Pod, log *logrus.Entry) ([]*corev1.Pod, error) {
	ns := ranks[0].Namespace
	peers := map[string]bool{}
	for _, r := range ranks {
		base, err := a.peerAgentURL(ctx, r.Spec.NodeName)
		if err != nil {
			return nil, fmt.Errorf("rank %s: %w", r.Name, err)
		}
		ips, err := fetchPeers(ctx, base, r.Namespace, r.Name, gpuContainer(r))
		if err != nil {
			return nil, fmt.Errorf("rank %s: connections: %w", r.Name, err)
		}
		for _, ip := range ips {
			peers[ip] = true
		}
	}
	if len(peers) == 0 {
		return nil, nil
	}
	pods, err := a.kubeClient.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	svcs, err := a.kubeClient.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var slices []discoveryv1.EndpointSlice
	for _, svc := range svcs.Items {
		if !servesAny(svc, peers) {
			continue
		}
		l, err := a.kubeClient.DiscoveryV1().EndpointSlices(ns).List(ctx, metav1.ListOptions{LabelSelector: discoveryv1.LabelServiceName + "=" + svc.Name})
		if err != nil {
			return nil, err
		}
		slices = append(slices, l.Items...)
	}
	found := matchCompanions(peers, pods.Items, svcs.Items, slices, ranks)
	out := make([]*corev1.Pod, 0, len(found))
	for _, p := range found {
		if err := a.companionCapturable(ctx, p); err != nil {
			return nil, fmt.Errorf("companion %s: %w", p.Name, err)
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) > 0 {
		names := make([]string, len(out))
		for i, p := range out {
			names[i] = p.Name
		}
		log.WithField("companions", strings.Join(names, ",")).Info("CRIU group capture: the instance's companions")
	}
	return out, nil
}

// matchCompanions maps the ranks' peer addresses to the pods without GPUs
// of their namespace: a pod's own address, or a Service's cluster address
// and the pods of its endpoints.
func matchCompanions(peers map[string]bool, pods []corev1.Pod, svcs []corev1.Service, slices []discoveryv1.EndpointSlice, ranks []*corev1.Pod) map[string]*corev1.Pod {
	rank := map[string]bool{}
	for _, r := range ranks {
		rank[r.Name] = true
	}
	byIP, byName := map[string]*corev1.Pod{}, map[string]*corev1.Pod{}
	for i := range pods {
		p := &pods[i]
		if p.DeletionTimestamp != nil || rank[p.Name] || hasGPU(p) {
			continue
		}
		byName[p.Name] = p
		for _, ip := range p.Status.PodIPs {
			byIP[ip.IP] = p
		}
		if p.Status.PodIP != "" {
			byIP[p.Status.PodIP] = p
		}
	}
	found := map[string]*corev1.Pod{}
	for ip := range peers {
		if p := byIP[ip]; p != nil {
			found[p.Name] = p
		}
	}
	served := map[string]bool{}
	for _, svc := range svcs {
		if servesAny(svc, peers) {
			served[svc.Name] = true
		}
	}
	for _, sl := range slices {
		if !served[sl.Labels[discoveryv1.LabelServiceName]] {
			continue
		}
		for _, ep := range sl.Endpoints {
			if ep.TargetRef == nil || ep.TargetRef.Kind != "Pod" {
				continue
			}
			if p := byName[ep.TargetRef.Name]; p != nil {
				found[p.Name] = p
			}
		}
	}
	return found
}

// servesAny reports whether one of a Service's cluster addresses is among
// peers.
func servesAny(svc corev1.Service, peers map[string]bool) bool {
	ips := append([]string{svc.Spec.ClusterIP}, svc.Spec.ClusterIPs...)
	for _, ip := range ips {
		if ip != "" && ip != corev1.ClusterIPNone && peers[ip] {
			return true
		}
	}
	return false
}

// companionCapturable checks what a companion's capture and restore need:
// one container, ready, run by a controller (so a replacement pod comes to
// restore into), on a node with an agent.
func (a *Agent) companionCapturable(ctx context.Context, p *corev1.Pod) error {
	if len(p.Spec.Containers) != 1 {
		return fmt.Errorf("has %d containers; a companion is captured with one", len(p.Spec.Containers))
	}
	if !rootfsonly.IsPodReady(p) {
		return fmt.Errorf("is not Ready")
	}
	if metav1.GetControllerOf(p) == nil {
		return fmt.Errorf("has no controller to replace it")
	}
	if a.specCacheURI == nil {
		return fmt.Errorf("the agent's webhook is off, so no configuration to restore it by")
	}
	if _, err := a.peerAgentURL(ctx, p.Spec.NodeName); err != nil {
		return fmt.Errorf("node %s: %w", p.Spec.NodeName, err)
	}
	return nil
}

// criuCompanionAdmits is the webhook's check before admitting a pod
// without GPUs as a companion placeholder: no GPU pod of its namespace
// already runs outside a restore. A companion recreated next to a running
// instance (its etcd crashed) must start, not wait for a restore.
func (a *Agent) criuCompanionAdmits(ctx context.Context, pod *corev1.Pod) (bool, string) {
	pods, err := a.kubeClient.CoreV1().Pods(pod.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Sprintf("cannot list the namespace's pods: %v", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || !hasGPU(p) {
			continue
		}
		if p.Annotations[webhook.CRIUGroupAnnotation] != "" && p.Annotations[webhook.CRIURestoredAnnotation] == "" {
			continue // a placeholder of this restore
		}
		return false, fmt.Sprintf("GPU pod %s already runs; this pod replaces a companion of a running instance", p.Name)
	}
	return true, ""
}

// companionPlaceholderWait bounds how long a companion placeholder waits
// for its group's restore before it is released: its GPU ranks may have
// been admitted cold.
const companionPlaceholderWait = criuGroupPlaceholderWait + 5*time.Minute

// considerCompanionPlaceholder releases a companion placeholder no restore
// picked up, counting a stall on its record so the configuration starts
// cold after criuGroupMaxStalls of them.
func (c *criuAutoRestorer) considerCompanionPlaceholder(ctx context.Context, pod *corev1.Pod) {
	if !isCompanion(pod) || pod.Annotations[webhook.CRIURestoredAnnotation] != "" || pod.DeletionTimestamp != nil {
		return
	}
	if time.Since(pod.CreationTimestamp.Time) < companionPlaceholderWait {
		return
	}
	if _, busy := c.attempted.LoadOrStore("companion/"+string(pod.UID), struct{}{}); busy {
		return
	}
	key := pod.Annotations[webhook.CRIUGroupAnnotation]
	log := c.a.log.WithFields(logrus.Fields{"pod": pod.Namespace + "/" + pod.Name, "criuGroup": key})
	c.failGroup(ctx, pod, key, false, fmt.Errorf("companion placeholder not restored after %s", companionPlaceholderWait), log)
}

// peersHandler lists a pod's established TCP peers, for the agent that
// captures its instance.
func (a *Agent) peersHandler(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	info, err := a.runtime.FindContainerByPod(r.Context(), v["namespace"], v["pod"], r.URL.Query().Get("container"))
	if err != nil || info == nil || info.PID == 0 {
		http.Error(w, fmt.Sprintf("container not found: %v", err), http.StatusNotFound)
		return
	}
	ips, err := tcpPeersInNS(int(info.PID), a.log.WithField("pod", v["namespace"]+"/"+v["pod"]))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func fetchPeers(ctx context.Context, base, ns, pod, container string) ([]string, error) {
	u := fmt.Sprintf("%s/v1/pods/%s/%s/peers?container=%s", base, url.PathEscape(ns), url.PathEscape(pod), url.QueryEscape(container))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := fabricHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var ips []string
	return ips, json.Unmarshal(b, &ips)
}

// ContinueRequest names a restored container whose processes the restore
// left stopped.
type ContinueRequest struct {
	Namespace     string `json:"namespace"`
	PodName       string `json:"podName"`
	ContainerName string `json:"containerName,omitempty"`
}

// continueHandler sends SIGCONT to every process of a restored container.
func (a *Agent) continueHandler(w http.ResponseWriter, r *http.Request) {
	var req ContinueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}
	info, err := a.runtime.FindContainerByPod(r.Context(), req.Namespace, req.PodName, req.ContainerName)
	if err != nil || info == nil || info.PID == 0 {
		http.Error(w, fmt.Sprintf("container not found: %v", err), http.StatusNotFound)
		return
	}
	n := continueTree(hostProcBase(), int(info.PID))
	a.log.WithFields(logrus.Fields{"pod": req.Namespace + "/" + req.PodName, "processes": n}).Info("CRIU group restore: companion continued")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"continued": n})
}

// continueTree sends SIGCONT to the container's processes, the restored
// tree under its placeholder included.
func continueTree(procBase string, hostPID int) int {
	n := 0
	for _, p := range processTree(procBase, hostPID) {
		if syscall.Kill(p, syscall.SIGCONT) == nil {
			n++
		}
	}
	return n
}

func postContinue(ctx context.Context, base string, req ContinueRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/restore/continue", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := fabricHTTPClient.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// resetDeadPeers closes, in a restored pod's network namespace, the
// connections whose peer is not part of the restored group: their peer's
// address belonged to the captured instance and no longer answers, and an
// engine would otherwise wait for a timeout before it reconnects. keep
// holds the addresses of the group's restored pods.
func resetDeadPeers(hostPID int, keep map[string]bool, log *logrus.Entry) {
	n, err := destroyTCPInNS(hostPID, func(remote net.IP) bool {
		return remote.IsLoopback() || keep[remote.String()]
	}, "CRIU restore: closed a connection to a peer outside the restored group", log)
	if err != nil {
		log.WithError(err).Warn("CRIU restore: cannot close the connections to peers outside the group")
		return
	}
	if n > 0 {
		log.WithField("closed", n).Info("CRIU restore: connections to peers outside the group closed; the engine reconnects")
	}
}

// keepAddresses is the set of addresses a restored pod keeps connections
// to: its own and every new address of an inet address map
// ("OLD=NEW,...").
func keepAddresses(own string, inetAddrMap string) map[string]bool {
	keep := map[string]bool{}
	if ip := net.ParseIP(own); ip != nil {
		keep[ip.String()] = true
	}
	for _, pair := range strings.Split(inetAddrMap, ",") {
		if _, nw, ok := strings.Cut(pair, "="); ok {
			if ip := net.ParseIP(strings.TrimSpace(nw)); ip != nil {
				keep[ip.String()] = true
			}
		}
	}
	return keep
}

// hostProcBase is the node's /proc as the agent sees it.
func hostProcBase() string {
	if _, err := os.Stat("/host/proc"); err == nil {
		return "/host/proc"
	}
	return "/proc"
}
