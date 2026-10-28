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
	"net/netip"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

// The seeding helpers write state the way a controller would, without the
// generation bookkeeping, and broadcast to watchers like the RPCs. They
// suit tests of consumers (routers, pylons, gateways); tests of writers
// should call the RPCs.

// EnsureCluster registers a cluster with the default 30 s lease when it is
// not registered, and renews its lease otherwise. An empty region becomes
// the server region. It returns the lease epoch.
func (s *Server) EnsureCluster(clusterset, cluster, region string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureClusterLocked(clusterset, cluster, region)
}

func (s *Server) ensureClusterLocked(clusterset, cluster, region string) uint64 {
	cs := s.setLocked(clusterset)
	if rec := cs.clusters[cluster]; rec != nil {
		rec.c.Lease.RenewedAt = timestamppb.New(s.now())
		s.commitLocked(cs)
		return rec.c.GetLease().GetEpoch()
	}
	if region == "" {
		region = s.region
	}
	rec := s.registerClusterLocked(&gdsv1.RegisterClusterRequest{Clusterset: clusterset, Cluster: cluster, Region: region})
	return rec.c.GetLease().GetEpoch()
}

// RemoveCluster deregisters a cluster and removes everything it owns, as
// DeregisterCluster does.
func (s *Server) RemoveCluster(clusterset, cluster string) {
	s.removeCluster(clusterset, cluster)
}

// PutExport stores the export of namespace/name from cluster with spec and
// replaces its endpoint set, registering the cluster first when needed.
// Generations are bumped automatically. A nil endpoints slice leaves the
// endpoint set unchanged; an empty one clears it.
func (s *Server) PutExport(clusterset, cluster, namespace, name string, spec *gdsv1.ServiceExportSpec, endpoints []*gdsv1.Endpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(clusterset)
	if cs.clusters[cluster] == nil {
		s.ensureClusterLocked(clusterset, cluster, "")
	}
	key := exportKey{cluster, namespace, name}
	gen := registry.NextGeneration(cs.exports[key].GetGeneration(), s.now())
	_ = s.putExportLocked(cs, key, spec, gen)
	if endpoints != nil {
		s.setEndpointsLocked(cs, key, endpoints)
	}
}

// SetEndpoints replaces the endpoint set of an export, bumping its
// generation.
func (s *Server) SetEndpoints(clusterset, cluster, namespace, name string, endpoints []*gdsv1.Endpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setEndpointsLocked(s.setLocked(clusterset), exportKey{cluster, namespace, name}, endpoints)
}

func (s *Server) setEndpointsLocked(cs *clusterSet, key exportKey, endpoints []*gdsv1.Endpoint) {
	var prev uint64
	if old := cs.endpoints[key]; old != nil {
		prev = old.generation
	}
	_ = s.putEndpointsLocked(cs, key, endpoints, registry.NextGeneration(prev, s.now()))
}

// DeleteExport removes an export and its endpoint set. It is idempotent.
func (s *Server) DeleteExport(clusterset, cluster, namespace, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cs := s.setLocked(clusterset)
	key := exportKey{cluster, namespace, name}
	delete(cs.exports, key)
	delete(cs.endpoints, key)
	s.commitLocked(cs)
}

// RouterSpec returns the export spec of a router Service as the router
// chart labels it: role router, ports grpc and http (http omitted when
// httpPort is 0), and the dial-scheme annotation when dialScheme is set.
func RouterSpec(grpcPort, httpPort int32, dialScheme string) *gdsv1.ServiceExportSpec {
	spec := &gdsv1.ServiceExportSpec{
		Type:   gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		Labels: map[string]string{api.LabelExport: api.ExportEnabled, api.LabelRole: api.RoleRouter},
		Ports: []*gdsv1.ServicePort{{
			Name: api.DefaultGRPCPortName, Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: grpcPort,
		}},
		SessionAffinity: gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP,
	}
	if httpPort != 0 {
		spec.Ports = append(spec.Ports, &gdsv1.ServicePort{
			Name: api.DefaultHTTPPortName, Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: httpPort,
		})
	}
	if dialScheme != "" {
		spec.Annotations = map[string]string{api.AnnotationDialScheme: dialScheme}
	}
	return spec
}

