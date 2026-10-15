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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/openbao"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
	"nvcf-cli/internal/trustbundle"
)

const controlPlaneProfileFileName = "control-plane-profile.yaml"
const defaultControlPlaneRootPKIPath = "services/all/pki/root"

type controlPlaneProfileWriteRequest struct {
	Ctx                 context.Context
	StackPath           string
	ClusterName         string
	NCAID               string
	Region              string
	Env                 string
	ControlPlaneContext string
	ComputePlaneContext string
	ICMSURL             string
	NATSURL             string
	SourceRootCA        bool
	RootCAPEM           string
}

func writeControlPlaneProfile(req controlPlaneProfileWriteRequest) (string, error) {
	doc := buildControlPlaneProfile(req)
	rootCAPEM := strings.TrimSpace(req.RootCAPEM)
	if rootCAPEM == "" && req.SourceRootCA {
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		var err error
		rootCAPEM, err = fetchControlPlaneRootCAPEM(ctx, req.ControlPlaneContext)
		if err != nil {
			return "", err
		}
	}
	if rootCAPEM != "" {
		if err := applyControlPlaneRootCATrust(&doc, rootCAPEM); err != nil {
			return "", err
		}
	}
	path := controlPlaneProfilePath(req.StackPath)
	if err := controlplaneprofile.WriteFile(path, doc); err != nil {
		return "", err
	}
	return path, nil
}

func controlPlaneProfilePath(stackPath string) string {
	return filepath.Join(stackPath, "out", controlPlaneProfileFileName)
}

var fetchControlPlaneRootCAPEM = func(ctx context.Context, kctx string) (string, error) {
	cfg := controlPlaneRootCAOpenBaoConfig(kctx)
	pem, err := openbao.NewClient(cfg, nil).ReadPKICertificatePEM(ctx, controlPlaneRootPKIPath())
	if errors.Is(err, openbao.ErrPKICertificateNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sourcing control-plane root CA from OpenBao: %w", err)
	}
	return strings.TrimSpace(pem), nil
}

func controlPlaneRootCAOpenBaoConfig(kctx string) *openbao.Config {
	return &openbao.Config{
		OpenBaoURL:        defaultString(firstNonEmptyEnv("NVCF_OPENBAO_URL", "OPENBAO_URL", "VAULT_ADDR", "BAO_ADDR"), "http://openbao-server.vault-system.svc.cluster.local:8200"),
		OpenBaoNamespace:  defaultString(os.Getenv("NVCF_OPENBAO_NAMESPACE"), "vault-system"),
		OpenBaoSecretName: defaultString(os.Getenv("NVCF_OPENBAO_SECRET_NAME"), "openbao-server-root-token"),
		KubeContext:       kctx,
		ClusterNamespace:  defaultString(os.Getenv("NVCF_CLUSTER_NAMESPACE"), "nvcf"),
		UtilityImage:      defaultString(os.Getenv("NVCF_CLUSTER_UTILITY_IMAGE"), "curlimages/curl:latest"),
	}
}

func controlPlaneRootPKIPath() string {
	return defaultString(os.Getenv("NVCF_OPENBAO_ROOT_PKI_PATH"), defaultControlPlaneRootPKIPath)
}

func applyControlPlaneRootCATrust(doc *controlplaneprofile.ControlPlaneProfile, rootCAPEM string) error {
	fingerprint, err := trustbundle.Fingerprint([]byte(rootCAPEM))
	if err != nil {
		return fmt.Errorf("fingerprinting control-plane root CA bundle: %w", err)
	}
	doc.ManagementTLS = controlplaneprofile.ManagementTLS{
		TrustMode:   controlplaneprofile.TrustModeBundle,
		CABundlePEM: rootCAPEM,
	}
	doc.TransportTLS = controlplaneprofile.TransportTLS{
		TrustMode:              controlplaneprofile.TrustModeBundle,
		TrustBundlePEM:         rootCAPEM,
		TrustBundleFingerprint: fingerprint,
	}
	return nil
}

