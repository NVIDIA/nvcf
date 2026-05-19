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

package controlplaneprofile

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	APIVersion = "nvcf.nvidia.com/v1alpha1"
	Kind       = "ControlPlaneProfile"
)

type ControlPlaneProfile struct {
	APIVersion   string       `yaml:"apiVersion"`
	Kind         string       `yaml:"kind"`
	ControlPlane ControlPlane `yaml:"controlPlane"`
}

func WriteFile(path string, doc ControlPlaneProfile) error {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal control-plane profile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

type ControlPlane struct {
	ClusterName string    `yaml:"clusterName"`
	NCAID       string    `yaml:"ncaID"`
	Region      string    `yaml:"region"`
	Endpoints   Endpoints `yaml:"endpoints"`
	Gateway     Gateway   `yaml:"gateway"`
	Hosts       Hosts     `yaml:"hosts"`
}

type Endpoints struct {
	InCluster        EndpointScope `yaml:"inCluster"`
	ComputeReachable EndpointScope `yaml:"computeReachable"`
}

type EndpointScope struct {
	ICMSURL  string `yaml:"icmsURL"`
	ReValURL string `yaml:"revalURL"`
	NATSURL  string `yaml:"natsURL"`
}

type Gateway struct {
	HTTPURL string `yaml:"httpURL"`
	GRPCURL string `yaml:"grpcURL"`
}

type Hosts struct {
	API        string `yaml:"api"`
	APIKeys    string `yaml:"apiKeys"`
	SIS        string `yaml:"sis"`
	ReVal      string `yaml:"reval"`
	NATS       string `yaml:"nats"`
	Invocation string `yaml:"invocation"`
}

type RequireMode string

const (
	RequireAny              RequireMode = "any"
	RequireInCluster        RequireMode = "in-cluster"
	RequireComputeReachable RequireMode = "compute-reachable"
	RequireBoth             RequireMode = "both"
)

type ValidateOptions struct {
	Require RequireMode
}

type ValidationResult struct {
	Profile                ControlPlaneProfile
	InClusterUsable        bool
	ComputeReachableUsable bool
}

type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 0 {
		return "invalid control-plane profile"
	}
	var b strings.Builder
	b.WriteString("invalid control-plane profile:")
	for _, problem := range e.Problems {
		b.WriteString("\n- ")
		b.WriteString(problem)
	}
	return b.String()
}

func ParseAndValidate(data []byte, opts ValidateOptions) (*ValidationResult, error) {
	var problems []string
	if strings.TrimSpace(string(data)) == "" {
		return nil, &ValidationError{Problems: []string{"file: required"}}
	}

	var doc ControlPlaneProfile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&doc); err != nil {
		problems = append(problems, fmt.Sprintf("schema: %v", err))
	}

	require := normalizeRequireMode(opts.Require)
	v := validator{doc: doc}
	v.validateTopLevel()
	v.validateGateway()
	inClusterUsable := v.validateEndpointScope("controlPlane.endpoints.inCluster", doc.ControlPlane.Endpoints.InCluster, endpointScopeRequired(require, RequireInCluster))
	computeReachableUsable := v.validateEndpointScope("controlPlane.endpoints.computeReachable", doc.ControlPlane.Endpoints.ComputeReachable, endpointScopeRequired(require, RequireComputeReachable))
	v.validateHosts()
	v.validateNoDNSHostHeaders()
	if require == RequireAny && !inClusterUsable && !computeReachableUsable {
		v.add("controlPlane.endpoints", "at least one endpoint scope is required")
	}
	problems = append(problems, v.problems...)
	if len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	return &ValidationResult{
		Profile:                doc,
		InClusterUsable:        inClusterUsable,
		ComputeReachableUsable: computeReachableUsable,
	}, nil
}

func (r *ValidationResult) Summary() string {
	status := func(ok bool) string {
		if ok {
			return "usable"
		}
		return "not present"
	}
	return fmt.Sprintf("endpoint scopes:\n  in-cluster: %s\n  compute-reachable: %s", status(r.InClusterUsable), status(r.ComputeReachableUsable))
}

func normalizeRequireMode(mode RequireMode) RequireMode {
	if mode == "" {
		return RequireAny
	}
	return mode
}

type validator struct {
	doc      ControlPlaneProfile
	problems []string
}

func (v *validator) add(field, message string) {
	v.problems = append(v.problems, fmt.Sprintf("%s: %s", field, message))
}

func (v *validator) validateTopLevel() {
	if v.doc.APIVersion == "" {
		v.add("apiVersion", "required")
	} else if v.doc.APIVersion != APIVersion {
		v.add("apiVersion", fmt.Sprintf("must be %q", APIVersion))
	}
	if v.doc.Kind == "" {
		v.add("kind", "required")
	} else if v.doc.Kind != Kind {
		v.add("kind", fmt.Sprintf("must be %q", Kind))
	}
	cp := v.doc.ControlPlane
	requireString(v.add, "controlPlane.clusterName", cp.ClusterName)
	requireString(v.add, "controlPlane.ncaID", cp.NCAID)
	requireString(v.add, "controlPlane.region", cp.Region)
}

