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

package api

import (
	"fmt"
	"net"
	"net/url"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"

	gds "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api"
	gdsv1 "github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/api/gen/go/nvidia/gds/v1"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/registry"
)

// Field limits not covered by api/errors.go.
const (
	maxNameBytes      = 253
	maxZones          = 64
	maxAddresses      = 100
	maxIPFamilies     = 2
	maxFrontends      = 64
	maxServices       = 64
	maxAliases        = 64
	maxModelAttrs     = 16
	maxLabelKeyBytes  = 317
	maxLabelValBytes  = 1024
	maxClientIPTimout = 86400
)

// validator collects field violations.
type validator struct {
	violations []*errdetails.BadRequest_FieldViolation
}

func (v *validator) add(field, format string, args ...any) {
	v.violations = append(v.violations, &errdetails.BadRequest_FieldViolation{Field: field, Description: fmt.Sprintf(format, args...)})
}

func (v *validator) err() error {
	if len(v.violations) == 0 {
		return nil
	}
	return invalidArgument(v.violations)
}

func (v *validator) dnsLabel(field, value string) {
	switch {
	case value == "":
		v.add(field, "is required")
	case !registry.IsDNSLabel(value):
		v.add(field, "%q is not a DNS-1123 label", value)
	}
}

func (v *validator) optionalDNSLabel(field, value string) {
	if value != "" {
		v.dnsLabel(field, value)
	}
}

func (v *validator) maxLen(field, value string, n int) {
	if len(value) > n {
		v.add(field, "is longer than %d bytes", n)
	}
}

func (v *validator) labels(field string, labels map[string]string) {
	if len(labels) > gds.MaxLabels {
		v.add(field, "has %d entries; at most %d are allowed", len(labels), gds.MaxLabels)
	}
	for k, val := range labels {
		if k == "" || len(k) > maxLabelKeyBytes {
			v.add(field, "key %q must have 1 to %d bytes", k, maxLabelKeyBytes)
		}
		if len(val) > maxLabelValBytes {
			v.add(field+"["+k+"]", "is longer than %d bytes", maxLabelValBytes)
		}
	}
}

func (v *validator) annotations(field string, annotations map[string]string) {
	if len(annotations) > gds.MaxAnnotations {
		v.add(field, "has %d entries; at most %d are allowed", len(annotations), gds.MaxAnnotations)
	}
	size := 0
	for k, val := range annotations {
		if k == "" {
			v.add(field, "has an empty key")
		}
		size += len(k) + len(val)
	}
	if size > gds.MaxAnnotationBytes {
		v.add(field, "has %d bytes; at most %d are allowed", size, gds.MaxAnnotationBytes)
	}
}

func (v *validator) selector(field string, s *gdsv1.LabelSelector) {
	v.labels(field+".match_labels", s.GetMatchLabels())
}

func (v *validator) port(field string, port int32, allowZero bool) {
	if (port == 0 && !allowZero) || port < 0 || port > 65535 {
		v.add(field, "%d is not a port number", port)
	}
}

func (v *validator) protocol(field string, p gdsv1.Protocol) {
	if _, ok := gdsv1.Protocol_name[int32(p)]; !ok {
		v.add(field, "unknown protocol %d", p)
	}
}

// leaseTTL validates a requested lease TTL and returns the clamped value.
func (v *validator) leaseTTL(field string, d *durationpb.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	if err := d.CheckValid(); err != nil {
		v.add(field, "%v", err)
		return def
	}
	if d.AsDuration() < 0 {
		v.add(field, "must not be negative")
		return def
	}
	return registry.ClampLeaseTTL(d.AsDuration(), def)
}

