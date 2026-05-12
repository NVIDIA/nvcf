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

// Package status implements the steady-state status collector for
// `nvcf self-hosted status` (SRD/SDD §6.5). It composes a snapshot from
// Kubernetes component probes + SIS cluster listing and emits typed events to
// an EventSink in the order: Snapshot → ComponentHealth × N → ClusterRow × M.
package status

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted/progress"
)

const (
	roleControlPlane = "control-plane"
	roleComputePlane = "compute-plane"
)

// ClusterLister is the SIS subset the collector needs.
type ClusterLister interface {
	ListClusters(ctx context.Context, sisURL, ncaID string) ([]client.SISCluster, error)
}

// Collector composes status snapshots from SIS + kube data.
//
// Dual-context mode (M+9.F): when ComputePlaneKube is non-nil the collector
// probes control-plane components against ControlPlaneKube and compute-plane
// components against ComputePlaneKube. ComponentHealth events are tagged with
// Role="control-plane" or Role="compute-plane" so renderers can split the
// display panel.
//
// Single-cluster mode: set only ControlPlaneKube (leave ComputePlaneKube nil)
// OR keep the legacy Kube field. ComponentHealth events get Role="" so
// renderers preserve the existing flat output.
type Collector struct {
	// ControlPlaneKube + ComputePlaneKube are the two kube clients. When
	// ComputePlaneKube is nil (single-cluster mode) ControlPlaneKube is used
	// for all components, and ComponentHealth events get Role="" so renderers
	// don't show a split panel.
	ControlPlaneKube kubernetes.Interface
	ComputePlaneKube kubernetes.Interface // optional; nil → single-cluster

	// Kube is the legacy single-client field. When ControlPlaneKube is not set,
	// Kube is used as the fallback so existing callers keep working without
	// requiring a refactor at the call site.
	Kube kubernetes.Interface

	SIS    ClusterLister
	SISURL string
	NCAID  string

	Cluster  string                    // local cluster name (for the snapshot's identity.Cluster)
	Identity progress.SnapshotIdentity // pre-populated from state file (clusterId, target, stack)

	// Components is the union list. Each spec carries a Role hint that the
	// collector uses to pick which kube client to probe AND tag the emitted
	// ComponentHealth event with.
	Components []ComponentSpec // well-known components to query

	// ComputePlaneContext is the kubeconfig context for the compute-plane kube
	// client. Used to set Context on ClusterRow events for the IsCurrent cluster.
	ComputePlaneContext string

	NowFunc func() time.Time // clock seam, defaults to time.Now
}

// ComponentSpec identifies a component to probe by namespace + kind + name.
type ComponentSpec struct {
	Name      string // human-readable, e.g. "SIS"
	Namespace string
	Kind      string // "deployment" | "statefulset"
	Resource  string // resource name in cluster
	Cluster   string // optional: only set for compute-plane components like nvca-worker
	Role      string // M+9: "control-plane" | "compute-plane"; empty in single-cluster mode
}

// DefaultComponents returns the canonical control-plane component list.
// This is the source-of-truth list used by the status command when the
// caller doesn't override Components. Order matches §6.5.1 mock; namespace
// + resource names match the nvcf-self-managed-stack helmfile-rendered
// workloads (verified on mcamp-dev-vm 2026-04-29).
func DefaultComponents() []ComponentSpec {
	return []ComponentSpec{
		{Name: "SIS", Role: roleControlPlane, Namespace: "sis", Kind: "deployment", Resource: "spot-instance-service"},
		{Name: "NATS", Role: roleControlPlane, Namespace: "nats-system", Kind: "statefulset", Resource: "nats"},
		{Name: "Cassandra", Role: roleControlPlane, Namespace: "cassandra-system", Kind: "statefulset", Resource: "cassandra"},
		{Name: "OpenBao", Role: roleControlPlane, Namespace: "vault-system", Kind: "statefulset", Resource: "openbao-server"},
		{Name: "API Keys", Role: roleControlPlane, Namespace: "api-keys", Kind: "deployment", Resource: "api-keys"},
		{Name: "NVCF API", Role: roleControlPlane, Namespace: "nvcf", Kind: "deployment", Resource: "nvcf-api"},
		{Name: "Reval", Role: roleControlPlane, Namespace: "nvcf", Kind: "deployment", Resource: "reval"},
		{Name: "Gateway", Role: roleControlPlane, Namespace: "envoy-gateway-system", Kind: "deployment", Resource: "envoy-gateway"},
		{Name: "NVCA Operator", Role: roleComputePlane, Namespace: "nvca-operator", Kind: "deployment", Resource: "nvca-operator"},
		// The "NVCA Worker" entry was removed in iteration #2: the current
		// nvcf-self-managed-stack compute-plane is solely the nvca-operator
		// deployment; worker pods are operator-managed lifecycle resources,
		// not a top-level deployment/statefulset to probe. If a future stack
		// version adds an explicit worker deployment, re-add it here.
	}
}

