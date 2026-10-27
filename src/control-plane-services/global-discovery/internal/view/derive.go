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

package view

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/convert"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/metrics"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry/merge"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// memoEntry caches a derived import by a fingerprint of its inputs.
type memoEntry struct {
	fp  string
	obj *Object
}

// endpointCache holds the proto form of one endpoint set. The messages are
// shared by every import and pool that includes the set and never modified.
type endpointCache struct {
	gen     uint64
	updated time.Time
	eps     []*gdsv1.Endpoint
	total   int32
	ready   int32
}

// poolIdent identifies a pool by its cluster and name, the join key of
// labelled exports and API records.
type poolIdent struct {
	cluster string
	name    string
}

// startInfo remembers whether a pool has been live, for
// registry.PoolStateInput.Started.
type startInfo struct {
	// epoch and renewedAt are the API lease seen first for an API-only
	// pool; a later renewal in the same epoch is a heartbeat.
	epoch     uint64
	renewedAt time.Time
	started   bool
}

// recompute evaluates leases, derives every object of the clusterset and
// publishes the differences to watchers. cs.mu must be held.
func (v *View) recompute(cs *clusterSet, now time.Time) {
	clusters, pools := cs.leaseStates(now, v.opts.LeaseSkew)
	v.countExpirations(cs, clusters, pools)
	cs.clusterLease, cs.poolLease = clusters, pools

	var next [numKinds]map[string]*Object
	next[KindCluster] = cs.deriveClusters()
	next[KindServiceImport] = cs.deriveImports(now)
	next[KindRouter] = cs.deriveRouters()
	next[KindPool] = cs.derivePools(now, v.opts.TombstoneTTL)
	for k := range cs.epCache {
		if _, ok := cs.endpoints[k]; !ok {
			delete(cs.epCache, k)
		}
	}

	changed := false
	for k := range numKinds {
		if v.diff(cs, k, next[k]) {
			changed = true
		}
	}
	if changed {
		cs.index = buildIndexes(cs.objects)
	}
	v.setGauges(cs)
}

