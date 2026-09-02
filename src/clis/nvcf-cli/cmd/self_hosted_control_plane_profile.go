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
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

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
	StackDomain         string
	ControlPlaneID      string
	SourceRootCA        bool
	RootCAPEM           string
}

func writeControlPlaneProfile(req controlPlaneProfileWriteRequest) (string, error) {
	if req.StackDomain == "" || req.ControlPlaneID == "" {
		settings, err := loadControlPlaneStackProfileSettings(req.StackPath, req.Env)
		if err != nil {
			return "", err
		}
		if req.StackDomain == "" {
			req.StackDomain = settings.Domain
		}
		if req.ControlPlaneID == "" {
			req.ControlPlaneID = settings.ControlPlaneID
		}
	}
	doc := buildControlPlaneProfile(req)
	rootCAPEM := strings.TrimSpace(req.RootCAPEM)
	if rootCAPEM == "" && req.SourceRootCA {
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		var err error
		rootCAPEM, err = fetchControlPlaneRootCAPEM(ctx, req.ControlPlaneContext, req.ControlPlaneID)
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

var fetchControlPlaneRootCAPEM = func(ctx context.Context, kctx, controlPlaneID string) (string, error) {
	cfg := controlPlaneRootCAOpenBaoConfig(kctx, controlPlaneID)
	pem, err := openbao.NewClient(cfg, nil).ReadPKICertificatePEM(ctx, controlPlaneRootPKIPath())
	if errors.Is(err, openbao.ErrPKICertificateNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("sourcing control-plane root CA from OpenBao: %w", err)
	}
	return strings.TrimSpace(pem), nil
}

func controlPlaneRootCAOpenBaoConfig(kctx, controlPlaneID string) *openbao.Config {
	prefix := ""
	if id := strings.TrimSpace(controlPlaneID); id != "" {
		prefix = id + "-"
	}
	vaultNamespace := prefix + "vault-system"
	return &openbao.Config{
		OpenBaoURL: defaultString(firstNonEmptyEnv("NVCF_OPENBAO_URL", "OPENBAO_URL", "VAULT_ADDR", "BAO_ADDR"),
			fmt.Sprintf("http://%sopenbao-server.%s.svc.cluster.local:8200", prefix, vaultNamespace)),
		OpenBaoNamespace:  defaultString(os.Getenv("NVCF_OPENBAO_NAMESPACE"), vaultNamespace),
		OpenBaoSecretName: defaultString(os.Getenv("NVCF_OPENBAO_SECRET_NAME"), prefix+"openbao-server-root-token"),
		KubeContext:       kctx,
		ClusterNamespace:  defaultString(os.Getenv("NVCF_CLUSTER_NAMESPACE"), prefix+"nvcf"),
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
	icmsURL := resolveProfileICMSURL(req.ICMSURL, req.Env, req.StackDomain)
	computeEndpoints := resolveRegisterEndpointValues(req.Env, req.ControlPlaneContext, req.ComputePlaneContext, icmsURL, req.NATSURL)
	gatewayHTTP := resolveProfileGatewayHTTPURL(req.Env, icmsURL)
	gatewayGRPC := resolveProfileGatewayGRPCURL(req.Env, req.StackDomain)
	apiHost := firstNonEmpty(os.Getenv("API_HOST"), viper.GetString("api_host"), hostnameFromURL(gatewayHTTP))
	domain := domainFromHost(apiHost)
	apiKeysHost := firstNonEmpty(os.Getenv("API_KEYS_HOST"), viper.GetString("api_keys_host"), "api-keys."+domain)
	invocationHost := firstNonEmpty(os.Getenv("INVOKE_HOST"), viper.GetString("invoke_host"), "invocation."+domain)
	sisHost := firstNonEmpty(os.Getenv("NVCF_ICMS_HOST"), viper.GetString("icms_host"), "sis."+domain)
	revalHost := firstNonEmpty(os.Getenv("NVCF_REVAL_HOST"), viper.GetString("reval_host"), "reval."+domain)
	natsHost := firstNonEmpty(os.Getenv("NVCF_NATS_HOST"), viper.GetString("nats_host"), "nats."+domain)
	inClusterEndpoints := profileInClusterEndpointScope(req.ControlPlaneID)
	if strings.EqualFold(req.Env, "local") {
		computeEndpoints.ICMSServiceURL = rewriteURLHost(computeEndpoints.ICMSServiceURL, sisHost)
		computeEndpoints.ReValServiceURL = rewriteURLHost(computeEndpoints.ReValServiceURL, revalHost)
		computeEndpoints.NATSURL = rewriteURLHost(computeEndpoints.NATSURL, natsHost)
	}

	return controlplaneprofile.ControlPlaneProfile{
		APIVersion: controlplaneprofile.APIVersion,
		Kind:       controlplaneprofile.Kind,
		ControlPlane: controlplaneprofile.ControlPlane{
			ClusterName: defaultString(req.ClusterName, "control-plane"),
			NCAID:       defaultString(req.NCAID, "nvcf-default"),
			Region:      defaultString(req.Region, "us-west-1"),
			Endpoints: controlplaneprofile.Endpoints{
				InCluster: inClusterEndpoints,
				ComputeReachable: controlplaneprofile.EndpointScope{
					ICMSURL:  computeEndpoints.ICMSServiceURL,
					ReValURL: computeEndpoints.ReValServiceURL,
					NATSURL:  computeEndpoints.NATSURL,
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

func profileInClusterEndpointScope(controlPlaneID string) controlplaneprofile.EndpointScope {
	prefix := ""
	if id := strings.TrimSpace(controlPlaneID); id != "" {
		prefix = id + "-"
	}
	return controlplaneprofile.EndpointScope{
		ICMSURL:  fmt.Sprintf("http://api.%ssis.svc.cluster.local:8080", prefix),
		ReValURL: fmt.Sprintf("http://reval.%snvcf.svc.cluster.local:8080", prefix),
		NATSURL:  fmt.Sprintf("nats://nats.%snats-system.svc.cluster.local:4222", prefix),
	}
}

type controlPlaneStackProfileSettings struct {
	Domain         string
	ControlPlaneID string
}

func loadControlPlaneStackProfileSettings(stackPath, env string) (controlPlaneStackProfileSettings, error) {
	if stackPath == "" {
		return controlPlaneStackProfileSettings{}, nil
	}
	settings := controlPlaneStackProfileSettings{}
	for _, name := range []string{"base.yaml", env + ".yaml"} {
		path := filepath.Join(stackPath, "environments", name)
		body, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return controlPlaneStackProfileSettings{}, fmt.Errorf("reading control-plane stack values %q: %w", path, err)
		}
		var values struct {
			Global struct {
				Domain       string `yaml:"domain"`
				ControlPlane struct {
					ID string `yaml:"id"`
				} `yaml:"controlPlane"`
			} `yaml:"global"`
		}
		if err := yaml.Unmarshal(body, &values); err != nil {
			return controlPlaneStackProfileSettings{}, fmt.Errorf("parsing control-plane stack values %q: %w", path, err)
		}
		if value := strings.TrimSpace(values.Global.Domain); value != "" {
			settings.Domain = value
		}
		if value := strings.TrimSpace(values.Global.ControlPlane.ID); value != "" {
			settings.ControlPlaneID = value
		}
	}
	return settings, nil
}

func loadControlPlaneStackDomain(stackPath, env string) (string, error) {
	settings, err := loadControlPlaneStackProfileSettings(stackPath, env)
	return settings.Domain, err
}

func resolveProfileICMSURL(flagValue, env, stackDomain string) string {
	if flagValue != "" {
		return flagValue
	}
	if value := os.Getenv("NVCF_ICMS_URL"); value != "" {
		return value
	}
	if value := os.Getenv("NVCF_SIS_URL"); value != "" {
		return value
	}
	if cfg, err := client.LoadConfigWithoutAuth(); err == nil {
		if cfg.ICMSURL != "" {
			return cfg.ICMSURL
		}
		if viper.IsSet("base_http_url") && cfg.BaseHTTPURL != "" {
			if derived, ok := deriveICMSFromAPI(cfg.BaseHTTPURL); ok {
				return derived
			}
			return cfg.BaseHTTPURL
		}
	}
	if stackDomain != "" {
		return profileHTTPServiceURL(env, "sis", stackDomain)
	}
	if strings.EqualFold(env, "local") {
		return "http://sis.localhost:8080"
	}
	return resolveICMSURL("")
}

// rewriteURLHost replaces the hostname (preserving scheme, port, path, and
// query) of rawURL with newHost. Returns rawURL unchanged when it cannot be
// parsed, has no host, or when newHost is empty.
//
// Local split-cluster resolution can produce cross-cluster service names that
// must be normalized to the local gateway routing hosts before export.
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
	if cfg, err := client.LoadConfigWithoutAuth(); err == nil && viper.IsSet("base_http_url") && cfg.BaseHTTPURL != "" {
		return cfg.BaseHTTPURL
	}
	if icmsURL != "" {
		return deriveSiblingHTTPServiceURL(icmsURL, "api")
	}
	return "http://api.localhost:8080"
}

func resolveProfileGatewayGRPCURL(env, stackDomain string) string {
	if v := os.Getenv("NVCF_BASE_GRPC_URL"); v != "" {
		return v
	}
	if v := os.Getenv("NVCF_GRPC_URL"); v != "" {
		return v
	}
	if cfg, err := client.LoadConfigWithoutAuth(); err == nil && (viper.IsSet("grpc_url") || viper.IsSet("base_grpc_url")) && cfg.BaseGRPCURL != "" {
		return cfg.BaseGRPCURL
	}
	if stackDomain != "" {
		port := "443"
		if strings.EqualFold(env, "local") {
			port = "10081"
		}
		return net.JoinHostPort("grpc."+stackDomain, port)
	}
	if !strings.EqualFold(env, "local") {
		if cfg, err := client.LoadConfigWithoutAuth(); err == nil && cfg.BaseGRPCURL != "" {
			return cfg.BaseGRPCURL
		}
	}
	return "grpc.localhost:10081"
}

func profileHTTPServiceURL(env, service, domain string) string {
	scheme := "https"
	host := service + "." + domain
	if strings.EqualFold(env, "local") {
		scheme = "http"
		host = net.JoinHostPort(host, "8080")
	}
	return (&url.URL{Scheme: scheme, Host: host}).String()
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