// RouterSpecWithQUIC returns RouterSpec with a UDP port named quic (the
// default of gds.nvidia.com/quic-port-name) for the reverse tunnel, omitted
// when quicPort is 0.
func RouterSpecWithQUIC(grpcPort, httpPort int32, dialScheme string, quicPort int32) *gdsv1.ServiceExportSpec {
	spec := RouterSpec(grpcPort, httpPort, dialScheme)
	if quicPort != 0 {
		spec.Ports = append(spec.Ports, &gdsv1.ServicePort{
			Name: api.DefaultQUICPortName, Protocol: gdsv1.Protocol_PROTOCOL_UDP, Port: quicPort,
		})
	}
	return spec
}

// RouterSpecDirect returns the export spec of a direct-mode router Service,
// one that selects the Stargate pods themselves: RouterSpecWithQUIC without
// an http port and without session affinity, annotated
// gds.nvidia.com/router-mode=direct and gds.nvidia.com/hostname-template
// (omitted when hostnameTemplate is empty). Give its endpoints identities
// with WithHostnames or WithPodNames.
func RouterSpecDirect(grpcPort, quicPort int32, dialScheme, hostnameTemplate string) *gdsv1.ServiceExportSpec {
	spec := RouterSpecWithQUIC(grpcPort, 0, dialScheme, quicPort)
	spec.SessionAffinity = gdsv1.SessionAffinity_SESSION_AFFINITY_NONE
	return WithRouterMode(spec, api.RouterModeDirect, hostnameTemplate)
}

// WithRouterMode sets the gds.nvidia.com/router-mode annotation of spec to
// mode and the gds.nvidia.com/hostname-template annotation to
// hostnameTemplate; an empty value removes its annotation. It modifies and
// returns spec.
func WithRouterMode(spec *gdsv1.ServiceExportSpec, mode, hostnameTemplate string) *gdsv1.ServiceExportSpec {
	for key, value := range map[string]string{api.AnnotationRouterMode: mode, api.AnnotationHostnameTemplate: hostnameTemplate} {
		if value == "" {
			delete(spec.Annotations, key)
			continue
		}
		if spec.Annotations == nil {
			spec.Annotations = map[string]string{}
		}
		spec.Annotations[key] = value
	}
	return spec
}

// PoolFrontendSpec returns the export spec of a pool frontend Service: role
// pool-frontend, the pool label, an http port and the pool-models
// annotation.
func PoolFrontendSpec(pool string, httpPort int32, models ...string) *gdsv1.ServiceExportSpec {
	spec := &gdsv1.ServiceExportSpec{
		Type: gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		Labels: map[string]string{
			api.LabelExport: api.ExportEnabled,
			api.LabelRole:   api.RolePoolFrontend,
			api.LabelPool:   pool,
		},
		Ports: []*gdsv1.ServicePort{{
			Name: api.DefaultHTTPPortName, Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: httpPort,
		}},
	}
	if len(models) > 0 {
		spec.Annotations = map[string]string{api.AnnotationPoolModels: strings.Join(models, ",")}
	}
	return spec
}

// PylonSpec returns the export spec of a Pylon Service as the Pylon
// Operator creates it: role pylon, the pool label, a metrics port (omitted
// when metricsPort is 0) and the pool-models annotation.
func PylonSpec(pool string, metricsPort int32, models ...string) *gdsv1.ServiceExportSpec {
	spec := &gdsv1.ServiceExportSpec{
		Type: gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		Labels: map[string]string{
			api.LabelExport: api.ExportEnabled,
			api.LabelRole:   api.RolePylon,
			api.LabelPool:   pool,
		},
	}
	if metricsPort != 0 {
		spec.Ports = []*gdsv1.ServicePort{{Name: "metrics", Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: metricsPort}}
	}
	if len(models) > 0 {
		spec.Annotations = map[string]string{api.AnnotationPoolModels: strings.Join(models, ",")}
	}
	return spec
}

