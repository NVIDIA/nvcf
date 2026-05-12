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
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"nvcf-cli/internal/client"
	"nvcf-cli/internal/selfhosted/kubectx"
)

var selfHostedCmd = &cobra.Command{
	Use:   "self-hosted",
	Short: "Manage self-hosted NVCF installations",
	Long: `Install, check, and manage self-hosted NVCF deployments.

Modeled on linkerd's CLI. Use 'self-hosted up --cluster-name=X' for the
one-shot install, or compose 'install --control-plane', 'cluster register',
and 'install --compute-plane' explicitly.`,
}

// Persistent flags shared by all self-hosted subcommands.
var (
	selfHostedStack               string
	selfHostedEnv                 string
	selfHostedNoApply             bool
	selfHostedNonInter            bool
	selfHostedToken               string
	selfHostedOutput              string
	selfHostedWait                string
	selfHostedICMSURL             string
	selfHostedNATSURL             string
	selfHostedJSON                bool
	selfHostedPlain               bool
	selfHostedAccessible          bool
	selfHostedRefreshToken        bool
	selfHostedControlPlaneContext string
	selfHostedComputePlaneContext string
)

type registerEndpointValues struct {
	ICMSServiceURL  string
	ReValServiceURL string
	NATSURL         string
}

type localEndpointDefaults struct {
	BaseHTTPURL string
	InvokeURL   string
	APIKeysURL  string
	APIHost     string
	InvokeHost  string
	APIKeysHost string
}

const (
	localControlPlaneDomainDefault = "nvcf-control-plane.test"
	localControlPlaneHTTPPort      = "8080"
	localControlPlaneNATSPort      = "4222"
)

func init() {
	rootCmd.AddCommand(selfHostedCmd)

	selfHostedCmd.PersistentFlags().StringVar(&selfHostedStack, "stack", "",
		"Bundle source: local path, git URL, or oci:// URL (default: built-in OCI URL pinned to this CLI version)")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedEnv, "env", "local",
		"Helmfile environment (e.g. local, prd)")
	selfHostedCmd.PersistentFlags().BoolVar(&selfHostedNoApply, "no-apply", false,
		"Emit YAML to stdout without invoking kubectl (install only)")
	selfHostedCmd.PersistentFlags().BoolVar(&selfHostedNonInter, "non-interactive", false,
		"Disable all prompts; error if required input missing")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedToken, "token", "",
		"Admin JWT (overrides stored session, suppresses init prompt)")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedOutput, "output", "text",
		"Output format for check: text or json")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedWait, "wait", "",
		"Block on check until pass or duration elapses (e.g. 5m)")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedICMSURL, "icms-url", "",
		"ICMS endpoint for cluster register (default: derived from base_http_url; env: NVCF_ICMS_URL)")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedICMSURL, "sis-url", "",
		"Deprecated alias for --icms-url (env: NVCF_SIS_URL)")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedNATSURL, "nats-url", "",
		"NATS endpoint for the compute plane agent (default: derived from ICMS/API URL; env: NVCF_NATS_URL)")
	selfHostedCmd.PersistentFlags().BoolVar(&selfHostedJSON, "json", false,
		"Emit JSONL events on stderr (one per line, schemaVersion: 1)")
	selfHostedCmd.PersistentFlags().BoolVar(&selfHostedPlain, "plain", false,
		"Force plain streaming output (RFC3339-prefixed lines)")
	selfHostedCmd.PersistentFlags().BoolVar(&selfHostedAccessible, "accessible", false,
		"Plain output without spinners; verbose state markers (for screen readers)")
	selfHostedCmd.PersistentFlags().BoolVar(&selfHostedRefreshToken, "refresh-token", false,
		"Re-mint the admin token via API Keys, bypassing any cached fingerprint")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedControlPlaneContext, "control-plane-context", "",
		"kubeconfig context for control plane (split-cluster mode; pair with --compute-plane-context)")
	selfHostedCmd.PersistentFlags().StringVar(&selfHostedComputePlaneContext, "compute-plane-context", "",
		"kubeconfig context for compute plane (split-cluster mode; pair with --control-plane-context)")

	selfHostedCmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		return kubectx.ValidateFlags(selfHostedControlPlaneContext, selfHostedComputePlaneContext)
	}
}