// diff publishes the changes from the current objects of a kind to next and
// installs next. Unchanged objects keep their previous pointer.
func (v *View) diff(cs *clusterSet, kind Kind, next map[string]*Object) bool {
	old := cs.objects[kind]
	keys := slices.Sorted(maps.Keys(old))
	for k := range next {
		if _, ok := old[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	changed := false
	for _, key := range keys {
		o, n := old[key], next[key]
		switch {
		case n == nil:
			v.publish(cs, kind, key, o, nil)
			changed = true
		case o == nil:
			v.publish(cs, kind, key, nil, n)
			changed = true
		case o == n:
		case proto.Equal(o.Full, n.Full):
			next[key] = o
		default:
			v.publish(cs, kind, key, o, n)
			changed = true
		}
	}
	cs.objects[kind] = next
	return changed
}

func (v *View) countExpirations(cs *clusterSet, clusters map[string]registry.LeaseState, pools map[store.PoolKey]registry.LeaseState) {
	for name, s := range clusters {
		if s == registry.LeaseStateExpired && cs.clusterLease[name] == registry.LeaseStateActive {
			v.log.Info("cluster lease expired", zap.String("clusterset", cs.name), zap.String("cluster", name))
			if v.opts.Metrics != nil {
				v.opts.Metrics.LeaseExpirations.WithLabelValues(metrics.LeaseCluster).Inc()
			}
		}
	}
	for key, s := range pools {
		if s == registry.LeaseStateExpired && cs.poolLease[key] == registry.LeaseStateActive {
			v.log.Info("pool lease expired", zap.String("clusterset", cs.name), zap.String("cluster", key.Cluster),
				zap.String("pool_id", key.PoolID))
			if v.opts.Metrics != nil {
				v.opts.Metrics.LeaseExpirations.WithLabelValues(metrics.LeasePool).Inc()
			}
		}
	}
}

func (v *View) setGauges(cs *clusterSet) {
	m := v.opts.Metrics
	if m == nil {
		return
	}
	g := metrics.ClusterSetGauges{Exports: len(cs.exports), PoolsByState: map[string]int{}, Routers: len(cs.objects[KindRouter])}
	for _, s := range cs.clusterLease {
		if s == registry.LeaseStateActive {
			g.ActiveClusters++
		} else {
			g.ExpiredClusters++
		}
	}
	for _, r := range cs.endpoints {
		for _, e := range r.Endpoints {
			if e.Conditions.Ready {
				g.ReadyEndpoints++
			} else {
				g.NotReadyEnds++
			}
		}
	}
	for _, o := range cs.objects[KindServiceImport] {
		g.Imports++
		if o.Full.(*gdsv1.ServiceImport).GetConflict() != nil {
			g.ConflictedImports++
		}
	}
	for _, o := range cs.objects[KindPool] {
		g.PoolsByState[string(convert.PoolStateFromProto(o.Full.(*gdsv1.Pool).GetState()))]++
	}
	m.SetClusterSet(cs.name, g)
}

// endpointsOf returns the proto form of an endpoint set.
func (cs *clusterSet) endpointsOf(key store.ExportKey) (endpointCache, bool) {
	rec, ok := cs.endpoints[key]
	if !ok {
		return endpointCache{}, false
	}
	if c, ok := cs.epCache[key]; ok && c.gen == rec.Generation && c.updated.Equal(rec.UpdatedAt) {
		return c, true
	}
	c := endpointCache{gen: rec.Generation, updated: rec.UpdatedAt}
	for _, e := range rec.Endpoints {
		c.eps = append(c.eps, convert.EndpointToProto(e))
		c.total++
		if e.Conditions.Ready {
			c.ready++
		}
	}
	cs.epCache[key] = c
	return c, true
}

func (cs *clusterSet) leaseActive(cluster string) bool {
	return cs.clusterLease[cluster] == registry.LeaseStateActive
}

func (cs *clusterSet) leaseStateProto(cluster string) gdsv1.LeaseState {
	if _, ok := cs.clusters[cluster]; !ok {
		return gdsv1.LeaseState_LEASE_STATE_UNSPECIFIED
	}
	return convert.LeaseStateToProto(cs.clusterLease[cluster])
}

func (cs *clusterSet) deriveClusters() map[string]*Object {
	out := make(map[string]*Object, len(cs.clusters))
	for name, r := range cs.clusters {
		c := convert.ClusterToProto(r, cs.clusterLease[name])
		out[name] = &Object{Key: name, Full: c, Lite: c}
	}
	return out
}

// ImportKey is the object key of a ServiceImport.
func ImportKey(namespace, name string) string { return namespace + "/" + name }

// RouterKey is the object key of a Router.
func RouterKey(cluster, namespace, name string) string { return cluster + "/" + namespace + "/" + name }

func (cs *clusterSet) deriveImports(now time.Time) map[string]*Object {
	groups := map[string][]store.ExportRecord{}
	for _, r := range cs.exports {
		k := ImportKey(r.Key.Namespace, r.Key.Name)
		groups[k] = append(groups[k], r)
	}
	out := make(map[string]*Object, len(groups))
	for key, recs := range groups {
		slices.SortFunc(recs, func(a, b store.ExportRecord) int { return cmp.Compare(a.Key.Cluster, b.Key.Cluster) })
		fp := cs.importFingerprint(recs)
		if m, ok := cs.importMemo[key]; ok && m.fp == fp {
			out[key] = m.obj
			continue
		}
		obj := cs.buildImport(key, recs, cs.objects[KindServiceImport][key], now)
		cs.importMemo[key] = memoEntry{fp: fp, obj: obj}
		out[key] = obj
	}
	for key := range cs.importMemo {
		if _, ok := groups[key]; !ok {
			delete(cs.importMemo, key)
		}
	}
	return out
}

// importFingerprint covers every input of a merged import: the export and
// endpoint set versions and the lease state, generation and region of each
// exporting cluster.
func (cs *clusterSet) importFingerprint(recs []store.ExportRecord) string {
	var b strings.Builder
	for _, r := range recs {
		cl := cs.clusters[r.Key.Cluster]
		ep := cs.endpoints[r.Key]
		fmt.Fprintf(&b, "%s|%d|%d|%d|%d|%s|%d|%s;", r.Key.Cluster, r.Generation, r.UpdatedAt.UnixNano(),
			ep.Generation, ep.UpdatedAt.UnixNano(), cs.leaseStateProto(r.Key.Cluster), cl.Generation, cl.Region)
	}
	return b.String()
}

func conflictToProto(c *merge.Conflict) *gdsv1.Conflict {
	if c == nil {
		return nil
	}
	return &gdsv1.Conflict{Reason: c.Reason, Message: c.Message, WinningCluster: c.WinningCluster}
}

var conditionStatuses = map[merge.ConditionStatus]gdsv1.ConditionStatus{
	merge.ConditionTrue:    gdsv1.ConditionStatus_CONDITION_STATUS_TRUE,
	merge.ConditionFalse:   gdsv1.ConditionStatus_CONDITION_STATUS_FALSE,
	merge.ConditionUnknown: gdsv1.ConditionStatus_CONDITION_STATUS_UNKNOWN,
}

// conditionsToProto converts conditions, keeping the transition time of a
// previous condition with the same type, status and reason.
func conditionsToProto(conds []merge.Condition, prev []*gdsv1.Condition, now time.Time) []*gdsv1.Condition {
	out := make([]*gdsv1.Condition, 0, len(conds))
	for _, c := range conds {
		status := conditionStatuses[c.Status]
		at := convert.Timestamp(now)
		for _, p := range prev {
			if p.GetType() == c.Type && p.GetStatus() == status && p.GetReason() == c.Reason {
				at = p.GetLastTransitionAt()
			}
		}
		out = append(out, &gdsv1.Condition{Type: c.Type, Status: status, Reason: c.Reason, Message: c.Message, LastTransitionAt: at})
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (cs *clusterSet) buildImport(key string, recs []store.ExportRecord, prev *Object, now time.Time) *Object {
	byCluster := make(map[string]store.ExportRecord, len(recs))
	inputs := make([]merge.Export, 0, len(recs))
	for _, r := range recs {
		byCluster[r.Key.Cluster] = r
		inputs = append(inputs, merge.Export{Cluster: r.Key.Cluster, LeaseActive: cs.leaseActive(r.Key.Cluster), Spec: r.Spec})
	}
	res := merge.Merge(inputs)
	si := &gdsv1.ServiceImport{
		Clusterset:            cs.name,
		Namespace:             recs[0].Key.Namespace,
		Name:                  recs[0].Key.Name,
		Type:                  convert.ServiceTypeToProto(res.Spec.Type),
		Ports:                 convert.ServicePortsToProto(res.Spec.Ports),
		IpFamilies:            convert.IPFamiliesToProto(res.Spec.IPFamilies),
		SessionAffinity:       convert.SessionAffinityToProto(res.Spec.SessionAffinity),
		SessionAffinityConfig: convert.SessionAffinityConfigToProto(res.Spec.SessionAffinityConfig),
		Annotations:           maps.Clone(res.Spec.Annotations),
		Labels:                maps.Clone(res.Spec.Labels),
		Conflict:              conflictToProto(res.Conflict),
	}
	prevConds := map[string][]*gdsv1.Condition{}
	if prev != nil {
		for _, c := range prev.Full.(*gdsv1.ServiceImport).GetClusters() {
			prevConds[c.GetCluster()] = c.GetConditions()
		}
	}
	var updated time.Time
	for _, cr := range res.Clusters {
		rec := byCluster[cr.Cluster]
		region := cs.clusters[cr.Cluster].Region
		ep, _ := cs.endpointsOf(rec.Key)
		si.Clusters = append(si.Clusters, &gdsv1.ClusterStatus{
			Cluster:            cr.Cluster,
			Region:             region,
			LeaseState:         cs.leaseStateProto(cr.Cluster),
			Participating:      cr.Participating,
			Conflict:           conflictToProto(cr.Conflict),
			EndpointCount:      ep.total,
			ReadyEndpointCount: ep.ready,
			ExportGeneration:   rec.Generation,
			Conditions:         conditionsToProto(cr.Conditions, prevConds[cr.Cluster], now),
		})
		if cr.Participating {
			si.Endpoints = append(si.Endpoints, &gdsv1.ClusterEndpoints{
				Cluster:            cr.Cluster,
				Region:             region,
				Endpoints:          ep.eps,
				Generation:         ep.gen,
				EndpointCount:      ep.total,
				ReadyEndpointCount: ep.ready,
			})
		}
		updated = maxTime(updated, maxTime(rec.UpdatedAt, cs.endpoints[rec.Key].UpdatedAt))
	}
	si.UpdatedAt = convert.Timestamp(updated)
	return &Object{Key: key, Full: si, Lite: liteImport(si)}
}

// liteClusterEndpoints copies endpoint groups without their endpoint lists.
func liteClusterEndpoints(in []*gdsv1.ClusterEndpoints) []*gdsv1.ClusterEndpoints {
	if in == nil {
		return nil
	}
	out := make([]*gdsv1.ClusterEndpoints, 0, len(in))
	for _, ce := range in {
		out = append(out, &gdsv1.ClusterEndpoints{
			Cluster:            ce.GetCluster(),
			Region:             ce.GetRegion(),
			Generation:         ce.GetGeneration(),
			EndpointCount:      ce.GetEndpointCount(),
			ReadyEndpointCount: ce.GetReadyEndpointCount(),
			Namespace:          ce.GetNamespace(),
			Name:               ce.GetName(),
		})
	}
	return out
}

// liteImport returns a ServiceImport sharing every field of si except the
// endpoint lists.
func liteImport(si *gdsv1.ServiceImport) *gdsv1.ServiceImport {
	return &gdsv1.ServiceImport{
		Clusterset:            si.Clusterset,
		Namespace:             si.Namespace,
		Name:                  si.Name,
		Type:                  si.Type,
		Ports:                 si.Ports,
		IpFamilies:            si.IpFamilies,
		SessionAffinity:       si.SessionAffinity,
		SessionAffinityConfig: si.SessionAffinityConfig,
		Annotations:           si.Annotations,
		Labels:                si.Labels,
		Conflict:              si.Conflict,
		Clusters:              si.Clusters,
		Endpoints:             liteClusterEndpoints(si.Endpoints),
		UpdatedAt:             si.UpdatedAt,
	}
}

// litePool returns a Pool sharing every field of p except the endpoint
// lists.
func litePool(p *gdsv1.Pool) *gdsv1.Pool {
	return &gdsv1.Pool{
		Clusterset:         p.Clusterset,
		PoolId:             p.PoolId,
		Name:               p.Name,
		Location:           p.Location,
		HardwareClass:      p.HardwareClass,
		Models:             p.Models,
		WorkerCount:        p.WorkerCount,
		ReadyWorkerCount:   p.ReadyWorkerCount,
		Frontends:          p.Frontends,
		Services:           p.Services,
		Labels:             p.Labels,
		DesiredState:       p.DesiredState,
		State:              p.State,
		Lease:              p.Lease,
		Generation:         p.Generation,
		OwnerId:            p.OwnerId,
		RegisteredAt:       p.RegisteredAt,
		UpdatedAt:          p.UpdatedAt,
		Endpoints:          liteClusterEndpoints(p.Endpoints),
		ReadyEndpointCount: p.ReadyEndpointCount,
	}
}

func portByName(ports []store.ServicePort, name string) int32 {
	for _, p := range ports {
		if p.Name == name {
			return p.Port
		}
	}
	return 0
}

// DialScheme returns the scheme of a router from its annotations: the
// gds.nvidia.com/dial-scheme value when it is http or https, else http.
func DialScheme(annotations map[string]string) string {
	switch s := strings.ToLower(strings.TrimSpace(annotations[api.AnnotationDialScheme])); s {
	case api.DialSchemeHTTP, api.DialSchemeHTTPS:
		return s
	}
	return api.DialSchemeHTTP
}

func (cs *clusterSet) deriveRouters() map[string]*Object {
	out := map[string]*Object{}
	for _, r := range cs.exports {
		if r.Spec.Labels[api.LabelRole] != api.RoleRouter {
			continue
		}
		key := RouterKey(r.Key.Cluster, r.Key.Namespace, r.Key.Name)
		grpcName, httpName := api.PortNames(r.Spec.Annotations)
		router := &gdsv1.Router{
			Cluster:           r.Key.Cluster,
			Region:            cs.clusters[r.Key.Cluster].Region,
			Namespace:         r.Key.Namespace,
			Name:              r.Key.Name,
			Type:              convert.ServiceTypeToProto(r.Spec.Type),
			ClusterLeaseState: cs.leaseStateProto(r.Key.Cluster),
			GrpcPort:          portByName(r.Spec.Ports, grpcName),
			HttpPort:          portByName(r.Spec.Ports, httpName),
			DialScheme:        DialScheme(r.Spec.Annotations),
			Labels:            maps.Clone(r.Spec.Labels),
			Annotations:       maps.Clone(r.Spec.Annotations),
		}
		if cs.leaseActive(r.Key.Cluster) {
			if set, ok := cs.endpoints[r.Key]; ok {
				for _, e := range set.Endpoints {
					replica := &gdsv1.RouterReplica{
						AddressType: convert.AddressTypeToProto(e.AddressType),
						Ready:       e.Conditions.Ready,
						Hostname:    e.Hostname,
						Zone:        e.Zone,
					}
					if len(e.Addresses) > 0 {
						replica.Address = e.Addresses[0]
					}
					router.Replicas = append(router.Replicas, replica)
				}
			}
		}
		out[key] = &Object{Key: key, Full: router, Lite: router}
	}
	return out
}

// poolInput gathers what one pool is derived from.
type poolInput struct {
	ident   poolIdent
	overlay *store.PoolRecord
	exports []store.ExportRecord
}

func poolRole(labels map[string]string) (store.FrontendRole, bool) {
	switch labels[api.LabelRole] {
	case api.RolePoolFrontend:
		return store.FrontendRoleOpenAI, true
	case api.RoleKVRelay:
		return store.FrontendRoleKVRelay, true
	}
	return "", false
}

func (cs *clusterSet) derivePools(now time.Time, tombstoneTTL time.Duration) map[string]*Object {
	inputs := map[poolIdent]*poolInput{}
	input := func(id poolIdent) *poolInput {
		in, ok := inputs[id]
		if !ok {
			in = &poolInput{ident: id}
			inputs[id] = in
		}
		return in
	}
	for _, r := range cs.exports {
		if _, ok := poolRole(r.Spec.Labels); !ok {
			continue
		}
		name := r.Spec.Labels[api.LabelPool]
		if !registry.IsDNSLabel(name) {
			continue
		}
		in := input(poolIdent{cluster: r.Key.Cluster, name: name})
		in.exports = append(in.exports, r)
	}
	poolKeys := slices.SortedFunc(maps.Keys(cs.pools), store.ComparePoolKeys)
	for _, k := range poolKeys {
		r := cs.pools[k]
		in := input(poolIdent{cluster: k.Cluster, name: r.Name})
		if in.overlay == nil {
			in.overlay = &r
		}
	}

	out := make(map[string]*Object, len(inputs))
	seen := map[poolIdent]bool{}
	idents := slices.SortedFunc(maps.Keys(inputs), func(a, b poolIdent) int {
		return cmp.Or(cmp.Compare(a.cluster, b.cluster), cmp.Compare(a.name, b.name))
	})
	for _, id := range idents {
		obj := cs.buildPool(inputs[id])
		if _, dup := out[obj.Key]; dup {
			continue
		}
		seen[id] = true
		out[obj.Key] = obj
	}
	for id := range cs.started {
		if !seen[id] {
			delete(cs.started, id)
		}
	}

	for id, o := range cs.objects[KindPool] {
		if _, ok := out[id]; ok {
			delete(cs.tombstones, id)
			continue
		}
		p := o.Full.(*gdsv1.Pool)
		if p.GetState() == gdsv1.PoolState_POOL_STATE_GONE {
			if until, ok := cs.tombstones[id]; ok && now.Before(until) {
				out[id] = o
			} else {
				delete(cs.tombstones, id)
			}
			continue
		}
		gone := proto.Clone(p).(*gdsv1.Pool)
		gone.State = gdsv1.PoolState_POOL_STATE_GONE
		gone.Endpoints = nil
		gone.ReadyEndpointCount = 0
		cs.tombstones[id] = now.Add(tombstoneTTL)
		out[id] = &Object{Key: id, Full: gone, Lite: gone}
	}
	return out
}

func sameService(a, b store.ServiceRef) bool { return a.Namespace == b.Namespace && a.Name == b.Name }

func frontendKey(f store.FrontendEndpoint) string {
	if f.Service != nil {
		return string(f.Role) + "|svc|" + f.Service.Namespace + "/" + f.Service.Name
	}
	return string(f.Role) + "|url|" + f.URL
}

// labelSpec derives pool metadata from the pool's labelled exports. The
// OPENAI frontend export comes first, so its labels and annotations win.
func (cs *clusterSet) labelSpec(in *poolInput) store.PoolSpec {
	cl := cs.clusters[in.ident.cluster]
	spec := store.PoolSpec{
		Location: store.Location{Region: cl.Region, Cluster: in.ident.cluster, Zones: slices.Clone(cl.Zones)},
	}
	exports := slices.Clone(in.exports)
	slices.SortFunc(exports, func(a, b store.ExportRecord) int {
		ra, _ := poolRole(a.Spec.Labels)
		rb, _ := poolRole(b.Spec.Labels)
		return cmp.Or(cmp.Compare(rb, ra), store.CompareExportKeys(a.Key, b.Key))
	})
	labels := map[string]string{}
	for _, e := range exports {
		for k, val := range e.Spec.Labels {
			if _, ok := labels[k]; !ok && k != api.LabelRole {
				labels[k] = val
			}
		}
		ann := e.Spec.Annotations
		if spec.HardwareClass == "" {
			spec.HardwareClass = ann[api.AnnotationHardwareClass]
		}
		if spec.Location.DC == "" {
			spec.Location.DC = ann[api.AnnotationDC]
		}
		if len(spec.Models) == 0 {
			for _, m := range api.ParsePoolModels(ann[api.AnnotationPoolModels]) {
				spec.Models = append(spec.Models, store.ModelServed{ModelID: m})
			}
		}
		role, _ := poolRole(e.Spec.Labels)
		ref := store.ServiceRef{Namespace: e.Key.Namespace, Name: e.Key.Name, Role: role}
		spec.Services = append(spec.Services, ref)
		spec.Frontends = append(spec.Frontends, store.FrontendEndpoint{Role: role, Service: &ref, PortName: portNameFor(role, ann)})
	}
	if len(labels) > 0 {
		spec.Labels = labels
	}
	return spec
}

func portNameFor(role store.FrontendRole, annotations map[string]string) string {
	grpcName, httpName := api.PortNames(annotations)
	if role == store.FrontendRoleKVRelay {
		return grpcName
	}
	return httpName
}

// overlaySpec applies an API registration over label-derived metadata:
// non-empty API values win, frontends and services are unioned, labels are
// merged with the API's winning per key, and worker counts and the desired
// state come from the API.
func overlaySpec(base, o store.PoolSpec) store.PoolSpec {
	out := base.Clone()
	if o.Location.Region != "" {
		out.Location.Region = o.Location.Region
	}
	if o.Location.DC != "" {
		out.Location.DC = o.Location.DC
	}
	if len(o.Location.Zones) > 0 {
		out.Location.Zones = slices.Clone(o.Location.Zones)
	}
	if o.HardwareClass != "" {
		out.HardwareClass = o.HardwareClass
	}
	if len(o.Models) > 0 {
		out.Models = o.Clone().Models
	}
	out.WorkerCount, out.ReadyWorkerCount, out.DesiredState = o.WorkerCount, o.ReadyWorkerCount, o.DesiredState

	oc := o.Clone()
	frontends := oc.Frontends
	keys := map[string]bool{}
	for _, f := range frontends {
		keys[frontendKey(f)] = true
	}
	for _, f := range out.Frontends {
		if !keys[frontendKey(f)] {
			frontends = append(frontends, f)
		}
	}
	out.Frontends = frontends

	services := oc.Services
	for _, s := range out.Services {
		if !slices.ContainsFunc(services, func(x store.ServiceRef) bool { return sameService(x, s) }) {
			services = append(services, s)
		}
	}
	out.Services = services

	if len(o.Labels) > 0 {
		if out.Labels == nil {
			out.Labels = map[string]string{}
		}
		maps.Copy(out.Labels, o.Labels)
	}
	return out
}

// addServiceFrontends gives every Service of the pool a frontend entry, so
// that an API pool listing an OPENAI Service in services[] has its dispatch
// entry.
func (cs *clusterSet) addServiceFrontends(spec *store.PoolSpec) {
	for _, s := range spec.Services {
		covered := slices.ContainsFunc(spec.Frontends, func(f store.FrontendEndpoint) bool {
			return f.Service != nil && sameService(*f.Service, s)
		})
		if covered {
			continue
		}
		ref := s
		var ann map[string]string
		if e, ok := cs.exports[store.ExportKey{ClusterSet: cs.name, Cluster: spec.Location.Cluster, Namespace: s.Namespace, Name: s.Name}]; ok {
			ann = e.Spec.Annotations
		}
		spec.Frontends = append(spec.Frontends, store.FrontendEndpoint{Role: s.Role, Service: &ref, PortName: portNameFor(s.Role, ann)})
	}
}

func (cs *clusterSet) buildPool(in *poolInput) *Object {
	id := in.ident
	cl, registered := cs.clusters[id.cluster]
	clusterActive := registered && cs.leaseActive(id.cluster)

	spec := cs.labelSpec(in)
	if in.overlay != nil {
		spec = overlaySpec(spec, in.overlay.Spec)
	}
	spec.Location.Cluster = id.cluster
	cs.addServiceFrontends(&spec)

	var (
		endpoints             []*gdsv1.ClusterEndpoints
		readyAll, readyOpenAI int32
		updated               time.Time
		registeredAt          time.Time
	)
	for _, ref := range spec.Services {
		key := store.ExportKey{ClusterSet: cs.name, Cluster: id.cluster, Namespace: ref.Namespace, Name: ref.Name}
		ce := &gdsv1.ClusterEndpoints{Cluster: id.cluster, Region: cl.Region, Namespace: ref.Namespace, Name: ref.Name}
		if exp, ok := cs.exports[key]; ok {
			updated = maxTime(updated, exp.UpdatedAt)
			if ep, ok := cs.endpointsOf(key); ok {
				ce.Generation = ep.gen
				updated = maxTime(updated, cs.endpoints[key].UpdatedAt)
				if clusterActive {
					ce.Endpoints, ce.EndpointCount, ce.ReadyEndpointCount = ep.eps, ep.total, ep.ready
				}
			}
		}
		readyAll += ce.ReadyEndpointCount
		if ref.Role == store.FrontendRoleOpenAI {
			readyOpenAI += ce.ReadyEndpointCount
		}
		endpoints = append(endpoints, ce)
	}
	for _, e := range in.exports {
		if registeredAt.IsZero() || e.Spec.ExportCreationTimestamp.Before(registeredAt) {
			registeredAt = e.Spec.ExportCreationTimestamp
		}
	}

	poolID := registry.PoolID(cs.name, id.cluster, id.name)
	var poolLeaseActive, overlayServices bool
	if o := in.overlay; o != nil {
		poolID = o.Key.PoolID
		poolLeaseActive = o.Lease.Epoch != 0 && cs.poolLease[o.Key] == registry.LeaseStateActive
		overlayServices = len(o.Spec.Services) > 0
	}

	state := registry.PoolStateInput{
		Desired:            registry.PoolStateActive,
		LabelledExport:     len(in.exports) > 0,
		Overlay:            in.overlay != nil,
		OverlayServices:    overlayServices,
		ClusterLeaseActive: clusterActive,
		PoolLeaseActive:    poolLeaseActive,
		ReadyEndpoints:     int(readyAll),
	}
	if spec.DesiredState == store.DesiredStateDraining {
		state.Desired = registry.PoolStateDraining
	}
	info := cs.started[id]
	if state.APIOnly() {
		l := in.overlay.Lease
		if l.Epoch != info.epoch {
			// A renewal after the lease was minted is a heartbeat: the
			// minted epoch is the mint time in unix milliseconds unless the
			// clock went backwards.
			info = startInfo{epoch: l.Epoch, renewedAt: l.RenewedAt, started: l.Epoch != 0 && l.RenewedAt.UnixMilli() > int64(l.Epoch)}
		} else if !l.RenewedAt.Equal(info.renewedAt) {
			info.started = true
		}
	} else if clusterActive && readyAll > 0 {
		info.started = true
	}
	cs.started[id] = info
	state.Started = info.started

	p := &gdsv1.Pool{Clusterset: cs.name, PoolId: poolID, Name: id.name}
	convert.PoolSpecToProto(spec, p)
	p.State = convert.PoolStateToProto(registry.DerivePoolState(state))
	p.Endpoints = endpoints
	p.ReadyEndpointCount = readyOpenAI
	if o := in.overlay; o != nil {
		p.Lease = convert.LeaseToProto(o.Lease, cs.poolLease[o.Key])
		p.Generation = o.Generation
		p.OwnerId = o.OwnerID
		registeredAt = o.RegisteredAt
		updated = maxTime(updated, o.UpdatedAt)
	}
	p.RegisteredAt = convert.Timestamp(registeredAt)
	p.UpdatedAt = convert.Timestamp(updated)
	return &Object{Key: poolID, Full: p, Lite: litePool(p)}
}
