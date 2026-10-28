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

package dynamodb

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// Attribute names. Conditions and updates use the top-level attributes;
// everything else of a record is in body, a serialized nvidia.gds.v1
// message. A field kept at top level is never also set in body, so each
// value has one source of truth.
const (
	attrPK             = "pk"
	attrSK             = "sk"
	attrBody           = "body"
	attrGen            = "gen"
	attrLeaseEpoch     = "lease_epoch"
	attrLeaseRenewedAt = "lease_renewed_at"
	attrOwnerID        = "owner_id"
	attrUpdatedAt      = "updated_at"
	attrExpiresAt      = "expires_at"
	attrPoolID         = "pool_id"
	attrPoolName       = "pool_name"
)

// item is the attribute layout shared by every item kind. Times are Unix
// nanoseconds, except expires_at, which DynamoDB TTL reads as Unix seconds.
// Absent pointers mean a zero time.Time; gen is a pointer so that a stored
// generation 0 is still present for the gen < :gen condition.
type item struct {
	PK             string  `dynamodbav:"pk"`
	SK             string  `dynamodbav:"sk"`
	Body           []byte  `dynamodbav:"body,omitempty"`
	Gen            *uint64 `dynamodbav:"gen,omitempty"`
	LeaseEpoch     uint64  `dynamodbav:"lease_epoch,omitempty"`
	LeaseRenewedAt *int64  `dynamodbav:"lease_renewed_at,omitempty"`
	OwnerID        string  `dynamodbav:"owner_id,omitempty"`
	UpdatedAt      *int64  `dynamodbav:"updated_at,omitempty"`
	ExpiresAt      int64   `dynamodbav:"expires_at,omitempty"`
	PoolID         string  `dynamodbav:"pool_id,omitempty"`
	PoolName       string  `dynamodbav:"pool_name,omitempty"`
}

func (it item) attributes() (map[string]ddbtypes.AttributeValue, error) {
	av, err := attributevalue.MarshalMap(it)
	if err != nil {
		return nil, fmt.Errorf("dynamodb: marshal item %s/%s: %w", it.PK, it.SK, err)
	}
	return av, nil
}

func parseItem(av map[string]ddbtypes.AttributeValue) (item, error) {
	var it item
	if err := attributevalue.UnmarshalMap(av, &it); err != nil {
		return item{}, fmt.Errorf("dynamodb: unmarshal item: %w", err)
	}
	return it, nil
}

func (it item) gen() uint64 {
	if it.Gen == nil {
		return 0
	}
	return *it.Gen
}

var marshalOptions = proto.MarshalOptions{Deterministic: true}

func marshalBody(m proto.Message) ([]byte, error) {
	b, err := marshalOptions.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("dynamodb: marshal body: %w", err)
	}
	return b, nil
}

func unmarshalBody(it item, m proto.Message) error {
	if err := proto.Unmarshal(it.Body, m); err != nil {
		return fmt.Errorf("dynamodb: unmarshal body of %s/%s: %w", it.PK, it.SK, err)
	}
	return nil
}

// Times and durations.

func unixNanos(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	n := t.UnixNano()
	return &n
}

