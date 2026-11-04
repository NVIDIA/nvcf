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

// Package clusterdump collects a diagnostic snapshot of a self-managed NVCF stack
// across the control-plane and compute-plane clusters. It consolidates the
// manual sequence operators otherwise run by hand: `helm list -A`,
// `kubectl get pods`, `kubectl get nvcfbackend`, and warning-event triage,
// against one or two kubeconfig contexts.
//
// The collector is read-only and resilient: failures from individual probes
// (a missing helm binary, an absent CRD, an unreachable namespace) are demoted
// to per-plane Warnings rather than aborting the whole dump, so a partially
// degraded cluster still produces a useful report.
package clusterdump

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// Plane role identifiers. They tag each PlaneSnapshot and drive the section
// headings in the text renderer.
const (
	RoleControlPlane = "control-plane"
	RoleComputePlane = "compute-plane"
)

// gpuResourceName is the extended resource the NVIDIA device plugin advertises
// on GPU nodes. Its capacity is summed across nodes for the compute-plane GPU
// count.
const gpuResourceName = corev1.ResourceName("nvidia.com/gpu")

// maxEventsPerNamespace bounds the warning events reported per namespace so a
// noisy namespace cannot drown the report.
const maxEventsPerNamespace = 10

// nvcfBackendGVR is the NVCFBackend custom resource, read from the compute
// plane. Mirrors the constant in internal/clusteragent/k8s_inspector.go; kept
// local because that one is unexported.
var nvcfBackendGVR = schema.GroupVersionResource{
	Group: "nvcf.nvidia.io", Version: "v1", Resource: "nvcfbackends",
}

// ControlPlaneNamespaces lists the stack-owned namespaces probed on the
// control-plane cluster. Source: tests/bdd/scripts/destroy-stack.sh allow-list.
func ControlPlaneNamespaces() []string {
	return []string{
		"nvcf",
		"nats-system",
		"sis",
		"api-keys",
		"cassandra-system",
		"vault-system",
		"ess",
		"envoy-gateway-system",
	}
}

// ComputePlaneNamespaces lists the stack-owned namespaces probed on the
// compute-plane cluster. Source: tests/bdd/scripts/destroy-stack.sh allow-list.
func ComputePlaneNamespaces() []string {
	return []string{
		"nvca-operator",
		"nvca-system",
		"nvcf-backend",
	}
}

// HumanDuration renders as a compact age string ("45s", "10m", "3h", "2d") in
// both JSON and text output, instead of the raw nanosecond count time.Duration
// would otherwise marshal to.
type HumanDuration time.Duration

func (d HumanDuration) String() string { return humanizeDuration(time.Duration(d)) }

// MarshalJSON emits the compact human string.
func (d HumanDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// HelmRelease is one row from `helm list -A -o json`.
type HelmRelease struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart"`
	AppVersion string `json:"appVersion"`
	Status     string `json:"status"`
	Updated    string `json:"updated,omitempty"`
}

// PodRow is a summarised pod for the dump table.
type PodRow struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Ready     string        `json:"ready"` // "1/1"
	Status    string        `json:"status"`
	Restarts  int32         `json:"restarts"`
	Age       HumanDuration `json:"age"`
}

// NVCFBackendRow summarises the NVCFBackend CR on the compute plane.
type NVCFBackendRow struct {
	Name        string        `json:"name"`
	AgentStatus string        `json:"agentStatus"`
	NVCAVersion string        `json:"nvcaVersion"`
	GPUCapacity int64         `json:"gpuCapacity"`
	Age         HumanDuration `json:"age"`
}

