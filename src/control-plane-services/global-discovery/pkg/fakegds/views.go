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

package fakegds

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

// computeLocked derives every view of cs at the server clock. The returned
// objects are fresh and never mutated afterwards.
func (s *Server) computeLocked(cs *clusterSet) [numKinds]map[string]proto.Message {
	now := s.now()
	var v [numKinds]map[string]proto.Message
	for k := range v {
		v[k] = map[string]proto.Message{}
	}
	active := map[string]bool{}
	region := map[string]string{}
	for name, rec := range cs.clusters {
		c := clusterView(rec, now)
		v[kindClusters][name] = c
		active[name] = c.GetLease().GetState() == gdsv1.LeaseState_LEASE_STATE_ACTIVE
		region[name] = c.GetRegion()
	}
	for key, imp := range importsView(cs, active, region) {
		v[kindImports][key] = imp
	}
	for key, r := range routersView(cs, active, region) {
		v[kindRouters][key] = r
	}
	for key, p := range poolsView(cs, active, region, now) {
		v[kindPools][key] = p
	}
	return v
}

func leaseState(renewedAt time.Time, ttl time.Duration, now time.Time) gdsv1.LeaseState {
	if registry.EvaluateLease(renewedAt, ttl, now, registry.DefaultLeaseSkew) == registry.LeaseStateActive {
		return gdsv1.LeaseState_LEASE_STATE_ACTIVE
	}
	return gdsv1.LeaseState_LEASE_STATE_EXPIRED
}

// leaseView fills the read-time fields of a stored lease.
func leaseView(l *gdsv1.Lease, ttl time.Duration, now time.Time) *gdsv1.Lease {
	if l == nil {
		return nil
	}
	l = proto.CloneOf(l)
	renewed := l.GetRenewedAt().AsTime()
	l.ExpiresAt = timestamppb.New(registry.LeaseExpiresAt(renewed, ttl))
	l.State = leaseState(renewed, ttl, now)
	return l
}

func clusterView(rec *clusterRec, now time.Time) *gdsv1.Cluster {
	c := proto.CloneOf(rec.c)
	c.Lease = leaseView(rec.c.GetLease(), rec.ttl, now)
	return c
}

func countEndpoints(eps []*gdsv1.Endpoint) (total, ready int32) {
	for _, ep := range eps {
		total++
		if ep.GetConditions().GetReady() {
			ready++
		}
	}
	return total, ready
}

func clusterEndpoints(cluster, region string, rec *endpointRec) *gdsv1.ClusterEndpoints {
	ce := &gdsv1.ClusterEndpoints{Cluster: cluster, Region: region}
	if rec != nil {
		ce.Generation = rec.generation
		ce.EndpointCount, ce.ReadyEndpointCount = countEndpoints(rec.endpoints)
		for _, ep := range rec.endpoints {
			ce.Endpoints = append(ce.Endpoints, proto.CloneOf(ep))
		}
	}
	return ce
}

// exportOrder sorts exports oldest first: export creation time, then
// cluster name.
func exportOrder(a, b *gdsv1.ServiceExport) int {
	ta, tb := a.GetSpec().GetExportCreationTimestamp().AsTime(), b.GetSpec().GetExportCreationTimestamp().AsTime()
	if c := ta.Compare(tb); c != 0 {
		return c
	}
	return cmp.Compare(a.GetCluster(), b.GetCluster())
}