func fromUnixNanos(n *int64) time.Time {
	if n == nil {
		return time.Time{}
	}
	return time.Unix(0, *n).UTC()
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func fromTimestamp(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func leaseTTL(ttl time.Duration) *gdsv1.Lease {
	if ttl == 0 {
		return nil
	}
	return &gdsv1.Lease{Ttl: durationpb.New(ttl)}
}

func fromLeaseTTL(l *gdsv1.Lease) time.Duration {
	if l == nil || l.Ttl == nil {
		return 0
	}
	return l.Ttl.AsDuration()
}

func ptr[T any](v T) *T { return &v }

// Clusters.

func encodeCluster(rec store.ClusterRecord, expiresAt int64) (item, error) {
	body, err := marshalBody(&gdsv1.Cluster{
		Region:       rec.Region,
		Zones:        rec.Zones,
		Provider:     rec.Provider,
		Labels:       rec.Labels,
		AgentId:      rec.AgentID,
		AgentVersion: rec.AgentVersion,
		Lease:        leaseTTL(rec.Lease.TTL),
		RegisteredAt: timestamp(rec.RegisteredAt),
	})
	if err != nil {
		return item{}, err
	}
	k := clusterKey(rec.Key)
	return item{
		PK:             k.pk,
		SK:             k.sk,
		Body:           body,
		Gen:            ptr(rec.Generation),
		LeaseEpoch:     rec.Lease.Epoch,
		LeaseRenewedAt: unixNanos(rec.Lease.RenewedAt),
		UpdatedAt:      unixNanos(rec.UpdatedAt),
		ExpiresAt:      expiresAt,
	}, nil
}

func decodeCluster(it item) (store.ClusterRecord, error) {
	cs, ok := parseClusterSetPK(it.PK)
	if !ok {
		return store.ClusterRecord{}, fmt.Errorf("dynamodb: %q is not a clusterset partition", it.PK)
	}
	key, ok := parseClusterSK(cs, it.SK)
	if !ok {
		return store.ClusterRecord{}, fmt.Errorf("dynamodb: %q is not a cluster key", it.SK)
	}
	var body gdsv1.Cluster
	if err := unmarshalBody(it, &body); err != nil {
		return store.ClusterRecord{}, err
	}
	return store.ClusterRecord{
		Key:          key,
		Region:       body.Region,
		Zones:        body.Zones,
		Provider:     body.Provider,
		Labels:       body.Labels,
		AgentID:      body.AgentId,
		AgentVersion: body.AgentVersion,
		Lease: store.Lease{
			Epoch:     it.LeaseEpoch,
			TTL:       fromLeaseTTL(body.Lease),
			RenewedAt: fromUnixNanos(it.LeaseRenewedAt),
		},
		Generation:   it.gen(),
		RegisteredAt: fromTimestamp(body.RegisteredAt),
		UpdatedAt:    fromUnixNanos(it.UpdatedAt),
	}, nil
}

// Exports.

func encodeExport(rec store.ExportRecord) (item, error) {
	var e encoder
	spec := e.exportSpec(rec.Spec)
	if e.err != nil {
		return item{}, fmt.Errorf("dynamodb: export %s/%s/%s: %w", rec.Key.Cluster, rec.Key.Namespace, rec.Key.Name, e.err)
	}
	body, err := marshalBody(spec)
	if err != nil {
		return item{}, err
	}
	k := exportKey(rec.Key)
	return item{PK: k.pk, SK: k.sk, Body: body, Gen: ptr(rec.Generation), UpdatedAt: unixNanos(rec.UpdatedAt)}, nil
}

func decodeExport(it item) (store.ExportRecord, error) {
	cs, ok := parseClusterSetPK(it.PK)
	if !ok {
		return store.ExportRecord{}, fmt.Errorf("dynamodb: %q is not a clusterset partition", it.PK)
	}
	key, ok := parseExportSK(cs, it.SK)
	if !ok {
		return store.ExportRecord{}, fmt.Errorf("dynamodb: %q is not an export key", it.SK)
	}
	var body gdsv1.ServiceExportSpec
	if err := unmarshalBody(it, &body); err != nil {
		return store.ExportRecord{}, err
	}
	return store.ExportRecord{
		Key:        key,
		Spec:       decodeExportSpec(&body),
		Generation: it.gen(),
		UpdatedAt:  fromUnixNanos(it.UpdatedAt),
	}, nil
}

// Endpoint sets.

func encodeEndpointSet(rec store.EndpointSetRecord) (item, error) {
	var e encoder
	msg := &gdsv1.ClusterEndpoints{Endpoints: make([]*gdsv1.Endpoint, 0, len(rec.Endpoints))}
	for _, ep := range rec.Endpoints {
		msg.Endpoints = append(msg.Endpoints, e.endpoint(ep))
	}
	if e.err != nil {
		return item{}, fmt.Errorf("dynamodb: endpoint set %s/%s/%s: %w", rec.Key.Cluster, rec.Key.Namespace, rec.Key.Name, e.err)
	}
	body, err := marshalBody(msg)
	if err != nil {
		return item{}, err
	}
	k := endpointSetKey(rec.Key)
	return item{PK: k.pk, SK: k.sk, Body: body, Gen: ptr(rec.Generation), UpdatedAt: unixNanos(rec.UpdatedAt)}, nil
}

func decodeEndpointSet(it item) (store.EndpointSetRecord, error) {
	cs, cluster, ok := parseEndpointPK(it.PK)
	if !ok {
		return store.EndpointSetRecord{}, fmt.Errorf("dynamodb: %q is not an endpoint partition", it.PK)
	}
	key, ok := parseEndpointSetSK(cs, cluster, it.SK)
	if !ok {
		return store.EndpointSetRecord{}, fmt.Errorf("dynamodb: %q is not an endpoint set key", it.SK)
	}
	var body gdsv1.ClusterEndpoints
	if err := unmarshalBody(it, &body); err != nil {
		return store.EndpointSetRecord{}, err
	}
	rec := store.EndpointSetRecord{Key: key, Generation: it.gen(), UpdatedAt: fromUnixNanos(it.UpdatedAt)}
	if len(body.Endpoints) > 0 {
		rec.Endpoints = make([]store.Endpoint, 0, len(body.Endpoints))
		for _, ep := range body.Endpoints {
			rec.Endpoints = append(rec.Endpoints, decodeEndpoint(ep))
		}
	}
	return rec, nil
}

// Pools.

// encodePoolBody returns the body of a pool: every field except the key,
// the generation, the owner, UpdatedAt and the lease epoch and renewal time.
func encodePoolBody(rec store.PoolRecord) ([]byte, error) {
	var e encoder
	msg := e.pool(rec)
	if e.err != nil {
		return nil, fmt.Errorf("dynamodb: pool %s/%s: %w", rec.Key.Cluster, rec.Key.PoolID, e.err)
	}
	return marshalBody(msg)
}

func encodePool(rec store.PoolRecord, expiresAt int64) (item, error) {
	body, err := encodePoolBody(rec)
	if err != nil {
		return item{}, err
	}
	k := poolKey(rec.Key)
	return item{
		PK:             k.pk,
		SK:             k.sk,
		Body:           body,
		Gen:            ptr(rec.Generation),
		LeaseEpoch:     rec.Lease.Epoch,
		LeaseRenewedAt: unixNanos(rec.Lease.RenewedAt),
		OwnerID:        rec.OwnerID,
		UpdatedAt:      unixNanos(rec.UpdatedAt),
		ExpiresAt:      expiresAt,
	}, nil
}

func decodePool(it item) (store.PoolRecord, error) {
	cs, ok := parseClusterSetPK(it.PK)
	if !ok {
		return store.PoolRecord{}, fmt.Errorf("dynamodb: %q is not a clusterset partition", it.PK)
	}
	key, ok := parsePoolSK(cs, it.SK)
	if !ok {
		return store.PoolRecord{}, fmt.Errorf("dynamodb: %q is not a pool key", it.SK)
	}
	var body gdsv1.Pool
	if err := unmarshalBody(it, &body); err != nil {
		return store.PoolRecord{}, err
	}
	rec := store.PoolRecord{
		Key:  key,
		Name: body.Name,
		Spec: store.PoolSpec{
			HardwareClass:    body.HardwareClass,
			WorkerCount:      body.WorkerCount,
			ReadyWorkerCount: body.ReadyWorkerCount,
			Labels:           body.Labels,
			DesiredState:     desiredStates.decode(body.DesiredState),
		},
		Lease: store.Lease{
			Epoch:     it.LeaseEpoch,
			TTL:       fromLeaseTTL(body.Lease),
			RenewedAt: fromUnixNanos(it.LeaseRenewedAt),
		},
		Generation:   it.gen(),
		OwnerID:      it.OwnerID,
		RegisteredAt: fromTimestamp(body.RegisteredAt),
		UpdatedAt:    fromUnixNanos(it.UpdatedAt),
	}
	if l := body.Location; l != nil {
		rec.Spec.Location = store.Location{Region: l.Region, DC: l.Dc, Cluster: l.Cluster, Zones: l.Zones}
	}
	for _, m := range body.Models {
		rec.Spec.Models = append(rec.Spec.Models, store.ModelServed{ModelID: m.ModelId, Aliases: m.Aliases, Attributes: m.Attributes})
	}
	for _, f := range body.Frontends {
		fe := store.FrontendEndpoint{Role: frontendRoles.decode(f.Role), URL: f.Url, PortName: f.PortName}
		if f.Service != nil {
			ref := decodeServiceRef(f.Service)
			fe.Service = &ref
		}
		rec.Spec.Frontends = append(rec.Spec.Frontends, fe)
	}
	for _, s := range body.Services {
		rec.Spec.Services = append(rec.Spec.Services, decodeServiceRef(s))
	}
	return rec, nil
}

// Pool agents.

// encodePoolAgentBody returns the body of an agent registration: the
// agent id, the attributes, the lease TTL and RegisteredAt. The key (with
// the role), the pool name, the generation, UpdatedAt and the lease epoch
// and renewal time are top level.
func encodePoolAgentBody(rec store.PoolAgentRecord) ([]byte, error) {
	return marshalBody(&gdsv1.PoolAgent{
		AgentId:      rec.AgentID,
		Attributes:   rec.Attributes,
		Lease:        leaseTTL(rec.Lease.TTL),
		RegisteredAt: timestamp(rec.RegisteredAt),
	})
}

func encodePoolAgent(rec store.PoolAgentRecord, expiresAt int64) (item, error) {
	if _, ok := frontendRoles.toProto[rec.Key.Role]; !ok {
		return item{}, fmt.Errorf("dynamodb: pool agent %s/%s: unknown frontend role %q", rec.Key.Cluster, rec.Key.PoolID, rec.Key.Role)
	}
	body, err := encodePoolAgentBody(rec)
	if err != nil {
		return item{}, err
	}
	k := poolAgentKey(rec.Key)
	return item{
		PK:             k.pk,
		SK:             k.sk,
		Body:           body,
		Gen:            ptr(rec.Generation),
		LeaseEpoch:     rec.Lease.Epoch,
		LeaseRenewedAt: unixNanos(rec.Lease.RenewedAt),
		UpdatedAt:      unixNanos(rec.UpdatedAt),
		ExpiresAt:      expiresAt,
		PoolName:       rec.PoolName,
	}, nil
}

func decodePoolAgent(it item) (store.PoolAgentRecord, error) {
	cs, ok := parseClusterSetPK(it.PK)
	if !ok {
		return store.PoolAgentRecord{}, fmt.Errorf("dynamodb: %q is not a clusterset partition", it.PK)
	}
	key, ok := parsePoolAgentSK(cs, it.SK)
	if !ok {
		return store.PoolAgentRecord{}, fmt.Errorf("dynamodb: %q is not a pool agent key", it.SK)
	}
	var body gdsv1.PoolAgent
	if err := unmarshalBody(it, &body); err != nil {
		return store.PoolAgentRecord{}, err
	}
	return store.PoolAgentRecord{
		Key:        key,
		PoolName:   it.PoolName,
		AgentID:    body.AgentId,
		Attributes: body.Attributes,
		Lease: store.Lease{
			Epoch:     it.LeaseEpoch,
			TTL:       fromLeaseTTL(body.Lease),
			RenewedAt: fromUnixNanos(it.LeaseRenewedAt),
		},
		Generation:   it.gen(),
		RegisteredAt: fromTimestamp(body.RegisteredAt),
		UpdatedAt:    fromUnixNanos(it.UpdatedAt),
	}, nil
}

func decodeServiceRef(r *gdsv1.ServiceRef) store.ServiceRef {
	return store.ServiceRef{Namespace: r.Namespace, Name: r.Name, Role: frontendRoles.decode(r.Role)}
}

// Enumerations. Store records use Kubernetes spellings, the body uses the
// proto enums. Encoding an unknown value fails rather than losing it;
// decoding an unknown proto value (written by a newer server) yields the
// empty value.

type enumMap[S ~string, P ~int32] struct {
	name      string
	toProto   map[S]P
	fromProto map[P]S
}

func newEnumMap[S ~string, P ~int32](name string, pairs map[S]P) enumMap[S, P] {
	m := enumMap[S, P]{name: name, toProto: pairs, fromProto: make(map[P]S, len(pairs))}
	for s, p := range pairs {
		m.fromProto[p] = s
	}
	return m
}

func (m enumMap[S, P]) encode(s S) (P, error) {
	if s == "" {
		return 0, nil
	}
	p, ok := m.toProto[s]
	if !ok {
		return 0, fmt.Errorf("unknown %s %q", m.name, s)
	}
	return p, nil
}

func (m enumMap[S, P]) decode(p P) S {
	return m.fromProto[p]
}

var (
	serviceTypes = newEnumMap("service type", map[store.ServiceType]gdsv1.ServiceType{
		store.ServiceTypeClusterSetIP: gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP,
		store.ServiceTypeHeadless:     gdsv1.ServiceType_SERVICE_TYPE_HEADLESS,
	})
	protocols = newEnumMap("protocol", map[store.Protocol]gdsv1.Protocol{
		store.ProtocolTCP:  gdsv1.Protocol_PROTOCOL_TCP,
		store.ProtocolUDP:  gdsv1.Protocol_PROTOCOL_UDP,
		store.ProtocolSCTP: gdsv1.Protocol_PROTOCOL_SCTP,
	})
	sessionAffinities = newEnumMap("session affinity", map[store.SessionAffinity]gdsv1.SessionAffinity{
		store.SessionAffinityNone:     gdsv1.SessionAffinity_SESSION_AFFINITY_NONE,
		store.SessionAffinityClientIP: gdsv1.SessionAffinity_SESSION_AFFINITY_CLIENT_IP,
	})
	ipFamilies = newEnumMap("IP family", map[store.IPFamily]gdsv1.IPFamily{
		store.IPFamilyIPv4: gdsv1.IPFamily_IP_FAMILY_IPV4,
		store.IPFamilyIPv6: gdsv1.IPFamily_IP_FAMILY_IPV6,
	})
	trafficPolicies = newEnumMap("internal traffic policy", map[store.InternalTrafficPolicy]gdsv1.InternalTrafficPolicy{
		store.InternalTrafficPolicyCluster: gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_CLUSTER,
		store.InternalTrafficPolicyLocal:   gdsv1.InternalTrafficPolicy_INTERNAL_TRAFFIC_POLICY_LOCAL,
	})
	addressTypes = newEnumMap("address type", map[store.AddressType]gdsv1.AddressType{
		store.AddressTypeIPv4: gdsv1.AddressType_ADDRESS_TYPE_IPV4,
		store.AddressTypeIPv6: gdsv1.AddressType_ADDRESS_TYPE_IPV6,
		store.AddressTypeFQDN: gdsv1.AddressType_ADDRESS_TYPE_FQDN,
	})
	frontendRoles = newEnumMap("frontend role", map[store.FrontendRole]gdsv1.FrontendRole{
		store.FrontendRoleOpenAI:  gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI,
		store.FrontendRoleKVRelay: gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY,
		store.FrontendRolePylon:   gdsv1.FrontendRole_FRONTEND_ROLE_PYLON,
	})
	desiredStates = newEnumMap("desired state", map[store.DesiredState]gdsv1.PoolState{
		store.DesiredStateActive:   gdsv1.PoolState_POOL_STATE_ACTIVE,
		store.DesiredStateDraining: gdsv1.PoolState_POOL_STATE_DRAINING,
	})
)

// encoder converts store values to proto messages and keeps the first
// enumeration error.
type encoder struct {
	err error
}

func encodeEnum[S ~string, P ~int32](e *encoder, m enumMap[S, P], s S) P {
	p, err := m.encode(s)
	if err != nil && e.err == nil {
		e.err = err
	}
	return p
}

func (e *encoder) exportSpec(s store.ExportSpec) *gdsv1.ServiceExportSpec {
	msg := &gdsv1.ServiceExportSpec{
		Annotations:             s.Annotations,
		Labels:                  s.Labels,
		ExportCreationTimestamp: timestamp(s.ExportCreationTimestamp),
		Type:                    encodeEnum(e, serviceTypes, s.Type),
		SessionAffinity:         encodeEnum(e, sessionAffinities, s.SessionAffinity),
		InternalTrafficPolicy:   encodeEnum(e, trafficPolicies, s.InternalTrafficPolicy),
		TrafficDistribution:     s.TrafficDistribution,
	}
	for _, p := range s.Ports {
		msg.Ports = append(msg.Ports, &gdsv1.ServicePort{
			Name:        p.Name,
			Protocol:    encodeEnum(e, protocols, p.Protocol),
			AppProtocol: p.AppProtocol,
			Port:        p.Port,
		})
	}
	if c := s.SessionAffinityConfig; c != nil {
		msg.SessionAffinityConfig = &gdsv1.SessionAffinityConfig{}
		if c.ClientIP != nil {
			msg.SessionAffinityConfig.ClientIp = &gdsv1.ClientIPConfig{TimeoutSeconds: c.ClientIP.TimeoutSeconds}
		}
	}
	for _, f := range s.IPFamilies {
		msg.IpFamilies = append(msg.IpFamilies, encodeEnum(e, ipFamilies, f))
	}
	return msg
}

func decodeExportSpec(msg *gdsv1.ServiceExportSpec) store.ExportSpec {
	s := store.ExportSpec{
		Annotations:             msg.Annotations,
		Labels:                  msg.Labels,
		ExportCreationTimestamp: fromTimestamp(msg.ExportCreationTimestamp),
		Type:                    serviceTypes.decode(msg.Type),
		SessionAffinity:         sessionAffinities.decode(msg.SessionAffinity),
		InternalTrafficPolicy:   trafficPolicies.decode(msg.InternalTrafficPolicy),
		TrafficDistribution:     msg.TrafficDistribution,
	}
	for _, p := range msg.Ports {
		s.Ports = append(s.Ports, store.ServicePort{
			Name:        p.Name,
			Protocol:    protocols.decode(p.Protocol),
			AppProtocol: p.AppProtocol,
			Port:        p.Port,
		})
	}
	if c := msg.SessionAffinityConfig; c != nil {
		s.SessionAffinityConfig = &store.SessionAffinityConfig{}
		if c.ClientIp != nil {
			s.SessionAffinityConfig.ClientIP = &store.ClientIPConfig{TimeoutSeconds: c.ClientIp.TimeoutSeconds}
		}
	}
	for _, f := range msg.IpFamilies {
		s.IPFamilies = append(s.IPFamilies, ipFamilies.decode(f))
	}
	return s
}

func (e *encoder) endpoint(ep store.Endpoint) *gdsv1.Endpoint {
	msg := &gdsv1.Endpoint{
		Addresses:   ep.Addresses,
		AddressType: encodeEnum(e, addressTypes, ep.AddressType),
		Conditions: &gdsv1.EndpointConditions{
			Ready:       ep.Conditions.Ready,
			Serving:     ep.Conditions.Serving,
			Terminating: ep.Conditions.Terminating,
		},
		Hostname:   ep.Hostname,
		NodeName:   ep.NodeName,
		Zone:       ep.Zone,
		Attributes: ep.Attributes,
	}
	for _, p := range ep.Ports {
		msg.Ports = append(msg.Ports, &gdsv1.EndpointPort{
			Name:        p.Name,
			Protocol:    encodeEnum(e, protocols, p.Protocol),
			AppProtocol: p.AppProtocol,
			Port:        p.Port,
		})
	}
	return msg
}

func decodeEndpoint(msg *gdsv1.Endpoint) store.Endpoint {
	ep := store.Endpoint{
		Addresses:   msg.Addresses,
		AddressType: addressTypes.decode(msg.AddressType),
		Hostname:    msg.Hostname,
		NodeName:    msg.NodeName,
		Zone:        msg.Zone,
		Attributes:  msg.Attributes,
	}
	if c := msg.Conditions; c != nil {
		ep.Conditions = store.EndpointConditions{Ready: c.Ready, Serving: c.Serving, Terminating: c.Terminating}
	}
	for _, p := range msg.Ports {
		ep.Ports = append(ep.Ports, store.EndpointPort{
			Name:        p.Name,
			Protocol:    protocols.decode(p.Protocol),
			AppProtocol: p.AppProtocol,
			Port:        p.Port,
		})
	}
	return ep
}

func (e *encoder) serviceRef(r store.ServiceRef) *gdsv1.ServiceRef {
	return &gdsv1.ServiceRef{Namespace: r.Namespace, Name: r.Name, Role: encodeEnum(e, frontendRoles, r.Role)}
}

func (e *encoder) pool(rec store.PoolRecord) *gdsv1.Pool {
	s := rec.Spec
	msg := &gdsv1.Pool{
		Name: rec.Name,
		Location: &gdsv1.Location{
			Region:  s.Location.Region,
			Dc:      s.Location.DC,
			Cluster: s.Location.Cluster,
			Zones:   s.Location.Zones,
		},
		HardwareClass:    s.HardwareClass,
		WorkerCount:      s.WorkerCount,
		ReadyWorkerCount: s.ReadyWorkerCount,
		Labels:           s.Labels,
		DesiredState:     encodeEnum(e, desiredStates, s.DesiredState),
		Lease:            leaseTTL(rec.Lease.TTL),
		RegisteredAt:     timestamp(rec.RegisteredAt),
	}
	for _, m := range s.Models {
		msg.Models = append(msg.Models, &gdsv1.ModelServed{ModelId: m.ModelID, Aliases: m.Aliases, Attributes: m.Attributes})
	}
	for _, f := range s.Frontends {
		fe := &gdsv1.FrontendEndpoint{Role: encodeEnum(e, frontendRoles, f.Role), Url: f.URL, PortName: f.PortName}
		if f.Service != nil {
			fe.Service = e.serviceRef(*f.Service)
		}
		msg.Frontends = append(msg.Frontends, fe)
	}
	for _, r := range s.Services {
		msg.Services = append(msg.Services, e.serviceRef(r))
	}
	return msg
}