// KVRelaySpec returns the export spec of a KV relay Service (the Dynamo
// PoolRelay): role kv-relay, the pool label and a grpc port.
func KVRelaySpec(pool string, grpcPort int32) *gdsv1.ServiceExportSpec {
	return &gdsv1.ServiceExportSpec{
		Type: gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		Labels: map[string]string{
			api.LabelExport: api.ExportEnabled,
			api.LabelRole:   api.RoleKVRelay,
			api.LabelPool:   pool,
		},
		Ports: []*gdsv1.ServicePort{{
			Name: api.DefaultGRPCPortName, Protocol: gdsv1.Protocol_PROTOCOL_TCP, Port: grpcPort,
		}},
	}
}

// WithRoles sets the gds.nvidia.com/roles annotation of spec to the given
// pool roles, for a process that fills several (a frontend embedding
// Pylon: WithRoles(PoolFrontendSpec(...), api.RolePoolFrontend,
// api.RolePylon)). It modifies and returns spec.
func WithRoles(spec *gdsv1.ServiceExportSpec, roles ...string) *gdsv1.ServiceExportSpec {
	if spec.Annotations == nil {
		spec.Annotations = map[string]string{}
	}
	spec.Annotations[api.AnnotationRoles] = strings.Join(roles, ",")
	return spec
}

// ReadyEndpoints returns one ready, serving endpoint per address, with the
// address type inferred from the address.
func ReadyEndpoints(addresses ...string) []*gdsv1.Endpoint {
	eps := make([]*gdsv1.Endpoint, 0, len(addresses))
	for _, a := range addresses {
		eps = append(eps, &gdsv1.Endpoint{
			Addresses:   []string{a},
			AddressType: addressType(a),
			Conditions:  &gdsv1.EndpointConditions{Ready: true, Serving: true},
		})
	}
	return eps
}

// WithHostnames sets the EndpointSlice hostname of eps[i] to hostnames[i],
// as a StatefulSet behind a headless Service has them. A router replica's
// identity is its hostname when set. Extra names are ignored. It modifies
// and returns eps.
func WithHostnames(eps []*gdsv1.Endpoint, hostnames ...string) []*gdsv1.Endpoint {
	for i := 0; i < len(eps) && i < len(hostnames); i++ {
		eps[i].Hostname = hostnames[i]
	}
	return eps
}

// WithPodNames gives eps[i] the target reference attributes the
// gds-mcs-controller exports for a pod named podNames[i]: targetRef.kind Pod
// and targetRef.name (api.EndpointAttrTargetName). A router replica without
// a hostname takes the pod name as its identity. An empty name leaves its
// endpoint unchanged and extra names are ignored. It modifies and returns
// eps.
func WithPodNames(eps []*gdsv1.Endpoint, podNames ...string) []*gdsv1.Endpoint {
	for i := 0; i < len(eps) && i < len(podNames); i++ {
		if podNames[i] == "" {
			continue
		}
		if eps[i].Attributes == nil {
			eps[i].Attributes = map[string]string{}
		}
		eps[i].Attributes["targetRef.kind"] = "Pod"
		eps[i].Attributes[api.EndpointAttrTargetName] = podNames[i]
	}
	return eps
}

func addressType(a string) gdsv1.AddressType {
	ip, err := netip.ParseAddr(a)
	switch {
	case err != nil:
		return gdsv1.AddressType_ADDRESS_TYPE_FQDN
	case ip.Is4():
		return gdsv1.AddressType_ADDRESS_TYPE_IPV4
	}
	return gdsv1.AddressType_ADDRESS_TYPE_IPV6
}