// resolveICMSURL picks the ICMS endpoint for cluster register, in priority order:
//  1. --icms-url flag (explicit user override).
//  2. NVCF_ICMS_URL env var.
//  3. NVCF_SIS_URL env var (legacy local quickstart name).
//  4. Derive from config.BaseHTTPURL by replacing the leading "api." host
//     prefix with "sis." — e.g. http://api.localhost:8080 → http://sis.localhost:8080.
//     This matches the multi-cluster gateway-routes layout where api/sis/invocation
//     are sibling HTTPRoutes on the shared envoy gateway.
//  5. Fallback to BaseHTTPURL unchanged (single-host deployments where the
//     gateway is fronted by one DNS name).
func resolveICMSURL(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("NVCF_ICMS_URL"); v != "" {
		return v
	}
	if v := os.Getenv("NVCF_SIS_URL"); v != "" {
		return v
	}
	cfg, err := client.LoadConfigWithoutAuth()
	if err != nil {
		return ""
	}
	base := cfg.BaseHTTPURL
	if base == "" {
		return ""
	}
	if derived, ok := deriveICMSFromAPI(base); ok {
		return derived
	}
	return base
}

// deriveICMSFromAPI swaps the host's leading "api." prefix with "sis.". Returns
// (derived, true) when the input host actually starts with "api."; (input,
// false) otherwise so the caller can fall back without a behavior change.
func deriveICMSFromAPI(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL, false
	}
	if !strings.HasPrefix(u.Hostname(), "api.") {
		return rawURL, false
	}
	u.Host = hostWithOptionalPort("sis."+strings.TrimPrefix(u.Hostname(), "api."), u.Port())
	return u.String(), true
}

func deriveSiblingHTTPServiceURL(rawURL, service string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := siblingServiceHost(u.Hostname(), service)
	u.Host = hostWithOptionalPort(host, u.Port())
	return u.String()
}

func deriveNATSURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	host := siblingServiceHost(u.Hostname(), "nats")
	return (&url.URL{
		Scheme: "nats",
		Host:   net.JoinHostPort(host, "4222"),
	}).String()
}

func localEndpointDefaultsFromICMSURL(rawURL string) (localEndpointDefaults, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return localEndpointDefaults{}, false
	}
	domain, ok := controlPlaneSiblingDomain(u.Hostname())
	if !ok || !isLocalhostDomain(domain) {
		return localEndpointDefaults{}, false
	}
	urlFor := func(service string) string {
		next := *u
		next.Host = hostWithOptionalPort(service+"."+domain, u.Port())
		return next.String()
	}
	return localEndpointDefaults{
		BaseHTTPURL: urlFor("api"),
		InvokeURL:   urlFor("invocation"),
		APIKeysURL:  urlFor("api-keys"),
		APIHost:     "api." + domain,
		InvokeHost:  "invocation." + domain,
		APIKeysHost: "api-keys." + domain,
	}, true
}

func applyLocalEndpointDefaults(icmsURL string) {
	if !strings.EqualFold(selfHostedEnv, "local") {
		return
	}
	defaults, ok := localEndpointDefaultsFromICMSURL(icmsURL)
	if !ok {
		return
	}
	setEnvDefault("NVCF_BASE_HTTP_URL", defaults.BaseHTTPURL)
	setEnvDefault("NVCF_INVOKE_URL", defaults.InvokeURL)
	setEnvDefault("API_KEYS_SERVICE_URL", defaults.APIKeysURL)
	setEnvDefault("API_KEYS_ADMIN_SERVICE_URL", defaults.APIKeysURL)
	setEnvDefault("API_HOST", defaults.APIHost)
	setEnvDefault("INVOKE_HOST", defaults.InvokeHost)
	setEnvDefault("API_KEYS_HOST", defaults.APIKeysHost)
}