// EventRow is one warning event.
type EventRow struct {
	Namespace string `json:"namespace"`
	Object    string `json:"object"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
}

// NodeSummary describes the node fleet for a plane.
type NodeSummary struct {
	Ready    int   `json:"ready"`
	Total    int   `json:"total"`
	GPUCount int64 `json:"gpuCount"` // 0 on the control plane
}

// NodeRow is per-node detail for the node table. Cordoned or pressured nodes are
// a common cause of scheduling failures, so they are surfaced explicitly.
type NodeRow struct {
	Name           string        `json:"name"`
	Ready          bool          `json:"ready"`
	Schedulable    bool          `json:"schedulable"`
	Roles          string        `json:"roles,omitempty"`
	GPU            int64         `json:"gpu,omitempty"`
	KubeletVersion string        `json:"kubeletVersion,omitempty"`
	Pressures      []string      `json:"pressures,omitempty"`
	Age            HumanDuration `json:"age"`
}

// PlaneSnapshot is the collected state for one cluster (control or compute).
type PlaneSnapshot struct {
	Role              string           `json:"role"`
	Context           string           `json:"context"`
	KubernetesVersion string           `json:"kubernetesVersion"`
	Nodes             NodeSummary      `json:"nodes"`
	HelmReleases      []HelmRelease    `json:"helmReleases"`
	NodeDetails       []NodeRow        `json:"nodeDetails,omitempty"`
	Pods              []PodRow         `json:"pods"` // all pods; unhealthy count shown in the summary header
	NVCFBackends      []NVCFBackendRow `json:"nvcfBackends,omitempty"`
	Events            []EventRow       `json:"events"`
	Warnings          []string         `json:"warnings,omitempty"`

	// Deep-capture sections (populated only when DumpOptions enables them).
	// Resources carries an index in JSON; the raw YAML bodies and pod logs are
	// written to the dump tree, not inlined into dump.json.
	Resources       []CapturedResource   `json:"capturedResources,omitempty"`
	PodLogs         []PodLog             `json:"-"`
	ICMSRequests    []ICMSRequestSummary `json:"icmsRequests,omitempty"` // compute plane
	GPU             *GPUReconciliation   `json:"gpu,omitempty"`          // compute plane
	HelmDetails     []HelmReleaseDetail  `json:"helmDetails,omitempty"`
	StuckNamespaces []StuckNamespace     `json:"stuckNamespaces,omitempty"`
}

// Dump is the full diagnostic snapshot. Either plane may be nil: control-only
// (single-cluster) omits ComputePlane, and compute-only omits ControlPlane.
type Dump struct {
	CollectedAt  time.Time      `json:"collectedAt"`
	ControlPlane *PlaneSnapshot `json:"controlPlane,omitempty"`
	ComputePlane *PlaneSnapshot `json:"computePlane,omitempty"`
}

// HelmRunner runs `helm list` and returns the parsed releases. It is an exec
// seam so tests can replace the shell-out.
type HelmRunner interface {
	ListReleases(ctx context.Context, kubeCtx string) ([]HelmRelease, error)
}

// PlaneCollector gathers the snapshot for a single cluster plane.
type PlaneCollector struct {
	Role       string
	Context    string
	Kube       kubernetes.Interface
	Dynamic    dynamic.Interface            // dynamic client for CRD reads
	Discovery  discovery.DiscoveryInterface // resolves served CRD versions; nil falls back to default GVR versions
	Helm       HelmRunner
	Namespaces []string
	NowFunc    func() time.Time // clock seam; defaults to time.Now().UTC()
	Options    DumpOptions      // deep-capture controls; zero value = lightweight snapshot only

	servedOnce sync.Once
	served     map[schema.GroupResource]string // resolved preferred version per group/resource
}

func (pc *PlaneCollector) now() time.Time {
	if pc.NowFunc != nil {
		return pc.NowFunc()
	}
	return time.Now().UTC()
}

// Collector composes a Dump from one or both PlaneCollectors. Either may be nil:
// Control nil = compute-only, Compute nil = control-only (single-cluster).
type Collector struct {
	Control *PlaneCollector
	Compute *PlaneCollector
}

// Collect runs the configured planes concurrently and assembles the Dump. It
// never returns an error today; per-probe failures surface as
// PlaneSnapshot.Warnings.
func (c *Collector) Collect(ctx context.Context) (Dump, error) {
	var d Dump
	switch {
	case c.Control != nil:
		d.CollectedAt = c.Control.now()
	case c.Compute != nil:
		d.CollectedAt = c.Compute.now()
	}

	var wg sync.WaitGroup
	if c.Control != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := c.Control.collect(ctx)
			d.ControlPlane = &s
		}()
	}
	if c.Compute != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := c.Compute.collect(ctx)
			d.ComputePlane = &s
		}()
	}
	wg.Wait()
	return d, nil
}

// collect runs every probe for one plane concurrently. Each probe writes its
// own result variable; failures are appended to a mutex-guarded warning list.
func (pc *PlaneCollector) collect(ctx context.Context) PlaneSnapshot {
	snap := PlaneSnapshot{Role: pc.Role, Context: pc.Context}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		nodes    NodeSummary
		nodeRows []NodeRow
		k8sVer   string
		releases []HelmRelease
		pods     []PodRow
		events   []EventRow
		backends []NVCFBackendRow

		resources     []CapturedResource
		podLogs       []PodLog
		icmsSummaries []ICMSRequestSummary
		icmsResources []CapturedResource
		icmsLogs      []PodLog
		gpu           *GPUReconciliation
		helmDetails   []HelmReleaseDetail
		stuckNS       []StuckNamespace
	)

	warn := func(format string, args ...interface{}) {
		mu.Lock()
		snap.Warnings = append(snap.Warnings, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	run := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}

	run(func() {
		ns, rows, err := pc.collectNodes(ctx)
		nodes = ns
		nodeRows = rows
		if err != nil {
			warn("nodes: %v", err)
		}
	})
	run(func() {
		v, err := pc.collectVersion()
		if err != nil {
			warn("kubernetes version: %v", err)
			return
		}
		k8sVer = v
	})
	run(func() {
		r, err := pc.Helm.ListReleases(ctx, pc.Context)
		if err != nil {
			warn("helm: %v", err)
			return
		}
		releases = r
	})
	run(func() {
		p, err := pc.collectPods(ctx)
		pods = p
		if err != nil {
			warn("pods: %v", err)
		}
	})
	run(func() {
		e, err := pc.collectEvents(ctx)
		events = e
		if err != nil {
			warn("events: %v", err)
		}
	})
	// --- Always-on summaries (cheap; shown in the stdout report, not just the
	// bundle). These give the compute plane a substantive default view. ---

	// NVCFBackend is a compute-plane CR; gate on role so a control-plane dynamic
	// client (now built for deep resource capture) does not probe a CRD that is
	// not installed there.
	if pc.Dynamic != nil && pc.Role == RoleComputePlane {
		run(func() {
			b, err := pc.collectNVCFBackends(ctx)
			if err != nil {
				warn("nvcfbackend: %v", err)
				return
			}
			backends = b
		})
		run(func() {
			s, crs, ws := pc.collectICMSSummaries(ctx)
			icmsSummaries, icmsResources = s, crs
			for _, m := range ws {
				warn("%s", m)
			}
		})
		run(func() {
			g, ws := pc.collectGPUReconciliation(ctx)
			gpu = g
			for _, m := range ws {
				warn("%s", m)
			}
		})
	}
	if pc.Dynamic != nil {
		run(func() {
			s, ws := pc.collectStuckNamespaces(ctx)
			stuckNS = s
			for _, m := range ws {
				warn("%s", m)
			}
		})
	}

	// --- Bundle-only heavy capture (writes files: raw YAML, pod logs, helm
	// manifests). Gated behind --bundle via DumpOptions sections. ---
	if pc.Options.Deep() {
		if pc.Options.Has(SectionResources) && pc.Dynamic != nil {
			run(func() {
				r, ws, err := pc.collectStackResources(ctx)
				resources = r
				for _, m := range ws {
					warn("%s", m)
				}
				if err != nil {
					warn("resources: %v", err)
				}
			})
		}
		if pc.Options.Has(SectionLogs) {
			run(func() {
				l, ws, err := pc.collectPodLogs(ctx)
				podLogs = l
				for _, m := range ws {
					warn("%s", m)
				}
				if err != nil {
					warn("logs: %v", err)
				}
			})
		}
		if pc.Options.Has(SectionHelm) {
			run(func() {
				h, ws := pc.collectHelmDetails(ctx)
				helmDetails = h
				for _, m := range ws {
					warn("%s", m)
				}
			})
		}
	}

	wg.Wait()

	// The helm-function namespace capture depends on the ICMS summaries above,
	// so it runs after the barrier (bundle only).
	if pc.Options.Deep() && pc.Options.Has(SectionResources) && pc.Dynamic != nil && pc.Role == RoleComputePlane {
		r, l, ws := pc.collectICMSHelmCaptures(ctx, icmsSummaries)
		icmsResources = append(icmsResources, r...)
		icmsLogs = append(icmsLogs, l...)
		for _, m := range ws {
			snap.Warnings = append(snap.Warnings, m)
		}
	}

	snap.Nodes = nodes
	snap.NodeDetails = nodeRows
	snap.KubernetesVersion = k8sVer
	snap.HelmReleases = releases
	snap.Pods = pods
	snap.Events = events
	snap.NVCFBackends = backends
	snap.Resources = append(resources, icmsResources...)
	snap.PodLogs = append(podLogs, icmsLogs...)
	snap.ICMSRequests = icmsSummaries
	snap.GPU = gpu
	snap.HelmDetails = helmDetails
	snap.StuckNamespaces = stuckNS
	// Stable warning order for deterministic output (goroutines append in
	// nondeterministic order).
	sort.Strings(snap.Warnings)
	return snap
}

func (pc *PlaneCollector) collectVersion() (string, error) {
	info, err := pc.Kube.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}
	return info.GitVersion, nil
}

func (pc *PlaneCollector) collectNodes(ctx context.Context) (NodeSummary, []NodeRow, error) {
	var ns NodeSummary
	nodes, err := pc.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return ns, nil, err
	}
	now := pc.now()
	ns.Total = len(nodes.Items)
	rows := make([]NodeRow, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		ready := nodeReady(n)
		if ready {
			ns.Ready++
		}
		var gpu int64
		if q, ok := n.Status.Capacity[gpuResourceName]; ok {
			gpu = q.Value()
			ns.GPUCount += gpu
		}
		rows = append(rows, NodeRow{
			Name:           n.Name,
			Ready:          ready,
			Schedulable:    !n.Spec.Unschedulable,
			Roles:          nodeRoles(n),
			GPU:            gpu,
			KubeletVersion: n.Status.NodeInfo.KubeletVersion,
			Pressures:      nodePressures(n),
			Age:            HumanDuration(now.Sub(n.CreationTimestamp.Time)),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return ns, rows, nil
}

// nodeRoles extracts the node-role.kubernetes.io/* labels.
func nodeRoles(n *corev1.Node) string {
	var roles []string
	for k := range n.Labels {
		if r := strings.TrimPrefix(k, "node-role.kubernetes.io/"); r != k && r != "" {
			roles = append(roles, r)
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		return "<none>"
	}
	return strings.Join(roles, ",")
}

// nodePressures returns any active pressure/availability conditions on a node.
func nodePressures(n *corev1.Node) []string {
	var out []string
	for _, c := range n.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case corev1.NodeMemoryPressure:
			out = append(out, "MemoryPressure")
		case corev1.NodeDiskPressure:
			out = append(out, "DiskPressure")
		case corev1.NodePIDPressure:
			out = append(out, "PIDPressure")
		case corev1.NodeNetworkUnavailable:
			out = append(out, "NetworkUnavailable")
		}
	}
	return out
}

func (pc *PlaneCollector) collectPods(ctx context.Context) ([]PodRow, error) {
	var rows []PodRow
	var firstErr error
	now := pc.now()
	for _, namespace := range pc.Namespaces {
		list, err := pc.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for i := range list.Items {
			rows = append(rows, podRow(&list.Items[i], now))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Namespace != rows[j].Namespace {
			return rows[i].Namespace < rows[j].Namespace
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, firstErr
}

func (pc *PlaneCollector) collectEvents(ctx context.Context) ([]EventRow, error) {
	var rows []EventRow
	var firstErr error
	for _, namespace := range pc.Namespaces {
		// Filter to Warning events server-side and paginate, so a busy namespace
		// does not return tens of thousands of Normal events we would discard.
		// The client-side check is kept as a defensive guard.
		var warnings []corev1.Event
		cont := ""
		for {
			list, err := pc.Kube.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
				FieldSelector: "type=Warning",
				Limit:         stackListPageSize,
				Continue:      cont,
			})
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			for i := range list.Items {
				if list.Items[i].Type == corev1.EventTypeWarning {
					warnings = append(warnings, list.Items[i])
				}
			}
			cont = list.Continue
			if cont == "" {
				break
			}
		}
		// Most-recent first, then cap per namespace.
		sort.Slice(warnings, func(i, j int) bool {
			return eventTime(&warnings[i]).After(eventTime(&warnings[j]))
		})
		if len(warnings) > maxEventsPerNamespace {
			warnings = warnings[:maxEventsPerNamespace]
		}
		for i := range warnings {
			rows = append(rows, eventRow(&warnings[i]))
		}
	}
	return rows, firstErr
}

func (pc *PlaneCollector) collectNVCFBackends(ctx context.Context) ([]NVCFBackendRow, error) {
	list, err := pc.Dynamic.Resource(nvcfBackendGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	now := pc.now()
	rows := make([]NVCFBackendRow, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		obj := item.Object
		rows = append(rows, NVCFBackendRow{
			Name:        item.GetName(),
			AgentStatus: firstNonEmpty(nestedStr(obj, "status", "agentStatus"), "Unknown"),
			NVCAVersion: nestedStr(obj, "spec", "version"),
			GPUCapacity: backendGPUCapacity(obj),
			Age:         HumanDuration(now.Sub(item.GetCreationTimestamp().Time)),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// --- row builders ---

func podRow(p *corev1.Pod, now time.Time) PodRow {
	ready, total := 0, len(p.Spec.Containers)
	var restarts int32
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			ready++
		}
		restarts += cs.RestartCount
	}
	status := string(p.Status.Phase)
	if r := podReason(p); r != "" {
		status = r
	}
	return PodRow{
		Namespace: p.Namespace,
		Name:      p.Name,
		Ready:     fmt.Sprintf("%d/%d", ready, total),
		Status:    status,
		Restarts:  restarts,
		Age:       HumanDuration(now.Sub(p.CreationTimestamp.Time)),
	}
}

// podReason returns a more specific status than the phase when the pod is
// blocked: a pod-level reason (Evicted, NodeLost) or the first container
// waiting/terminated reason (CrashLoopBackOff, ImagePullBackOff, Error).
func podReason(p *corev1.Pod) string {
	if p.Status.Reason != "" {
		return p.Status.Reason
	}
	for _, cs := range p.Status.ContainerStatuses {
		switch {
		case cs.State.Waiting != nil && cs.State.Waiting.Reason != "":
			return cs.State.Waiting.Reason
		case cs.State.Terminated != nil && cs.State.Terminated.Reason != "":
			return cs.State.Terminated.Reason
		}
	}
	return ""
}

func eventRow(e *corev1.Event) EventRow {
	obj := e.InvolvedObject.Name
	if kind := e.InvolvedObject.Kind; kind != "" {
		obj = strings.ToLower(kind) + "/" + e.InvolvedObject.Name
	}
	return EventRow{
		Namespace: e.Namespace,
		Object:    obj,
		Reason:    e.Reason,
		Message:   strings.TrimSpace(e.Message),
	}
}

func eventTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func backendGPUCapacity(obj map[string]interface{}) int64 {
	raw, found, err := unstructured.NestedMap(obj, "status", "gpuUsage")
	if !found || err != nil {
		return 0
	}
	var total int64
	for _, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		total += nestedInt64(m, "capacity")
	}
	return total
}

// --- exec HelmRunner ---

// NewExecHelmRunner returns a HelmRunner that shells out to the helm binary.
func NewExecHelmRunner() HelmRunner { return &execHelmRunner{} }

type execHelmRunner struct{}

// helmListEntry maps the snake_case fields of `helm list -o json`.
type helmListEntry struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Chart      string `json:"chart"`
	AppVersion string `json:"app_version"`
	Status     string `json:"status"`
	Updated    string `json:"updated"`
}

func (h *execHelmRunner) ListReleases(ctx context.Context, kubeCtx string) ([]HelmRelease, error) {
	if _, err := exec.LookPath("helm"); err != nil {
		return nil, fmt.Errorf("helm not found in PATH: %w", err)
	}
	args := []string{"list", "-A", "-o", "json"}
	if kubeCtx != "" {
		args = append([]string{"--kube-context", kubeCtx}, args...)
	}
	out, err := exec.CommandContext(ctx, "helm", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("helm list: %w", err)
	}
	var entries []helmListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse helm list output: %w", err)
	}
	releases := make([]HelmRelease, 0, len(entries))
	for _, e := range entries {
		releases = append(releases, HelmRelease{
			Name:       e.Name,
			Namespace:  e.Namespace,
			Chart:      e.Chart,
			AppVersion: e.AppVersion,
			Status:     e.Status,
			Updated:    e.Updated,
		})
	}
	return releases, nil
}

// --- renderers ---

// RenderJSON writes the dump as indented JSON.
func RenderJSON(w io.Writer, d Dump) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

// RenderText writes the human-readable report for whichever planes are present.
func RenderText(w io.Writer, d Dump) error {
	fmt.Fprintf(w, "NVCF Cluster Dump  %s\n", d.CollectedAt.UTC().Format(time.RFC3339))
	if d.ControlPlane != nil {
		renderPlane(w, "CONTROL PLANE", *d.ControlPlane)
	}
	if d.ComputePlane != nil {
		renderPlane(w, "COMPUTE PLANE", *d.ComputePlane)
	}
	return nil
}

func renderPlane(w io.Writer, title string, p PlaneSnapshot) {
	ctxLabel := p.Context
	if ctxLabel == "" {
		ctxLabel = "(current context)"
	}
	fmt.Fprintf(w, "\n%s  %s\n", title, ctxLabel)

	version := p.KubernetesVersion
	if version == "" {
		version = "unknown"
	}
	fmt.Fprintf(w, "  Kubernetes   %s\n", version)

	nodeLine := fmt.Sprintf("%d ready", p.Nodes.Ready)
	if p.Nodes.Ready != p.Nodes.Total {
		nodeLine = fmt.Sprintf("%d/%d ready", p.Nodes.Ready, p.Nodes.Total)
	}
	if p.Nodes.GPUCount > 0 {
		nodeLine += fmt.Sprintf("  (%d GPU capacity)", p.Nodes.GPUCount)
	}
	fmt.Fprintf(w, "  Nodes        %s\n", nodeLine)

	if len(p.NodeDetails) > 0 {
		tw := newTab(w)
		fmt.Fprintln(tw, "  NAME\tSTATUS\tROLES\tGPU\tVERSION\tAGE")
		for _, n := range p.NodeDetails {
			status := "Ready"
			if !n.Ready {
				status = "NotReady"
			}
			if !n.Schedulable {
				status += ",cordoned"
			}
			for _, pr := range n.Pressures {
				status += "," + pr
			}
			gpu := ""
			if n.GPU > 0 {
				gpu = fmt.Sprintf("%d", n.GPU)
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", n.Name, status, n.Roles, gpu, n.KubeletVersion, n.Age)
		}
		_ = tw.Flush()
	}

	helmHeader := "Helm Releases"
	if nd := helmNotDeployed(p.HelmReleases); nd > 0 {
		helmHeader = fmt.Sprintf("Helm Releases  (%d not deployed)", nd)
	}
	fmt.Fprintf(w, "\n  %s\n", helmHeader)
	if len(p.HelmReleases) == 0 {
		fmt.Fprintf(w, "  (none)\n")
	} else {
		tw := newTab(w)
		fmt.Fprintln(tw, "  NAME\tNAMESPACE\tCHART\tVERSION\tSTATUS")
		for _, r := range p.HelmReleases {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", r.Name, r.Namespace, r.Chart, r.AppVersion, r.Status)
		}
		_ = tw.Flush()
	}

	if len(p.NVCFBackends) > 0 {
		fmt.Fprintf(w, "\n  NVCFBackend\n")
		tw := newTab(w)
		fmt.Fprintln(tw, "  NAME\tAGENT STATUS\tVERSION\tGPU CAPACITY\tAGE")
		for _, b := range p.NVCFBackends {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\n", b.Name, b.AgentStatus, b.NVCAVersion, b.GPUCapacity, b.Age)
		}
		_ = tw.Flush()
	}

	if p.GPU != nil {
		g := p.GPU
		verdict := "MATCH"
		if !g.Match {
			verdict = "MISMATCH"
		}
		fmt.Fprintf(w, "\n  GPU (NVCFBackend vs nodes)\n")
		fmt.Fprintf(w, "  capacity %d  allocated %d (pods %d + miniservice %d)  available %d  [%s]\n",
			g.BackendCapacity, g.BackendAllocated, g.NodeAllocatedByPods, g.MiniServiceReserved, g.BackendAvailable, verdict)
		if len(g.Cliques) > 1 {
			tw := newTab(w)
			fmt.Fprintln(tw, "  CLIQUE\tCAPACITY\tALLOCATED\tAVAILABLE\tNODES")
			for _, c := range g.Cliques {
				fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\n", c.Clique, c.Capacity, c.Allocated, c.Available, c.Nodes)
			}
			_ = tw.Flush()
		}
	}

	if len(p.ICMSRequests) > 0 {
		failed := len(failedICMS(p.ICMSRequests))
		fmt.Fprintf(w, "\n  ICMSRequests  %d total, %d failed\n", len(p.ICMSRequests), failed)
		tw := newTab(w)
		fmt.Fprintln(tw, "  NAME\tTYPE\tSTATUS\tFUNCTION-ID")
		for _, r := range p.ICMSRequests {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", r.Name, r.Type, r.RequestStatus, r.FunctionID)
		}
		_ = tw.Flush()
	}

	unhealthyN := 0
	for _, pod := range p.Pods {
		if !podRowHealthy(pod) {
			unhealthyN++
		}
	}
	fmt.Fprintf(w, "\n  Pods  %d total, %d unhealthy\n", len(p.Pods), unhealthyN)
	if len(p.Pods) == 0 {
		fmt.Fprintf(w, "  (none)\n")
	} else {
		tw := newTab(w)
		fmt.Fprintln(tw, "  NAMESPACE\tNAME\tREADY\tSTATUS\tRESTARTS\tAGE")
		for _, pod := range p.Pods {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%d\t%s\n", pod.Namespace, pod.Name, pod.Ready, pod.Status, pod.Restarts, pod.Age)
		}
		_ = tw.Flush()
	}

	fmt.Fprintf(w, "\n  Warning Events (last %d per namespace)\n", maxEventsPerNamespace)
	if len(p.Events) == 0 {
		fmt.Fprintf(w, "  (none)\n")
	} else {
		tw := newTab(w)
		fmt.Fprintln(tw, "  NAMESPACE\tOBJECT\tREASON\tMESSAGE")
		for _, e := range p.Events {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", e.Namespace, e.Object, e.Reason, truncate(e.Message, 80))
		}
		_ = tw.Flush()
	}

	if len(p.StuckNamespaces) > 0 {
		fmt.Fprintf(w, "\n  Stuck Namespaces (Terminating)\n")
		for _, sn := range p.StuckNamespaces {
			fmt.Fprintf(w, "  - %s: %d object(s) holding finalizers\n", sn.Name, len(sn.RemainingObjects))
		}
	}

	if len(p.Warnings) > 0 {
		fmt.Fprintf(w, "\n  Collection Warnings\n")
		for _, warning := range p.Warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
}

func newTab(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

// helmNotDeployed counts releases not in the "deployed" state (failed,
// pending-upgrade, superseded, etc.).
func helmNotDeployed(releases []HelmRelease) int {
	n := 0
	for _, r := range releases {
		if r.Status != "deployed" {
			n++
		}
	}
	return n
}

// podRowHealthy reports whether a pod is in a steady, healthy state and can be
// hidden from the default text view.
func podRowHealthy(p PodRow) bool {
	switch p.Status {
	case "Succeeded", "Completed":
		return true
	case "Running":
		ready, total, ok := parseReady(p.Ready)
		return ok && total > 0 && ready == total
	default:
		return false
	}
}

// parseReady parses an "n/m" ready string.
func parseReady(s string) (ready, total int, ok bool) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	r, err1 := atoi(parts[0])
	t, err2 := atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return r, t, true
}

func atoi(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	return n, nil
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// --- unstructured field helpers (missing fields degrade to zero values) ---

func nestedStr(obj map[string]interface{}, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj, fields...)
	return v
}

func nestedInt64(obj map[string]interface{}, fields ...string) int64 {
	if v, found, err := unstructured.NestedInt64(obj, fields...); found && err == nil {
		return v
	}
	// JSON-decoded counts often arrive as float64.
	if f, found, err := unstructured.NestedFloat64(obj, fields...); found && err == nil {
		return int64(f)
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