func importsView(cs *clusterSet, active map[string]bool, region map[string]string) map[string]*gdsv1.ServiceImport {
	groups := map[string][]*gdsv1.ServiceExport{}
	for key, e := range cs.exports {
		groups[key.namespace+"/"+key.name] = append(groups[key.namespace+"/"+key.name], e)
	}
	out := map[string]*gdsv1.ServiceImport{}
	for key, exports := range groups {
		slices.SortFunc(exports, exportOrder)
		var participants []*gdsv1.ServiceExport
		for _, e := range exports {
			if active[e.GetCluster()] {
				participants = append(participants, e)
			}
		}
		winner := exports[0]
		if len(participants) > 0 {
			winner = participants[0]
		}
		spec := winner.GetSpec()
		imp := &gdsv1.ServiceImport{
			Clusterset:            cs.name,
			Namespace:             winner.GetNamespace(),
			Name:                  winner.GetName(),
			Type:                  spec.GetType(),
			IpFamilies:            slices.Clone(spec.GetIpFamilies()),
			SessionAffinity:       spec.GetSessionAffinity(),
			SessionAffinityConfig: proto.CloneOf(spec.GetSessionAffinityConfig()),
			Annotations:           maps.Clone(spec.GetAnnotations()),
			Labels:                maps.Clone(spec.GetLabels()),
		}
		portSources := participants
		if len(portSources) == 0 {
			portSources = []*gdsv1.ServiceExport{winner}
		}
		type portKey struct {
			port     int32
			protocol gdsv1.Protocol
		}
		seen := map[portKey]bool{}
		for _, e := range portSources {
			for _, p := range e.GetSpec().GetPorts() {
				k := portKey{p.GetPort(), p.GetProtocol()}
				if !seen[k] {
					seen[k] = true
					imp.Ports = append(imp.Ports, proto.CloneOf(p))
				}
			}
		}
		var updated time.Time
		byCluster := slices.Clone(exports)
		slices.SortFunc(byCluster, func(a, b *gdsv1.ServiceExport) int { return cmp.Compare(a.GetCluster(), b.GetCluster()) })
		for _, e := range byCluster {
			ek := exportKey{e.GetCluster(), e.GetNamespace(), e.GetName()}
			epRec := cs.endpoints[ek]
			participating := active[e.GetCluster()]
			st := &gdsv1.ClusterStatus{
				Cluster:          e.GetCluster(),
				Region:           region[e.GetCluster()],
				LeaseState:       gdsv1.LeaseState_LEASE_STATE_EXPIRED,
				Participating:    participating,
				ExportGeneration: e.GetGeneration(),
				Conditions: []*gdsv1.Condition{{
					Type:   api.ConditionConflict,
					Status: gdsv1.ConditionStatus_CONDITION_STATUS_FALSE,
					Reason: api.ConditionReasonNoConflicts,
				}},
			}
			ready := &gdsv1.Condition{Type: api.ConditionReady, Status: gdsv1.ConditionStatus_CONDITION_STATUS_TRUE, Reason: "Exported"}
			if participating {
				st.LeaseState = gdsv1.LeaseState_LEASE_STATE_ACTIVE
				if epRec != nil {
					st.EndpointCount, st.ReadyEndpointCount = countEndpoints(epRec.endpoints)
					imp.Endpoints = append(imp.Endpoints, clusterEndpoints(e.GetCluster(), region[e.GetCluster()], epRec))
				}
			} else {
				ready.Status = gdsv1.ConditionStatus_CONDITION_STATUS_FALSE
				ready.Reason = api.ConditionReasonClusterLeaseExpired
			}
			st.Conditions = append([]*gdsv1.Condition{ready}, st.Conditions...)
			imp.Clusters = append(imp.Clusters, st)
			if t := e.GetUpdatedAt().AsTime(); t.After(updated) {
				updated = t
			}
		}
		imp.UpdatedAt = timestamppb.New(updated)
		out[key] = imp
	}
	return out
}

// portByName returns the port number of the named Service port, or 0.
func portByName(ports []*gdsv1.ServicePort, name string) int32 {
	for _, p := range ports {
		if p.GetName() == name {
			return p.GetPort()
		}
	}
	return 0
}