// kubeForRole returns the appropriate kubernetes client for a component role.
// In single-cluster mode all components use the same client; Role tags on
// ComponentHealth events are only populated when ComputePlaneKube is non-nil.
func (c *Collector) kubeForRole(role string) kubernetes.Interface {
	// Prefer ControlPlaneKube/ComputePlaneKube over the legacy Kube field.
	controlKube := c.ControlPlaneKube
	if controlKube == nil {
		controlKube = c.Kube
	}
	switch role {
	case roleComputePlane:
		if c.ComputePlaneKube != nil {
			return c.ComputePlaneKube
		}
		return controlKube
	default:
		return controlKube
	}
}

// roleTag returns the Role string to embed in a ComponentHealth event. When the
// collector is in single-cluster mode (ComputePlaneKube is nil) we emit Role=""
// to preserve byte-identical output for single-cluster deployments.
func (c *Collector) roleTag(specRole string) string {
	if c.ComputePlaneKube == nil {
		return ""
	}
	return specRole
}

// Collect performs one snapshot pass: kicks SIS + kubectl queries in parallel,
// composes events in identity → components → clusters order, and emits to sink.
//
// Verdict is derived from observed component readiness:
//   - degraded if ≥1 component !Healthy
//   - unknown  if SIS unreachable
//   - healthy  otherwise
//   - failed   (NVCFBackend.Health=Failed) deferred to v2; "degraded" is the
//     safe fallback when components reflect upstream failure.
func (c *Collector) Collect(ctx context.Context, sink progress.EventSink) error {
	var (
		components []progress.ComponentHealth
		clusters   []progress.ClusterRow
		sisErr     error
	)

	g, gctx := errgroup.WithContext(ctx)

	// kube probes (sequential within the goroutine to keep result order stable).
	g.Go(func() error {
		var err error
		components, err = c.collectComponents(gctx)
		return err
	})

	// SIS probe (parallel to kube).
	g.Go(func() error {
		var err error
		clusters, err = c.collectClusters(gctx)
		sisErr = err
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	// Verdict precedence: SIS-unreachable wins. Without SIS we can't trust
	// our view of the cluster fleet, so "unknown" is more accurate than
	// reporting degraded based on a partial picture. Once SIS responds,
	// degrade-on-any-not-ready takes over.
	components = appendSISDiagnostic(components, sisErr)

	snap := progress.Snapshot{
		Cluster:         c.Cluster,
		Verdict:         statusVerdict(components, sisErr),
		ReconcileAgeSec: 0, // best-effort; no source today
		Identity:        c.Identity,
	}
	return emitStatusEvents(ctx, sink, snap, components, clusters)
}

func (c *Collector) collectComponents(ctx context.Context) ([]progress.ComponentHealth, error) {
	components := make([]progress.ComponentHealth, 0, len(c.Components))
	nowFn := c.nowFunc()
	for _, sp := range c.Components {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		components = append(components, c.componentHealth(ctx, sp, nowFn()))
	}
	return components, nil
}

func (c *Collector) nowFunc() func() time.Time {
	if c.NowFunc != nil {
		return c.NowFunc
	}
	return func() time.Time { return time.Now().UTC() }
}

func (c *Collector) componentHealth(ctx context.Context, sp ComponentSpec, now time.Time) progress.ComponentHealth {
	kube := c.kubeForRole(sp.Role)
	if kube == nil {
		return progress.ComponentHealth{
			Name:    sp.Name,
			Cluster: sp.Cluster,
			Role:    c.roleTag(sp.Role),
			Healthy: false,
			Message: "no kube client configured for role " + sp.Role,
		}
	}
	ch, err := c.probeComponent(ctx, kube, sp, now)
	if err != nil {
		return progress.ComponentHealth{
			Name:    sp.Name,
			Cluster: sp.Cluster,
			Role:    c.roleTag(sp.Role),
			Healthy: false,
			Message: err.Error(),
		}
	}
	ch.Role = c.roleTag(sp.Role)
	return ch
}

func (c *Collector) collectClusters(ctx context.Context) ([]progress.ClusterRow, error) {
	list, err := c.SIS.ListClusters(ctx, c.SISURL, c.NCAID)
	if err != nil {
		return nil, err
	}
	clusters := make([]progress.ClusterRow, 0, len(list))
	for _, cl := range list {
		clusters = append(clusters, c.clusterRow(cl))
	}
	return clusters, nil
}

func (c *Collector) clusterRow(cl client.SISCluster) progress.ClusterRow {
	row := progress.ClusterRow{
		Name: cl.ClusterName,
		// GPU/GPUCount/ActiveDeployments/LastSeenAgeSec: not available
		// in the SIS list response today. Left at zero; M+9 will add
		// per-cluster detail probes.
		Healthy:   true,
		IsCurrent: cl.ClusterName == c.Cluster,
	}
	// Set Context on the IsCurrent row — we know its context from the
	// operator's --compute-plane-context flag. Other rows get "" until
	// a per-cluster detail probe is available.
	if row.IsCurrent && c.ComputePlaneContext != "" {
		row.Context = c.ComputePlaneContext
	}
	return row
}

func appendSISDiagnostic(components []progress.ComponentHealth, sisErr error) []progress.ComponentHealth {
	if sisErr == nil {
		return components
	}
	return append(components, progress.ComponentHealth{
		Name:    "SIS Cluster List",
		Role:    roleControlPlane,
		Healthy: false,
		Message: sisErr.Error(),
	})
}

func statusVerdict(components []progress.ComponentHealth, sisErr error) string {
	if sisErr != nil {
		return "unknown"
	}
	for _, ch := range components {
		if !ch.Healthy {
			return "degraded"
		}
	}
	return "healthy"
}

func emitStatusEvents(ctx context.Context, sink progress.EventSink, snap progress.Snapshot, components []progress.ComponentHealth, clusters []progress.ClusterRow) error {
	if err := sink.Emit(ctx, snap); err != nil {
		return err
	}
	for _, ch := range components {
		if err := sink.Emit(ctx, ch); err != nil {
			return err
		}
	}
	for _, cl := range clusters {
		if err := sink.Emit(ctx, cl); err != nil {
			return err
		}
	}
	return nil
}

// probeComponent queries Kubernetes for a single component's readiness and
// returns a ComponentHealth event. Returns an error if the API call fails
// (e.g. resource not found), which the caller converts to !Healthy.
func (c *Collector) probeComponent(ctx context.Context, kube kubernetes.Interface, sp ComponentSpec, now time.Time) (progress.ComponentHealth, error) {
	var ready, total int32
	var creationTS time.Time

	switch sp.Kind {
	case "deployment":
		d, err := kube.AppsV1().Deployments(sp.Namespace).Get(ctx, sp.Resource, metav1.GetOptions{})
		if err != nil {
			return progress.ComponentHealth{}, err
		}
		if d.Spec.Replicas != nil {
			total = *d.Spec.Replicas
		}
		ready = d.Status.ReadyReplicas
		creationTS = d.CreationTimestamp.Time

	case "statefulset":
		s, err := kube.AppsV1().StatefulSets(sp.Namespace).Get(ctx, sp.Resource, metav1.GetOptions{})
		if err != nil {
			return progress.ComponentHealth{}, err
		}
		if s.Spec.Replicas != nil {
			total = *s.Spec.Replicas
		}
		ready = s.Status.ReadyReplicas
		creationTS = s.CreationTimestamp.Time

	default:
		return progress.ComponentHealth{}, fmt.Errorf("unknown component kind %q for %s", sp.Kind, sp.Name)
	}

	healthy := ready == total && total > 0
	var uptime int
	if !creationTS.IsZero() {
		uptime = int(now.Sub(creationTS).Seconds())
	}
	return progress.ComponentHealth{
		Name:      sp.Name,
		Cluster:   sp.Cluster,
		Ready:     int(ready),
		Total:     int(total),
		UptimeSec: uptime,
		Healthy:   healthy,
	}, nil
}