func (v *validator) exportSpec(field string, s *gdsv1.ServiceExportSpec) {
	if s == nil {
		v.add(field, "is required")
		return
	}
	v.labels(field+".labels", s.GetLabels())
	v.annotations(field+".annotations", s.GetAnnotations())
	switch s.GetType() {
	case gdsv1.ServiceType_SERVICE_TYPE_CLUSTER_SET_IP, gdsv1.ServiceType_SERVICE_TYPE_HEADLESS:
	default:
		v.add(field+".type", "must be SERVICE_TYPE_CLUSTER_SET_IP or SERVICE_TYPE_HEADLESS")
	}
	if len(s.GetPorts()) > gds.MaxPorts {
		v.add(field+".ports", "has %d entries; at most %d are allowed", len(s.GetPorts()), gds.MaxPorts)
	}
	for i, p := range s.GetPorts() {
		f := fmt.Sprintf("%s.ports[%d]", field, i)
		v.port(f+".port", p.GetPort(), false)
		v.protocol(f+".protocol", p.GetProtocol())
		v.maxLen(f+".name", p.GetName(), 63)
		v.maxLen(f+".app_protocol", p.GetAppProtocol(), maxNameBytes)
	}
	if _, ok := gdsv1.SessionAffinity_name[int32(s.GetSessionAffinity())]; !ok {
		v.add(field+".session_affinity", "unknown value %d", s.GetSessionAffinity())
	}
	if t := s.GetSessionAffinityConfig().GetClientIp().GetTimeoutSeconds(); t < 0 || t > maxClientIPTimout {
		v.add(field+".session_affinity_config.client_ip.timeout_seconds", "must be between 0 and %d", maxClientIPTimout)
	}
	if len(s.GetIpFamilies()) > maxIPFamilies {
		v.add(field+".ip_families", "has more than %d entries", maxIPFamilies)
	}
	for i, f := range s.GetIpFamilies() {
		if f != gdsv1.IPFamily_IP_FAMILY_IPV4 && f != gdsv1.IPFamily_IP_FAMILY_IPV6 {
			v.add(fmt.Sprintf("%s.ip_families[%d]", field, i), "must be IP_FAMILY_IPV4 or IP_FAMILY_IPV6")
		}
	}
	if _, ok := gdsv1.InternalTrafficPolicy_name[int32(s.GetInternalTrafficPolicy())]; !ok {
		v.add(field+".internal_traffic_policy", "unknown value %d", s.GetInternalTrafficPolicy())
	}
	v.maxLen(field+".traffic_distribution", s.GetTrafficDistribution(), 63)
	if ts := s.GetExportCreationTimestamp(); ts != nil {
		if err := ts.CheckValid(); err != nil {
			v.add(field+".export_creation_timestamp", "%v", err)
		}
	}
}

func (v *validator) endpoint(field string, e *gdsv1.Endpoint) {
	if n := len(e.GetAddresses()); n == 0 || n > maxAddresses {
		v.add(field+".addresses", "must have 1 to %d entries", maxAddresses)
	}
	for i, a := range e.GetAddresses() {
		if a == "" || len(a) > maxNameBytes {
			v.add(fmt.Sprintf("%s.addresses[%d]", field, i), "must have 1 to %d bytes", maxNameBytes)
		}
	}
	if _, ok := gdsv1.AddressType_name[int32(e.GetAddressType())]; !ok {
		v.add(field+".address_type", "unknown value %d", e.GetAddressType())
	}
	if len(e.GetAttributes()) > gds.MaxEndpointAttributes {
		v.add(field+".attributes", "has %d entries; at most %d are allowed", len(e.GetAttributes()), gds.MaxEndpointAttributes)
	}
	if len(e.GetPorts()) > gds.MaxPorts {
		v.add(field+".ports", "has more than %d entries", gds.MaxPorts)
	}
	for i, p := range e.GetPorts() {
		f := fmt.Sprintf("%s.ports[%d]", field, i)
		v.port(f+".port", p.GetPort(), true)
		v.protocol(f+".protocol", p.GetProtocol())
		v.maxLen(f+".name", p.GetName(), 63)
	}
	v.maxLen(field+".hostname", e.GetHostname(), maxNameBytes)
	v.maxLen(field+".node_name", e.GetNodeName(), maxNameBytes)
	v.maxLen(field+".zone", e.GetZone(), maxNameBytes)
}

func (v *validator) serviceRef(field string, r *gdsv1.ServiceRef) {
	v.dnsLabel(field+".namespace", r.GetNamespace())
	v.dnsLabel(field+".name", r.GetName())
	if r.GetRole() != gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI && r.GetRole() != gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY {
		v.add(field+".role", "must be FRONTEND_ROLE_OPENAI or FRONTEND_ROLE_KV_RELAY")
	}
}