func routersView(cs *clusterSet, active map[string]bool, region map[string]string) map[string]*gdsv1.Router {
	out := map[string]*gdsv1.Router{}
	for key, e := range cs.exports {
		spec := e.GetSpec()
		if spec.GetLabels()[api.LabelRole] != api.RoleRouter {
			continue
		}
		grpcName, httpName := api.PortNames(spec.GetAnnotations())
		lease := gdsv1.LeaseState_LEASE_STATE_EXPIRED
		if active[key.cluster] {
			lease = gdsv1.LeaseState_LEASE_STATE_ACTIVE
		}
		r := &gdsv1.Router{
			Cluster:           key.cluster,
			Region:            region[key.cluster],
			Namespace:         key.namespace,
			Name:              key.name,
			Type:              spec.GetType(),
			ClusterLeaseState: lease,
			GrpcPort:          portByName(spec.GetPorts(), grpcName),
			HttpPort:          portByName(spec.GetPorts(), httpName),
			DialScheme:        spec.GetAnnotations()[api.AnnotationDialScheme],
			Labels:            maps.Clone(spec.GetLabels()),
			Annotations:       maps.Clone(spec.GetAnnotations()),
		}
		if rec := cs.endpoints[key]; rec != nil {
			for _, ep := range rec.endpoints {
				for _, addr := range ep.GetAddresses() {
					r.Replicas = append(r.Replicas, &gdsv1.RouterReplica{
						Address:     addr,
						AddressType: ep.GetAddressType(),
						Ready:       ep.GetConditions().GetReady(),
						Hostname:    ep.GetHostname(),
						Zone:        ep.GetZone(),
					})
				}
			}
		}
		out[key.cluster+"/"+key.namespace+"/"+key.name] = r
	}
	return out
}

// LabelPoolID returns the pool id the fake assigns to a pool that exists
// only through labelled exports: a stable hash of clusterset, cluster and
// pool name.
func LabelPoolID(clusterset, cluster, name string) string {
	sum := sha256.Sum256([]byte(clusterset + "/" + cluster + "/" + name))
	return "label-" + hex.EncodeToString(sum[:8])
}

var registryPoolStates = map[registry.PoolState]gdsv1.PoolState{
	registry.PoolStateRegistered: gdsv1.PoolState_POOL_STATE_REGISTERED,
	registry.PoolStateActive:     gdsv1.PoolState_POOL_STATE_ACTIVE,
	registry.PoolStateDraining:   gdsv1.PoolState_POOL_STATE_DRAINING,
	registry.PoolStateUnhealthy:  gdsv1.PoolState_POOL_STATE_UNHEALTHY,
	registry.PoolStateGone:       gdsv1.PoolState_POOL_STATE_GONE,
}

func roleOf(role string) gdsv1.FrontendRole {
	switch role {
	case api.RolePoolFrontend:
		return gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI
	case api.RoleKVRelay:
		return gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY
	}
	return gdsv1.FrontendRole_FRONTEND_ROLE_UNSPECIFIED
}

type poolAgg struct {
	cluster, name string
	rec           *poolRec
	exports       []*gdsv1.ServiceExport
}

func poolsView(cs *clusterSet, active map[string]bool, region map[string]string, now time.Time) map[string]*gdsv1.Pool {
	type aggKey struct{ cluster, name string }
	aggs := map[aggKey]*poolAgg{}
	get := func(cluster, name string) *poolAgg {
		k := aggKey{cluster, name}
		if aggs[k] == nil {
			aggs[k] = &poolAgg{cluster: cluster, name: name}
		}
		return aggs[k]
	}
	for _, rec := range cs.pools {
		get(rec.p.GetLocation().GetCluster(), rec.p.GetName()).rec = rec
	}
	for _, e := range cs.exports {
		labels := e.GetSpec().GetLabels()
		role, ok := registry.ParseRole(labels[api.LabelRole])
		if !ok || !role.IsPoolRole() || labels[api.LabelPool] == "" {
			continue
		}
		a := get(e.GetCluster(), labels[api.LabelPool])
		a.exports = append(a.exports, e)
	}
	out := map[string]*gdsv1.Pool{}
	for _, a := range aggs {
		p := poolView(cs, a, active, region, now)
		out[p.GetPoolId()] = p
	}
	return out
}

