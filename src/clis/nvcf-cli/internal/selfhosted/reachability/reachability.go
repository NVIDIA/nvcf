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

package reachability

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type CheckRequest struct {
	TargetClusterName string
	GatewayHTTPURL    string
	ICMSURL           string
	ReValURL          string
	NATSURL           string
	SISHost           string
	ReValHost         string
	NATSHost          string
	HTTPClient        *http.Client
	ProbeHTTP         bool
}

type CheckError struct {
	TargetClusterName string
	Problems          []string
}

func (e *CheckError) Error() string {
	summary := "compute-plane reachability check failed"
	if strings.TrimSpace(e.TargetClusterName) != "" {
		summary = fmt.Sprintf("%s for cluster %q", summary, e.TargetClusterName)
	}
	if len(e.Problems) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(summary)
	b.WriteString(":")
	for _, problem := range e.Problems {
		b.WriteString("\n- ")
		b.WriteString(problem)
	}
	return b.String()
}

func Check(ctx context.Context, req CheckRequest) error {
	var problems []string
	problems = append(problems, validateHTTPService("controlPlane.endpoints.computeReachable.icmsURL", req.ICMSURL, req.GatewayHTTPURL, "controlPlane.hosts.sis", req.SISHost)...)
	problems = append(problems, validateHTTPService("controlPlane.endpoints.computeReachable.revalURL", req.ReValURL, req.GatewayHTTPURL, "controlPlane.hosts.reval", req.ReValHost)...)
	problems = append(problems, validateNATSServiceShape(req.NATSURL, req.GatewayHTTPURL, req.NATSHost)...)
	if len(problems) == 0 && req.ProbeHTTP {
		client := req.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 10 * time.Second}
		}
		probeClient := *client
		probeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		if err := probeHTTP(ctx, &probeClient, req.ICMSURL, "/health", req.SISHost, "controlPlane.hosts.sis"); err != nil {
			problems = append(problems, fmt.Sprintf("controlPlane.endpoints.computeReachable.icmsURL: %v", err))
		}
		if err := probeHTTP(ctx, &probeClient, req.ReValURL, "/info", req.ReValHost, "controlPlane.hosts.reval"); err != nil {
			problems = append(problems, fmt.Sprintf("controlPlane.endpoints.computeReachable.revalURL: %v", err))
		}
	}
	if len(problems) > 0 {
		return &CheckError{TargetClusterName: req.TargetClusterName, Problems: problems}
	}
	return nil
}

func validateHTTPService(urlField, rawURL, gatewayHTTPURL, hostField, hostHeader string) []string {
	var problems []string
	if strings.TrimSpace(rawURL) == "" {
		return []string{urlField + ": required"}
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []string{urlField + ": must be an absolute http or https URL"}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		problems = append(problems, urlField+": scheme must be http or https")
	}
	if isGatewayAddress(u.Hostname(), gatewayHTTPURL) && strings.TrimSpace(hostHeader) == "" {
		problems = append(problems, fmt.Sprintf("%s: required when %s uses a shared gateway address; set %s to the service Host header", hostField, urlField, hostField))
	}
	return problems
}

func validateNATSServiceShape(rawURL, gatewayHTTPURL, hostHeader string) []string {
	if strings.TrimSpace(rawURL) == "" {
		return []string{"controlPlane.endpoints.computeReachable.natsURL: required"}
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []string{"controlPlane.endpoints.computeReachable.natsURL: must be an absolute nats or tls URL; TCP reachability is not probed"}
	}
	if u.Scheme != "nats" && u.Scheme != "tls" {
		return []string{"controlPlane.endpoints.computeReachable.natsURL: scheme must be nats or tls; TCP reachability is not probed"}
	}
	if isGatewayAddress(u.Hostname(), gatewayHTTPURL) && strings.TrimSpace(hostHeader) == "" {
		return []string{"controlPlane.hosts.nats: required when controlPlane.endpoints.computeReachable.natsURL uses a shared gateway address; set controlPlane.hosts.nats to the service Host header"}
	}
	return nil
}

func probeHTTP(ctx context.Context, client *http.Client, rawURL, probePath, hostHeader, hostField string) error {
	probeURL, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	probeURL.Path = strings.TrimRight(probeURL.Path, "/") + probePath
	probeURL.RawPath = ""
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL.String(), nil)
	if err != nil {
		return err
	}
	if hostHeader != "" {
		httpReq.Host = hostHeader
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if (resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices) && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("status %d from %s; verify the endpoint routes %s to the service and set %s to the required Host header when using a shared gateway", resp.StatusCode, probePath, probePath, hostField)
	}
	return nil
}

func isGatewayAddress(host, gatewayHTTPURL string) bool {
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	gateway, err := url.Parse(gatewayHTTPURL)
	return err == nil && gateway.Hostname() != "" && strings.EqualFold(host, gateway.Hostname())
}
