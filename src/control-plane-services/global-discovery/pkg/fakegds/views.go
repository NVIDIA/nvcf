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
			QuicPort:          portByName(spec.GetPorts(), api.QUICPortName(spec.GetAnnotations())),
			DialScheme:        spec.GetAnnotations()[api.AnnotationDialScheme],
			Labels:            maps.Clone(spec.GetLabels()),
			Annotations:       maps.Clone(spec.GetAnnotations()),
		}
		for _, port := range spec.GetPorts() {
			r.Ports = append(r.Ports, proto.CloneOf(port))
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

// LabelPoolID returns the pool id of a pool that no RegisterPool call named
// an id for: the deterministic id of the server (UUID version 5 of
// clusterset, cluster and pool name, registry.PoolID). Pools derived from
// labelled exports, pools created by RegisterPoolAgent and pools registered
// without pool_id all get it, so every source of one (cluster, pool name)
// converges on one pool.
func LabelPoolID(clusterset, cluster, name string) string {
	return registry.PoolID(clusterset, cluster, name)
}

var registryPoolStates = map[registry.PoolState]gdsv1.PoolState{
	registry.PoolStateRegistered: gdsv1.PoolState_POOL_STATE_REGISTERED,
	registry.PoolStateActive:     gdsv1.PoolState_POOL_STATE_ACTIVE,
	registry.PoolStateDraining:   gdsv1.PoolState_POOL_STATE_DRAINING,
	registry.PoolStateUnhealthy:  gdsv1.PoolState_POOL_STATE_UNHEALTHY,
	registry.PoolStateGone:       gdsv1.PoolState_POOL_STATE_GONE,
}

// roleOf maps a pool role label value to its FrontendRole.
func roleOf(role string) gdsv1.FrontendRole {
	switch role {
	case api.RolePoolFrontend:
		return gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI
	case api.RoleKVRelay:
		return gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY
	case api.RolePylon:
		return gdsv1.FrontendRole_FRONTEND_ROLE_PYLON
	}
	return gdsv1.FrontendRole_FRONTEND_ROLE_UNSPECIFIED
}

// exportRoles returns the pool roles of an export (api.ParseRoles).
func exportRoles(e *gdsv1.ServiceExport) []string {
	return api.ParseRoles(e.GetSpec().GetLabels(), e.GetSpec().GetAnnotations())
}

// rolePrecedence orders the pool roles of one Service: the frontend first,
// then Pylon, then the relay.
var rolePrecedence = []string{api.RolePoolFrontend, api.RolePylon, api.RoleKVRelay}

// exportRank is the position of an export's first role in rolePrecedence.
func exportRank(roles []string) int {
	for i, r := range rolePrecedence {
		if slices.Contains(roles, r) {
			return i
		}
	}
	return len(rolePrecedence)
}

// primaryRole is the role a Service with several pool roles is listed with
// in services[].
func primaryRole(roles []string) gdsv1.FrontendRole {
	if i := exportRank(roles); i < len(rolePrecedence) {
		return roleOf(rolePrecedence[i])
	}
	return gdsv1.FrontendRole_FRONTEND_ROLE_UNSPECIFIED
}

// validAgentRole reports whether a role can be registered as a pool agent.
func validAgentRole(r gdsv1.FrontendRole) bool {
	switch r {
	case gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY, gdsv1.FrontendRole_FRONTEND_ROLE_PYLON:
		return true
	}
	return false
}

// isIngress reports whether a role takes requests: OPENAI by inbound
// dispatch, PYLON over its reverse tunnel.
func isIngress(r gdsv1.FrontendRole) bool {
	return r == gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI || r == gdsv1.FrontendRole_FRONTEND_ROLE_PYLON
}

// dialMode is INBOUND for the OPENAI frontend, which routers dial, and
// OUTBOUND for PYLON and KV_RELAY, which dial the routers.
func dialMode(r gdsv1.FrontendRole) gdsv1.DialMode {
	if r == gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI {
		return gdsv1.DialMode_DIAL_MODE_INBOUND
	}
	return gdsv1.DialMode_DIAL_MODE_OUTBOUND
}

// agentPortName is the Service port an agent of role r is reached on: the
// HTTP port of the frontend, the gRPC port of the relay, none for Pylon.
func agentPortName(r gdsv1.FrontendRole, annotations map[string]string) string {
	grpcName, httpName := api.PortNames(annotations)
	switch r {
	case gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI:
		return httpName
	case gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY:
		return grpcName
	}
	return ""
}

// poolAgg gathers the sources of one pool.
type poolAgg struct {
	key     poolKey
	rec     *poolRec
	exports []*gdsv1.ServiceExport
	agents  []*agentRec
}

// agentPoolKey returns the pool an agent registration belongs to: the API
// registration holding its pool id, which a rename moves, else the
// (cluster, pool name) it registered with.
func agentPoolKey(cs *clusterSet, ag *agentRec) poolKey {
	if rec := cs.pools[ag.poolID]; rec != nil {
		return poolKey{rec.p.GetLocation().GetCluster(), rec.p.GetName()}
	}
	return poolKey{ag.cluster, ag.pool}
}

// poolsView merges the API registrations, the labelled exports and the
// agent registrations by (cluster, pool name) and derives one pool from
// each. It records which pools have been live in cs.started.
func poolsView(cs *clusterSet, active map[string]bool, region map[string]string, now time.Time) map[string]*gdsv1.Pool {
	aggs := map[poolKey]*poolAgg{}
	get := func(k poolKey) *poolAgg {
		if aggs[k] == nil {
			aggs[k] = &poolAgg{key: k}
		}
		return aggs[k]
	}
	for _, id := range slices.Sorted(maps.Keys(cs.pools)) {
		rec := cs.pools[id]
		if a := get(poolKey{rec.p.GetLocation().GetCluster(), rec.p.GetName()}); a.rec == nil {
			a.rec = rec
		}
	}
	for _, e := range cs.exports {
		name := e.GetSpec().GetLabels()[api.LabelPool]
		if len(exportRoles(e)) == 0 || !registry.IsDNSLabel(name) {
			continue
		}
		a := get(poolKey{e.GetCluster(), name})
		a.exports = append(a.exports, e)
	}
	for _, ag := range cs.agents {
		a := get(agentPoolKey(cs, ag))
		a.agents = append(a.agents, ag)
	}
	out := map[string]*gdsv1.Pool{}
	keys := slices.SortedFunc(maps.Keys(aggs), func(x, y poolKey) int {
		return cmp.Or(cmp.Compare(x.cluster, y.cluster), cmp.Compare(x.name, y.name))
	})
	for _, k := range keys {
		p := poolView(cs, aggs[k], active, region, now)
		if _, dup := out[p.GetPoolId()]; !dup {
			out[p.GetPoolId()] = p
		}
	}
	for k := range cs.started {
		if aggs[k] == nil {
			delete(cs.started, k)
		}
	}
	return out
}

// agentSources holds the sources of one pool agent.
type agentSources struct {
	// label is the first export with the role by namespace and name;
	// total and ready count the endpoints of every export with the role
	// while the cluster lease is active.
	label        *gdsv1.ServiceExport
	total, ready int32
	regs         []*agentRec
}

// registrationView returns a registration with the read-time fields of its
// lease filled.
func registrationView(rec *agentRec, now time.Time) *gdsv1.PoolAgentRegistration {
	r := proto.CloneOf(rec.r)
	r.Lease = leaseView(rec.r.GetLease(), rec.ttl, now)
	return r
}

// poolAgents merges the label source and the registrations of a pool's
// agents, one per role present, sorted by role.
func poolAgents(cs *clusterSet, a *poolAgg, clusterActive bool, now time.Time) []*gdsv1.PoolAgent {
	sources := map[gdsv1.FrontendRole]*agentSources{}
	get := func(r gdsv1.FrontendRole) *agentSources {
		if sources[r] == nil {
			sources[r] = &agentSources{}
		}
		return sources[r]
	}
	exports := slices.Clone(a.exports)
	slices.SortFunc(exports, func(x, y *gdsv1.ServiceExport) int {
		return cmp.Or(cmp.Compare(x.GetNamespace(), y.GetNamespace()), cmp.Compare(x.GetName(), y.GetName()))
	})
	for _, e := range exports {
		var total, ready int32
		if clusterActive {
			if rec := cs.endpoints[exportKey{a.key.cluster, e.GetNamespace(), e.GetName()}]; rec != nil {
				total, ready = countEndpoints(rec.endpoints)
			}
		}
		for _, role := range exportRoles(e) {
			src := get(roleOf(role))
			if src.label == nil {
				src.label = e
			}
			src.total += total
			src.ready += ready
		}
	}
	for _, ag := range a.agents {
		src := get(ag.role)
		src.regs = append(src.regs, ag)
	}
	var out []*gdsv1.PoolAgent
	for _, role := range slices.Sorted(maps.Keys(sources)) {
		src := sources[role]
		ag := &gdsv1.PoolAgent{Role: role, DialMode: dialMode(role)}
		if e := src.label; e != nil {
			ag.Source = gdsv1.AgentSource_AGENT_SOURCE_LABEL
			ag.Service = &gdsv1.ServiceRef{Namespace: e.GetNamespace(), Name: e.GetName(), Role: role}
			ag.PortName = agentPortName(role, e.GetSpec().GetAnnotations())
			ag.EndpointCount, ag.ReadyEndpointCount = src.total, src.ready
		}
		if len(src.regs) > 0 {
			ag.Source = gdsv1.AgentSource_AGENT_SOURCE_API
			if src.label != nil {
				ag.Source = gdsv1.AgentSource_AGENT_SOURCE_BOTH
			}
			slices.SortFunc(src.regs, func(x, y *agentRec) int { return cmp.Compare(x.r.GetAgentId(), y.r.GetAgentId()) })
			// The agent mirrors the registration whose lease expires last,
			// the first by agent_id on a tie.
			var freshest *gdsv1.PoolAgentRegistration
			for _, rec := range src.regs {
				r := registrationView(rec, now)
				ag.Registrations = append(ag.Registrations, r)
				if r.GetLease().GetState() == gdsv1.LeaseState_LEASE_STATE_ACTIVE {
					ag.ActiveRegistrationCount++
				}
				if freshest == nil || r.GetLease().GetExpiresAt().AsTime().After(freshest.GetLease().GetExpiresAt().AsTime()) {
					freshest = r
				}
			}
			ag.Lease = freshest.GetLease()
			ag.AgentId = freshest.GetAgentId()
			ag.Attributes = freshest.GetAttributes()
			ag.Generation = freshest.GetGeneration()
			ag.RegisteredAt = freshest.GetRegisteredAt()
			ag.UpdatedAt = freshest.GetUpdatedAt()
		}
		ag.Live = (clusterActive && ag.ReadyEndpointCount > 0) || ag.ActiveRegistrationCount > 0
		out = append(out, ag)
	}
	return out
}

func poolView(cs *clusterSet, a *poolAgg, active map[string]bool, region map[string]string, now time.Time) *gdsv1.Pool {
	clusterActive := active[a.key.cluster]
	var p *gdsv1.Pool
	var updated, registered time.Time
	switch {
	case a.rec != nil:
		p = proto.CloneOf(a.rec.p)
		p.Lease = leaseView(a.rec.p.GetLease(), a.rec.ttl, now)
		updated = a.rec.p.GetUpdatedAt().AsTime()
	case len(a.agents) > 0:
		p = &gdsv1.Pool{PoolId: a.agents[0].poolID, Name: a.key.name}
	default:
		p = &gdsv1.Pool{PoolId: LabelPoolID(cs.name, a.key.cluster, a.key.name), Name: a.key.name}
	}
	p.Clusterset = cs.name
	if p.Location == nil {
		p.Location = &gdsv1.Location{}
	}
	p.Location.Cluster = a.key.cluster
	if p.Location.Region == "" {
		p.Location.Region = region[a.key.cluster]
	}
	// Frontends first, then Pylons, then relays, each by namespace and name.
	slices.SortFunc(a.exports, func(x, y *gdsv1.ServiceExport) int {
		return cmp.Or(cmp.Compare(exportRank(exportRoles(x)), exportRank(exportRoles(y))),
			cmp.Compare(x.GetNamespace(), y.GetNamespace()), cmp.Compare(x.GetName(), y.GetName()))
	})
	hasOpenAI := slices.ContainsFunc(p.GetFrontends(), func(f *gdsv1.FrontendEndpoint) bool {
		return f.GetRole() == gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI
	})
	// labelRoles holds the roles of the pool's labelled exports by
	// namespace/name.
	labelRoles := map[string][]string{}
	for _, e := range a.exports {
		spec := e.GetSpec()
		ann := spec.GetAnnotations()
		roles := exportRoles(e)
		labelRoles[e.GetNamespace()+"/"+e.GetName()] = roles
		frontend := slices.Contains(roles, api.RolePoolFrontend)
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
		if len(p.Labels) == 0 && (frontend || slices.Contains(roles, api.RolePylon)) {
			p.Labels = maps.Clone(spec.GetLabels())
		}
		if !slices.ContainsFunc(p.Services, func(r *gdsv1.ServiceRef) bool {
			return r.GetNamespace() == e.GetNamespace() && r.GetName() == e.GetName()
		}) {
			p.Services = append(p.Services, &gdsv1.ServiceRef{Namespace: e.GetNamespace(), Name: e.GetName(), Role: primaryRole(roles)})
		}
		if frontend && !hasOpenAI {
			hasOpenAI = true
			p.Frontends = append(p.Frontends, &gdsv1.FrontendEndpoint{
				Role:     gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI,
				Service:  &gdsv1.ServiceRef{Namespace: e.GetNamespace(), Name: e.GetName(), Role: gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI},
				PortName: agentPortName(gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI, ann),
			})
		}
		if t := e.GetUpdatedAt().AsTime(); t.After(updated) {
			updated = t
		}
	}
	var readyOpenAI, readyIngress int32
	if clusterActive {
		for _, ref := range p.Services {
			ek := exportKey{a.key.cluster, ref.GetNamespace(), ref.GetName()}
			if _, ok := cs.exports[ek]; !ok {
				continue
			}
			ce := clusterEndpoints(a.key.cluster, region[a.key.cluster], cs.endpoints[ek])
			ce.Namespace, ce.Name = ref.GetNamespace(), ref.GetName()
			p.Endpoints = append(p.Endpoints, ce)
			roles := []gdsv1.FrontendRole{ref.GetRole()}
			for _, r := range labelRoles[ref.GetNamespace()+"/"+ref.GetName()] {
				roles = append(roles, roleOf(r))
			}
			if slices.Contains(roles, gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI) {
				readyOpenAI += ce.ReadyEndpointCount
			}
			// A Service with several ingress roles counts once.
			if slices.ContainsFunc(roles, isIngress) {
				readyIngress += ce.ReadyEndpointCount
			}
		}
	}
	p.ReadyEndpointCount = readyOpenAI
	p.ReadyIngressCount = readyIngress
	p.Agents = poolAgents(cs, a, clusterActive, now)

	in := registry.PoolStateInput{
		LabelledExport:        len(a.exports) > 0,
		Overlay:               a.rec != nil,
		OverlayServices:       a.rec != nil && len(a.rec.p.GetServices()) > 0,
		ClusterLeaseActive:    clusterActive,
		PoolLeaseActive:       p.GetLease().GetState() == gdsv1.LeaseState_LEASE_STATE_ACTIVE,
		ReadyIngressEndpoints: int(readyIngress),
		Agents:                len(a.agents) > 0,
	}
	for _, ag := range p.Agents {
		leaseActive := ag.GetActiveRegistrationCount() > 0
		in.AgentLeaseActive = in.AgentLeaseActive || leaseActive
		in.IngressAgentLeaseActive = in.IngressAgentLeaseActive || (leaseActive && isIngress(ag.GetRole()))
		if ag.GetRole() == gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY && ag.GetLive() {
			p.KvRelayLive = true
		}
	}
	for _, ag := range a.agents {
		if t := ag.r.GetUpdatedAt().AsTime(); t.After(updated) {
			updated = t
		}
		if t := ag.r.GetRegisteredAt().AsTime(); registered.IsZero() || t.Before(registered) {
			registered = t
		}
	}
	if a.rec == nil && !registered.IsZero() {
		p.RegisteredAt = timestamppb.New(registered)
	}
	p.UpdatedAt = timestamppb.New(updated)
	if p.GetDesiredState() == gdsv1.PoolState_POOL_STATE_DRAINING {
		in.Desired = registry.PoolStateDraining
	}
	// An API-only pool starts with its first heartbeat; any other pool
	// when it is first live.
	if in.APIOnly() {
		in.Started = a.rec.started
	} else {
		in.Started = cs.started[a.key]
	}
	in.Started = in.Started || (!in.APIOnly() && in.Live())
	cs.started[a.key] = in.Started
	p.State = registryPoolStates[registry.DerivePoolState(in)]
	return p
}