func poolView(cs *clusterSet, a *poolAgg, active map[string]bool, region map[string]string, now time.Time) *gdsv1.Pool {
	var p *gdsv1.Pool
	if a.rec != nil {
		p = proto.CloneOf(a.rec.p)
		p.Lease = leaseView(a.rec.p.GetLease(), a.rec.ttl, now)
	} else {
		p = &gdsv1.Pool{PoolId: LabelPoolID(cs.name, a.cluster, a.name), Name: a.name}
	}
	p.Clusterset = cs.name
	if p.Location == nil {
		p.Location = &gdsv1.Location{}
	}
	p.Location.Cluster = a.cluster
	if p.Location.Region == "" {
		p.Location.Region = region[a.cluster]
	}
	// Frontends first, then relays, each by namespace and name.
	slices.SortFunc(a.exports, func(x, y *gdsv1.ServiceExport) int {
		rx, ry := x.GetSpec().GetLabels()[api.LabelRole], y.GetSpec().GetLabels()[api.LabelRole]
		if rx != ry {
			if rx == api.RolePoolFrontend {
				return -1
			}
			if ry == api.RolePoolFrontend {
				return 1
			}
		}
		return cmp.Or(cmp.Compare(x.GetNamespace(), y.GetNamespace()), cmp.Compare(x.GetName(), y.GetName()))
	})
	var updated time.Time
	if a.rec != nil {
		updated = a.rec.p.GetUpdatedAt().AsTime()
	}
	hasOpenAI := false
	for _, f := range p.GetFrontends() {
		if f.GetRole() == gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI {
			hasOpenAI = true
		}
	}
	for _, e := range a.exports {
		spec := e.GetSpec()
		ann := spec.GetAnnotations()
		role := spec.GetLabels()[api.LabelRole]
		if p.HardwareClass == "" {
			p.HardwareClass = ann[api.AnnotationHardwareClass]
		}
		if p.Location.Dc == "" {
			p.Location.Dc = ann[api.AnnotationDC]
		}
		if len(p.Models) == 0 {
			for _, m := range api.ParsePoolModels(ann[api.AnnotationPoolModels]) {
				p.Models = append(p.Models, &gdsv1.ModelServed{ModelId: m})
			}
		}
		if len(p.Labels) == 0 && role == api.RolePoolFrontend {
			p.Labels = maps.Clone(spec.GetLabels())
		}
		if !slices.ContainsFunc(p.Services, func(r *gdsv1.ServiceRef) bool {
			return r.GetNamespace() == e.GetNamespace() && r.GetName() == e.GetName()
		}) {
			p.Services = append(p.Services, &gdsv1.ServiceRef{Namespace: e.GetNamespace(), Name: e.GetName(), Role: roleOf(role)})
		}
		if role == api.RolePoolFrontend && !hasOpenAI {
			hasOpenAI = true
			_, httpName := api.PortNames(ann)
			p.Frontends = append(p.Frontends, &gdsv1.FrontendEndpoint{
				Role:     gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI,
				Service:  &gdsv1.ServiceRef{Namespace: e.GetNamespace(), Name: e.GetName(), Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI},
				PortName: httpName,
			})
		}
		if t := e.GetUpdatedAt().AsTime(); t.After(updated) {
			updated = t
		}
	}
	var readyTotal, readyFrontend int32
	if active[a.cluster] {
		for _, ref := range p.Services {
			ek := exportKey{a.cluster, ref.GetNamespace(), ref.GetName()}
			if _, ok := cs.exports[ek]; !ok {
				continue
			}
			ce := clusterEndpoints(a.cluster, region[a.cluster], cs.endpoints[ek])
			ce.Namespace, ce.Name = ref.GetNamespace(), ref.GetName()
			p.Endpoints = append(p.Endpoints, ce)
			readyTotal += ce.ReadyEndpointCount
			if ref.GetRole() == gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI {
				readyFrontend += ce.ReadyEndpointCount
			}
		}
	}
	p.ReadyEndpointCount = readyFrontend
	p.UpdatedAt = timestamppb.New(updated)
	in := registry.PoolStateInput{
		LabelledExport:     len(a.exports) > 0,
		Overlay:            a.rec != nil,
		OverlayServices:    a.rec != nil && len(a.rec.p.GetServices()) > 0,
		ClusterLeaseActive: active[a.cluster],
		PoolLeaseActive:    p.GetLease().GetState() == gdsv1.LeaseState_LEASE_STATE_ACTIVE,
		ReadyEndpoints:     int(readyTotal),
		Started:            a.rec != nil && a.rec.started,
	}
	if p.GetDesiredState() == gdsv1.PoolState_POOL_STATE_DRAINING {
		in.Desired = registry.PoolStateDraining
	}
	p.State = registryPoolStates[registry.DerivePoolState(in)]
	return p
}