func (v *validator) poolStateDesired(field string, s gdsv1.PoolState) {
	switch s {
	case gdsv1.PoolState_POOL_STATE_UNSPECIFIED, gdsv1.PoolState_POOL_STATE_ACTIVE, gdsv1.PoolState_POOL_STATE_DRAINING:
	default:
		v.add(field, "must be POOL_STATE_ACTIVE or POOL_STATE_DRAINING")
	}
}

// poolSpec validates the writer-owned fields of a pool.
func (v *validator) poolSpec(field string, p *gdsv1.Pool) {
	v.dnsLabel(field+".name", p.GetName())
	v.dnsLabel(field+".location.cluster", p.GetLocation().GetCluster())
	v.maxLen(field+".location.region", p.GetLocation().GetRegion(), 63)
	v.maxLen(field+".location.dc", p.GetLocation().GetDc(), 63)
	if len(p.GetLocation().GetZones()) > maxZones {
		v.add(field+".location.zones", "has more than %d entries", maxZones)
	}
	v.maxLen(field+".hardware_class", p.GetHardwareClass(), 63)
	if len(p.GetModels()) > gds.MaxModelsPerPool {
		v.add(field+".models", "has %d entries; at most %d are allowed", len(p.GetModels()), gds.MaxModelsPerPool)
	}
	for i, m := range p.GetModels() {
		f := fmt.Sprintf("%s.models[%d]", field, i)
		if m.GetModelId() == "" || len(m.GetModelId()) > maxNameBytes {
			v.add(f+".model_id", "must have 1 to %d bytes", maxNameBytes)
		}
		if len(m.GetAliases()) > maxAliases {
			v.add(f+".aliases", "has more than %d entries", maxAliases)
		}
		if len(m.GetAttributes()) > maxModelAttrs {
			v.add(f+".attributes", "has more than %d entries", maxModelAttrs)
		}
	}
	if p.GetWorkerCount() < 0 {
		v.add(field+".worker_count", "must not be negative")
	}
	if p.GetReadyWorkerCount() < 0 {
		v.add(field+".ready_worker_count", "must not be negative")
	}
	if len(p.GetFrontends()) > maxFrontends {
		v.add(field+".frontends", "has more than %d entries", maxFrontends)
	}
	for i, fe := range p.GetFrontends() {
		f := fmt.Sprintf("%s.frontends[%d]", field, i)
		if fe.GetRole() != gdsv1.FrontendRole_FRONTEND_ROLE_OPENAI && fe.GetRole() != gdsv1.FrontendRole_FRONTEND_ROLE_KV_RELAY {
			v.add(f+".role", "must be FRONTEND_ROLE_OPENAI or FRONTEND_ROLE_KV_RELAY")
		}
		switch {
		case (fe.GetUrl() == "") == (fe.GetService() == nil):
			v.add(f, "exactly one of url and service must be set")
		case fe.GetUrl() != "":
			if u, err := url.Parse(fe.GetUrl()); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				v.add(f+".url", "%q is not an http or https URL", fe.GetUrl())
			}
		default:
			v.serviceRef(f+".service", fe.GetService())
		}
		v.maxLen(f+".port_name", fe.GetPortName(), 63)
	}
	if len(p.GetServices()) > maxServices {
		v.add(field+".services", "has more than %d entries", maxServices)
	}
	for i, s := range p.GetServices() {
		v.serviceRef(fmt.Sprintf("%s.services[%d]", field, i), s)
	}
	v.labels(field+".labels", p.GetLabels())
	v.poolStateDesired(field+".desired_state", p.GetDesiredState())
	v.maxLen(field+".owner_id", p.GetOwnerId(), maxNameBytes)
}

// addressType infers the address type of an endpoint that names none.
func addressType(addresses []string) gdsv1.AddressType {
	if len(addresses) == 0 {
		return gdsv1.AddressType_ADDRESS_TYPE_UNSPECIFIED
	}
	ip := net.ParseIP(addresses[0])
	switch {
	case ip == nil:
		return gdsv1.AddressType_ADDRESS_TYPE_FQDN
	case ip.To4() != nil:
		return gdsv1.AddressType_ADDRESS_TYPE_IPV4
	}
	return gdsv1.AddressType_ADDRESS_TYPE_IPV6
}