func (v *validator) validateGateway() {
	validateHTTPURL(v.add, "controlPlane.gateway.httpURL", v.doc.ControlPlane.Gateway.HTTPURL, true)
	validateGRPCAddress(v.add, "controlPlane.gateway.grpcURL", v.doc.ControlPlane.Gateway.GRPCURL, true)
}

func (v *validator) validateEndpointScope(field string, scope EndpointScope, required bool) bool {
	present := scope.ICMSURL != "" || scope.ReValURL != "" || scope.NATSURL != ""
	if !present && !required {
		return false
	}
	requireFields := required || present
	before := len(v.problems)
	validateHTTPURL(v.add, field+".icmsURL", scope.ICMSURL, requireFields)
	validateHTTPURL(v.add, field+".revalURL", scope.ReValURL, requireFields)
	validateNATSURL(v.add, field+".natsURL", scope.NATSURL, requireFields)
	return len(v.problems) == before && scope.ICMSURL != "" && scope.ReValURL != "" && scope.NATSURL != ""
}

func (v *validator) validateHosts() {
	hosts := v.doc.ControlPlane.Hosts
	validateHost(v.add, "controlPlane.hosts.api", hosts.API, false)
	validateHost(v.add, "controlPlane.hosts.apiKeys", hosts.APIKeys, false)
	validateHost(v.add, "controlPlane.hosts.sis", hosts.SIS, false)
	validateHost(v.add, "controlPlane.hosts.reval", hosts.ReVal, false)
	validateHost(v.add, "controlPlane.hosts.nats", hosts.NATS, false)
	validateHost(v.add, "controlPlane.hosts.invocation", hosts.Invocation, false)
}

func (v *validator) validateNoDNSHostHeaders() {
	cp := v.doc.ControlPlane
	requireHostForGatewayURL(v.add, "controlPlane.gateway.httpURL", cp.Gateway.HTTPURL, "controlPlane.hosts.api", cp.Hosts.API)
	requireHostForGatewayURL(v.add, "controlPlane.endpoints.computeReachable.icmsURL", cp.Endpoints.ComputeReachable.ICMSURL, "controlPlane.hosts.sis", cp.Hosts.SIS)
	requireHostForGatewayURL(v.add, "controlPlane.endpoints.computeReachable.revalURL", cp.Endpoints.ComputeReachable.ReValURL, "controlPlane.hosts.reval", cp.Hosts.ReVal)
}

func endpointScopeRequired(mode RequireMode, scope RequireMode) bool {
	return mode == scope || mode == RequireBoth
}

func requireString(add func(string, string), field, value string) {
	if strings.TrimSpace(value) == "" {
		add(field, "required")
	}
}

func validateHTTPURL(add func(string, string), field, value string, required bool) {
	if strings.TrimSpace(value) == "" {
		if required {
			add(field, "required")
		}
		return
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		add(field, "must be an absolute http or https URL")
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		add(field, "scheme must be http or https")
	}
}

func validateNATSURL(add func(string, string), field, value string, required bool) {
	if strings.TrimSpace(value) == "" {
		if required {
			add(field, "required")
		}
		return
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		add(field, "must be an absolute nats or tls URL")
		return
	}
	if u.Scheme != "nats" && u.Scheme != "tls" {
		add(field, "scheme must be nats or tls")
	}
}

func validateGRPCAddress(add func(string, string), field, value string, required bool) {
	if strings.TrimSpace(value) == "" {
		if required {
			add(field, "required")
		}
		return
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" {
			add(field, "must be a host:port address or absolute URL")
			return
		}
		switch u.Scheme {
		case "grpc", "grpcs", "https":
		default:
			add(field, "scheme must be grpc, grpcs, or https")
		}
		return
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		add(field, "must be a host:port address")
	}
}

func validateHost(add func(string, string), field, value string, required bool) {
	if strings.TrimSpace(value) == "" {
		if required {
			add(field, "required")
		}
		return
	}
	if strings.Contains(value, "://") || strings.ContainsAny(value, " \t\r\n") {
		add(field, "must be a hostname or Host header value, not a URL")
	}
}

func requireHostForGatewayURL(add func(string, string), urlField, rawURL, hostField, hostValue string) {
	if rawURL == "" {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return
	}
	if isNoDNSGatewayHost(u.Hostname()) && strings.TrimSpace(hostValue) == "" {
		add(hostField, fmt.Sprintf("required as Host header when %s uses a gateway address", urlField))
	}
}

func isNoDNSGatewayHost(host string) bool {
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return strings.EqualFold(host, "localhost")
}