func setEnvDefault(name, value string) {
	if value == "" {
		return
	}
	if _, ok := os.LookupEnv(name); ok {
		return
	}
	_ = os.Setenv(name, value)
}

func resolveNATSURL(flagValue, baseServiceURL string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("NVCF_NATS_URL"); v != "" {
		return v
	}
	return deriveNATSURL(baseServiceURL)
}

func resolveRegisterEndpointValues(env, controlCtx, computeCtx, icmsURL, natsURLOverride string) registerEndpointValues {
	if strings.EqualFold(env, "local") && kubectx.SelectMode(controlCtx, computeCtx) == kubectx.ModeSplit {
		natsURL := localSplitNATSURL(icmsURL)
		if natsURLOverride != "" || os.Getenv("NVCF_NATS_URL") != "" {
			natsURL = resolveNATSURL(natsURLOverride, localSplitHTTPServiceURL(icmsURL, "sis"))
		}
		return registerEndpointValues{
			ICMSServiceURL:  localSplitHTTPServiceURL(icmsURL, "sis"),
			ReValServiceURL: localSplitHTTPServiceURL(icmsURL, "reval"),
			NATSURL:         natsURL,
		}
	}

	return registerEndpointValues{
		ICMSServiceURL:  icmsURL,
		ReValServiceURL: deriveSiblingHTTPServiceURL(icmsURL, "reval"),
		NATSURL:         resolveNATSURL(natsURLOverride, icmsURL),
	}
}

func localSplitHTTPServiceURL(rawURL, service string) string {
	scheme := "http"
	port := localControlPlaneHTTPPortFromEnv()
	domain := localControlPlaneDomainFromURL(rawURL)
	if u, err := url.Parse(rawURL); err == nil {
		if u.Scheme != "" {
			scheme = u.Scheme
		}
		if u.Port() != "" {
			port = u.Port()
		}
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   hostWithOptionalPort(service+"."+domain, port),
	}).String()
}

func localSplitNATSURL(rawURL string) string {
	port := os.Getenv("CONTROL_PLANE_NATS_PORT")
	if port == "" {
		port = localControlPlaneNATSPort
	}
	return (&url.URL{
		Scheme: "nats",
		Host:   net.JoinHostPort("nats."+localControlPlaneDomainFromURL(rawURL), port),
	}).String()
}

func localControlPlaneHTTPPortFromEnv() string {
	if v := os.Getenv("CONTROL_PLANE_HTTP_PORT"); v != "" {
		return v
	}
	return localControlPlaneHTTPPort
}

func localControlPlaneDomainFromURL(rawURL string) string {
	if v := os.Getenv("CONTROL_PLANE_DOMAIN"); v != "" {
		return v
	}
	u, err := url.Parse(rawURL)
	if err == nil && u.Host != "" {
		if domain, ok := controlPlaneSiblingDomain(u.Hostname()); ok && !isLocalhostDomain(domain) {
			return domain
		}
	}
	return localControlPlaneDomainDefault
}

func controlPlaneSiblingDomain(host string) (string, bool) {
	for _, prefix := range []string{"api.", "sis.", "reval.", "nats."} {
		if strings.HasPrefix(host, prefix) {
			return strings.TrimPrefix(host, prefix), true
		}
	}
	return "", false
}

func isLocalhostDomain(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

func siblingServiceHost(host, service string) string {
	for _, prefix := range []string{"api.", "sis.", "reval.", "nats."} {
		if strings.HasPrefix(host, prefix) {
			return service + "." + strings.TrimPrefix(host, prefix)
		}
	}
	return host
}

func hostWithOptionalPort(host, port string) string {
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