func buildControlPlaneProfile(req controlPlaneProfileWriteRequest) controlplaneprofile.ControlPlaneProfile {
	icmsURL := req.ICMSURL
	if icmsURL == "" && req.Env == "local" {
		icmsURL = "http://sis.localhost:8080"
	}
	computeEndpoints := resolveRegisterEndpointValues(req.Env, req.ControlPlaneContext, req.ComputePlaneContext, icmsURL, req.NATSURL)
	gatewayHTTP := resolveProfileGatewayHTTPURL(req.Env, icmsURL)
	gatewayGRPC := resolveProfileGatewayGRPCURL(req.Env)
	apiHost := firstNonEmpty(os.Getenv("API_HOST"), hostnameFromURL(gatewayHTTP))
	domain := domainFromHost(apiHost)
	apiKeysHost := firstNonEmpty(os.Getenv("API_KEYS_HOST"), "api-keys."+domain)
	invocationHost := firstNonEmpty(os.Getenv("INVOKE_HOST"), "invocation."+domain)
	sisHost := firstNonEmpty(os.Getenv("NVCF_ICMS_HOST"), "sis."+domain)
	revalHost := firstNonEmpty(os.Getenv("NVCF_REVAL_HOST"), "reval."+domain)
	natsHost := firstNonEmpty(os.Getenv("NVCF_NATS_HOST"), "nats."+domain)

	return controlplaneprofile.ControlPlaneProfile{
		APIVersion: controlplaneprofile.APIVersion,
		Kind:       controlplaneprofile.Kind,
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: defaultString(req.ClusterName, "control-plane"),
			NCAID:       defaultString(req.NCAID, "nvcf-default"),
			Region:      defaultString(req.Region, "us-west-1"),
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: controlplaneprofile.EndpointScope{
					ICMSURL:  "http://api.sis.svc.cluster.local:8080",
					ReValURL: "http://reval.nvcf.svc.cluster.local:8080",
					NATSURL:  "nats://nats.nats-system.svc.cluster.local:4222",
				},
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL:  rewriteURLHost(computeEndpoints.ICMSServiceURL, sisHost),
					ReValURL: rewriteURLHost(computeEndpoints.ReValServiceURL, revalHost),
					NATSURL:  rewriteURLHost(computeEndpoints.NATSURL, natsHost),
				},
			},
			Gateway: controlplaneprofile.Gateway{
				HTTPURL: gatewayHTTP,
				GRPCURL: gatewayGRPC,
			},
			Hosts: controlplaneprofile.Hosts{
				API:        apiHost,
				APIKeys:    apiKeysHost,
				SIS:        sisHost,
				ReVal:      revalHost,
				NATS:       natsHost,
				Invocation: invocationHost,
			},
		},
	}
}

// rewriteURLHost replaces the hostname (preserving scheme, port, path, and
// query) of rawURL with newHost. Returns rawURL unchanged when it cannot be
// parsed, has no host, or when newHost is empty.
//
// This lets buildControlPlaneProfile project bare-ELB URLs back through the
// canonical sis./reval./nats. service prefix that the gateway HTTPRoutes
// match. For local k3d (rawURL already has sis./reval./nats. prefixes equal
// to newHost) this is effectively a no-op.
func rewriteURLHost(rawURL, newHost string) string {
	if rawURL == "" || newHost == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	if port := u.Port(); port != "" {
		u.Host = hostWithOptionalPort(newHost, port)
	} else {
		u.Host = newHost
	}
	return u.String()
}

func resolveProfileGatewayHTTPURL(env, icmsURL string) string {
	if v := os.Getenv("NVCF_BASE_HTTP_URL"); v != "" {
		return v
	}
	if cfg, err := client.LoadConfigWithoutAuth(); err == nil && cfg.BaseHTTPURL != "" && !(env == "local" && cfg.BaseHTTPURL == "https://api.nvcf.nvidia.com") {
		return cfg.BaseHTTPURL
	}
	if icmsURL != "" {
		return deriveSiblingHTTPServiceURL(icmsURL, "api")
	}
	return "http://api.localhost:8080"
}

func resolveProfileGatewayGRPCURL(env string) string {
	if v := os.Getenv("NVCF_BASE_GRPC_URL"); v != "" {
		return v
	}
	if v := os.Getenv("NVCF_GRPC_URL"); v != "" {
		return v
	}
	if cfg, err := client.LoadConfigWithoutAuth(); err == nil && cfg.BaseGRPCURL != "" && !(env == "local" && cfg.BaseGRPCURL == "grpc.nvcf.nvidia.com:443") {
		return cfg.BaseGRPCURL
	}
	return "grpc.localhost:10081"
}

func hostnameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func domainFromHost(host string) string {
	if host == "" {
		return "localhost"
	}
	if domain, ok := controlPlaneSiblingDomain(host); ok {
		return domain
	}
	return host
}
