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
	"net/url"
	"os"
	"path/filepath"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted/controlplaneprofile"
)

const controlPlaneProfileFileName = "control-plane-profile.yaml"

type controlPlaneProfileWriteRequest struct {
	StackPath           string
	ClusterName         string
	NCAID               string
	Region              string
	Env                 string
	ControlPlaneContext string
	ComputePlaneContext string
	ICMSURL             string
	NATSURL             string
}

func writeControlPlaneProfile(req controlPlaneProfileWriteRequest) (string, error) {
	doc := buildControlPlaneProfile(req)
	path := controlPlaneProfilePath(req.StackPath)
	if err := controlplaneprofile.WriteFile(path, doc); err != nil {
		return "", err
	}
	return path, nil
}

func controlPlaneProfilePath(stackPath string) string {
	return filepath.Join(stackPath, "out", controlPlaneProfileFileName)
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
	apiKeysHost := firstNonEmpty(os.Getenv("API_KEYS_HOST"), "api-keys."+domainFromHost(apiHost))
	invocationHost := firstNonEmpty(os.Getenv("INVOKE_HOST"), "invocation."+domainFromHost(apiHost))

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
				SIS:        firstNonEmpty(os.Getenv("NVCF_ICMS_HOST"), hostnameFromURL(computeEndpoints.ICMSServiceURL)),
				ReVal:      firstNonEmpty(os.Getenv("NVCF_REVAL_HOST"), hostnameFromURL(computeEndpoints.ReValServiceURL)),
				NATS:       firstNonEmpty(os.Getenv("NVCF_NATS_HOST"), hostnameFromURL(computeEndpoints.NATSURL)),
				Invocation: invocationHost,
			},
		},
	}
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
