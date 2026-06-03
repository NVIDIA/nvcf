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

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"nvcf-cli/internal/state"

	"github.com/spf13/viper"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// AuthType represents the authentication type
type AuthType string

const (
	AuthTypeOAuth2 AuthType = "oauth2"
	AuthTypeBearer AuthType = "bearer"

	headerContentType = "Content-Type"
	contentTypeJSON   = "application/json"
	apiErrorFormat    = "API error %d: %s"
	functionTypeLLM   = "LLM"

	functionVersionEndpointFormat = "/v2/nvcf/functions/%s/versions/%s"
)

// Config holds the configuration for the NVCF client
type Config struct {
	// OAuth2 configuration
	OAuth2ClientID      string
	OAuth2ClientSecret  string
	OAuth2TokenEndpoint string

	// Authentication tokens
	APIKey string // General API operations token (NVCF_API_KEY)
	Token  string // Function creation specific token (NVCF_TOKEN)

	// Account configuration
	ClientID string // NVIDIA Cloud Account client ID (NVCF_CLIENT_ID)

	// Common configuration
	BaseHTTPURL    string
	BaseGRPCURL    string
	BaseInvokeURL  string // Dedicated endpoint for function invocations
	ICMSURL        string // ICMS/SIS endpoint for self-hosted cluster registration
	DefaultTimeout time.Duration
	AuthType       AuthType
	Debug          bool
	Demo           bool // Generate demo folder with JSON stubs after function creation

	// Host header overrides for hostname-based routing (self-hosted deployments)
	APIHost    string // Host header for NVCF API requests (e.g., "api.gateway.example.com")
	InvokeHost string // Host header for invocation requests (e.g., "invocation.gateway.example.com")
	ICMSHost   string // Host header for SIS/ICMS requests; allows pairing icms_url=http://<bare-elb> with Host: sis.<elb> for gateway-routed self-hosted deployments where sis.<elb> does not DNS-resolve.

	// Cluster mode configuration
	ClusterMode    bool           // Enable cluster mode (uses kubectl instead of direct HTTP)
	KubeconfigPath string         // Path to kubeconfig file for cluster access
	KubeContext    string         // kubeconfig context for cluster access
	ClusterConfig  *ClusterConfig // Cluster-specific configuration
}

// ClusterConfig holds cluster-specific configuration for in-cluster operations
type ClusterConfig struct {
	// Kubernetes configuration
	Namespace         string // NVCF namespace (e.g., "nvcf")
	APIService        string // API service endpoint (e.g., "api.nvcf.svc.cluster.local:8080")
	InvocationService string // Invocation service endpoint
	GRPCService       string // gRPC service endpoint
	UtilityImage      string // Container image for kubectl run operations

	// OpenBao configuration for token generation
	OpenBaoURL            string // OpenBao service URL (e.g., "http://openbao-server.vault-system.svc.cluster.local:8200")
	OpenBaoNamespace      string // OpenBao namespace (e.g., "vault-system")
	OpenBaoStatefulSet    string // OpenBao statefulset name
	OpenBaoContainer      string // OpenBao container name
	OpenBaoSecretName     string // Secret containing root token
	OpenBaoJWTAuthRole    string // JWT authentication role (e.g., "nvcf-api-account-bootstrap")
	OpenBaoJWTSigningRole string // JWT signing role (e.g., "nvcf-api-admin")

	// Account configuration
	NVCFAccount string // NVCF account name (e.g., "nvcf-default")
}

// LoadConfig loads configuration from YAML config file and environment variables
// getTokenWithFallback gets a token with priority: env > config > state
func getTokenWithFallback(configKey string, stateToken string, stateExpiration time.Time) string {
	// Priority 1: NVCF_TOKEN and NVCF_API_KEY environment variables
	// Priority 2: config file
	// Viper handles this order internally
	if configValue := viper.GetString(configKey); configValue != "" {
		return configValue
	}

	// Priority 3: State file (auto-generated fallback from 'nvcf-cli init')
	if stateToken != "" {
		if stateExpiration.IsZero() || time.Now().Before(stateExpiration) {
			return stateToken
		}
	}

	// No valid token found
	return ""
}

// isTokenExpired checks if a token from state is expired
func isTokenExpired(expiration time.Time) bool {
	if expiration.IsZero() {
		// No expiration set, assume valid for backwards compatibility
		return false
	}
	return time.Now().After(expiration)
}

// getTokenSource returns the source of the token for debugging
func getTokenSource(configKey string, stateToken string, stateExpiration time.Time) string {
	// Check in the same priority order as getTokenWithFallback: env > config > state
	envKey := "NVCF_" + strings.ToUpper(configKey)
	if os.Getenv(envKey) != "" {
		return "environment"
	}

	if viper.GetString(configKey) != "" {
		return "config_file"
	}

	if stateToken != "" && (stateExpiration.IsZero() || time.Now().Before(stateExpiration)) {
		return "state"
	}
	return "none"
}

func loadStateForActiveConfig() (*state.State, error) {
	configFile := viper.ConfigFileUsed()
	if configFile != "" {
		sm := state.GetStateManagerForConfig(configFile)
		err := sm.Load()
		return sm.GetState(), err
	}

	err := state.Load()
	return state.GetState(), err
}

// Environment variables take precedence over config file values, with state file fallback
func LoadConfig() (*Config, error) {
	// Load state first to get potential token fallbacks
	currentState, err := loadStateForActiveConfig()
	if err != nil {
		// Don't fail on state loading error, just log it
		if viper.GetBool("debug") {
			log.Printf("DEBUG: Could not load state file (not an error): %v", err)
		}
	}
	config := &Config{
		// OAuth2 configuration (Viper maps: oauth2_client_id → NVCF_OAUTH2_CLIENT_ID)
		OAuth2ClientID:      getConfigValue("oauth2_client_id"),
		OAuth2ClientSecret:  getConfigValue("oauth2_client_secret"),
		OAuth2TokenEndpoint: getConfigValue("oauth2_token_endpoint"),

		// Authentication tokens with state fallback (Viper maps: api_key → NVCF_API_KEY, token → NVCF_TOKEN)
		APIKey: getTokenWithFallback("api_key", currentState.APIKey, currentState.APIKeyExpiration),
		Token:  getTokenWithFallback("token", currentState.Token, currentState.TokenExpiration),

		// Account configuration (Viper maps: client_id → NVCF_CLIENT_ID)
		ClientID: getConfigValueWithDefault("client_id", "nvcf-default"),

		// Common configuration (Viper maps: base_http_url → NVCF_BASE_HTTP_URL, etc.)
		BaseHTTPURL:    getConfigValueWithDefault("base_http_url", "https://api.nvcf.nvidia.com"),
		BaseGRPCURL:    getConfigValueWithDefault("grpc_url", getConfigValueWithDefault("base_grpc_url", "grpc.nvcf.nvidia.com:443")),
		BaseInvokeURL:  getConfigValueWithDefault("invoke_url", getConfigValueWithDefault("base_http_url", "https://api.nvcf.nvidia.com")),
		ICMSURL:        getConfigValue("icms_url"),
		DefaultTimeout: 300 * time.Second,
		Debug:          viper.GetBool("debug"),
		Demo:           viper.GetBool("demo"),

		// Host header overrides for self-hosted deployments
		APIHost:    getConfigValue("api_host"),
		InvokeHost: getConfigValue("invoke_host"),
		ICMSHost:   getConfigValue("icms_host"),

		// Cluster mode configuration (deprecated, always false)
		ClusterMode:    false,
		KubeconfigPath: "",
	}

	// Debug logging for token sources
	if config.Debug {
		if config.APIKey != "" {
			apiKeySource := getTokenSource("api_key", currentState.APIKey, currentState.APIKeyExpiration)
			log.Printf("DEBUG: NVCF_API_KEY loaded from: %s", apiKeySource)
			if apiKeySource == "state" && !isTokenExpired(currentState.APIKeyExpiration) {
				log.Printf("DEBUG: State API key expires: %s", currentState.APIKeyExpiration.Format("2006-01-02 15:04:05"))
			}
		}
		if config.Token != "" {
			tokenSource := getTokenSource("token", currentState.Token, currentState.TokenExpiration)
			log.Printf("DEBUG: NVCF_TOKEN loaded from: %s", tokenSource)
			if tokenSource == "state" && !isTokenExpired(currentState.TokenExpiration) {
				log.Printf("DEBUG: State function token expires: %s", currentState.TokenExpiration.Format("2006-01-02 15:04:05"))
			}
		}
	}

	// All operations now use direct mode with external endpoints
	// No cluster mode or kubeconfig needed
	config.ClusterMode = false
	config.KubeconfigPath = ""

	if config.Debug {
		log.Printf("DEBUG: Using direct mode with external endpoints")
	}

	// Legacy cluster configuration block (kept for backward compatibility but never executed)
	if false {
		// Determine namespace - priority order:
		// 1. Explicit NVCF_CLUSTER_NAMESPACE configuration
		// 2. Namespace from kubeconfig context
		// 3. Default to "nvcf"
		clusterNamespace := getConfigValue("NVCF_CLUSTER_NAMESPACE")
		if clusterNamespace == "" {
			// Try to get namespace from kubeconfig context
			if kubeconfigNamespace := getNamespaceFromKubeconfig(""); kubeconfigNamespace != "" {
				clusterNamespace = kubeconfigNamespace
				if config.Debug {
					log.Printf("DEBUG: Using namespace from kubeconfig context: %s", clusterNamespace)
				}
			} else {
				clusterNamespace = "nvcf" // Default fallback
				if config.Debug {
					log.Printf("DEBUG: Using default namespace: %s", clusterNamespace)
				}
			}
		} else if config.Debug {
			log.Printf("DEBUG: Using explicit NVCF_CLUSTER_NAMESPACE: %s", clusterNamespace)
		}

		config.ClusterConfig = &ClusterConfig{
			Namespace:             clusterNamespace,
			APIService:            getConfigValueWithDefault("NVCF_CLUSTER_API_SERVICE", "api.nvcf.svc.cluster.local:8080"),
			InvocationService:     getConfigValueWithDefault("NVCF_CLUSTER_INVOCATION_SERVICE", "invocation.nvcf.svc.cluster.local:8080"),
			GRPCService:           getConfigValueWithDefault("NVCF_CLUSTER_GRPC_SERVICE", "grpc.nvcf.svc.cluster.local:10081"),
			UtilityImage:          getConfigValueWithDefault("NVCF_CLUSTER_UTILITY_IMAGE", "curlimages/curl:latest"),
			OpenBaoURL:            getConfigValueWithDefault("NVCF_OPENBAO_URL", "http://openbao-server.vault-system.svc.cluster.local:8200"),
			OpenBaoNamespace:      getConfigValueWithDefault("NVCF_OPENBAO_NAMESPACE", "vault-system"),
			OpenBaoStatefulSet:    getConfigValueWithDefault("NVCF_OPENBAO_STATEFULSET", "openbao-server"),
			OpenBaoContainer:      getConfigValueWithDefault("NVCF_OPENBAO_CONTAINER", "openbao"),
			OpenBaoSecretName:     getConfigValueWithDefault("NVCF_OPENBAO_SECRET_NAME", "openbao-server-root-token"),
			OpenBaoJWTAuthRole:    getConfigValueWithDefault("NVCF_OPENBAO_JWT_AUTH_ROLE", "nvcf-api-account-bootstrap"),
			OpenBaoJWTSigningRole: getConfigValueWithDefault("NVCF_OPENBAO_JWT_SIGNING_ROLE", "nvcf-api-admin"),
			NVCFAccount:           getConfigValueWithDefault("NVCF_CLUSTER_ACCOUNT", "nvcf-default"),
		}

		// Override HTTP URLs for cluster mode - use cluster internal endpoints
		config.BaseHTTPURL = "http://" + config.ClusterConfig.APIService
		config.BaseInvokeURL = "http://" + config.ClusterConfig.InvocationService
		config.BaseGRPCURL = config.ClusterConfig.GRPCService

		if config.Debug {
			log.Printf("DEBUG: Cluster mode enabled - overriding endpoints:")
			log.Printf("DEBUG:   BaseHTTPURL: %s", config.BaseHTTPURL)
			log.Printf("DEBUG:   BaseInvokeURL: %s", config.BaseInvokeURL)
			log.Printf("DEBUG:   BaseGRPCURL: %s", config.BaseGRPCURL)
		}
	}

	// Determine authentication type based on available credentials
	if config.APIKey != "" || config.Token != "" {
		config.AuthType = AuthTypeBearer
	} else if config.OAuth2ClientID != "" && config.OAuth2ClientSecret != "" && config.OAuth2TokenEndpoint != "" {
		config.AuthType = AuthTypeOAuth2
	} else {
		// In cluster mode, we might be able to generate tokens via OpenBao
		if config.ClusterMode {
			log.Printf("DEBUG: Cluster mode enabled - tokens can be generated via OpenBao")
			config.AuthType = AuthTypeBearer // We'll generate tokens as needed
		} else {
			// Check what's missing and provide helpful error message
			var missing []string
			if config.APIKey == "" && config.Token == "" {
				missing = append(missing, "NVCF_API_KEY or NVCF_TOKEN")
			}
			if config.OAuth2ClientID == "" {
				missing = append(missing, "NVCF_OAUTH2_CLIENT_ID")
			}
			if config.OAuth2ClientSecret == "" {
				missing = append(missing, "NVCF_OAUTH2_CLIENT_SECRET")
			}
			if config.OAuth2TokenEndpoint == "" {
				missing = append(missing, "NVCF_OAUTH2_TOKEN_ENDPOINT")
			}

			return nil, fmt.Errorf("missing authentication credentials. Please set NVCF_API_KEY environment variable")
		}
	}

	return config, nil
}

// LoadConfigWithoutAuth loads configuration without requiring authentication credentials.
// This is used by commands like 'init' and 'refresh' that generate tokens.
func LoadConfigWithoutAuth() (*Config, error) {
	// Load state first to get potential token fallbacks
	currentState, err := loadStateForActiveConfig()
	if err != nil {
		// Don't fail on state loading error, just log it
		if viper.GetBool("debug") {
			log.Printf("DEBUG: Could not load state file (not an error): %v", err)
		}
	}
	config := &Config{
		// OAuth2 configuration (Viper maps: oauth2_client_id → NVCF_OAUTH2_CLIENT_ID)
		OAuth2ClientID:      getConfigValue("oauth2_client_id"),
		OAuth2ClientSecret:  getConfigValue("oauth2_client_secret"),
		OAuth2TokenEndpoint: getConfigValue("oauth2_token_endpoint"),

		// Authentication tokens with state fallback (Viper maps: api_key → NVCF_API_KEY, token → NVCF_TOKEN)
		APIKey: getTokenWithFallback("api_key", currentState.APIKey, currentState.APIKeyExpiration),
		Token:  getTokenWithFallback("token", currentState.Token, currentState.TokenExpiration),

		// Account configuration (Viper maps: client_id → NVCF_CLIENT_ID)
		ClientID: getConfigValueWithDefault("client_id", "nvcf-default"),

		// Common configuration (Viper maps: base_http_url → NVCF_BASE_HTTP_URL, etc.)
		BaseHTTPURL:    getConfigValueWithDefault("base_http_url", "https://api.nvcf.nvidia.com"),
		BaseGRPCURL:    getConfigValueWithDefault("grpc_url", getConfigValueWithDefault("base_grpc_url", "grpc.nvcf.nvidia.com:443")),
		BaseInvokeURL:  getConfigValueWithDefault("invoke_url", getConfigValueWithDefault("base_http_url", "https://api.nvcf.nvidia.com")),
		ICMSURL:        getConfigValue("icms_url"),
		DefaultTimeout: 300 * time.Second,
		Debug:          viper.GetBool("debug"),
		Demo:           viper.GetBool("demo"),

		// Host header overrides for self-hosted deployments
		APIHost:    getConfigValue("api_host"),
		InvokeHost: getConfigValue("invoke_host"),
		ICMSHost:   getConfigValue("icms_host"),

		// Cluster mode configuration (deprecated, always false)
		ClusterMode:    false,
		KubeconfigPath: "",
	}

	if config.Debug {
		log.Printf("DEBUG: Using direct mode with external endpoints (no auth required)")
	}

	// Set auth type to Bearer if we have any credentials, otherwise leave it empty
	// Commands using this function will generate credentials as needed
	if config.APIKey != "" || config.Token != "" {
		config.AuthType = AuthTypeBearer
	} else if config.OAuth2ClientID != "" && config.OAuth2ClientSecret != "" && config.OAuth2TokenEndpoint != "" {
		config.AuthType = AuthTypeOAuth2
	}
	// Note: No error if credentials are missing - that's expected for init/refresh

	return config, nil
}

// getConfigValue reads configuration value with proper priority order:
// 1. Environment variable (highest priority - explicit user override)
// 2. Config file via Viper (static configuration)
func getConfigValue(key string) string {
	// Viper handles both environment variables and config file
	// with automatic env mapping (e.g., base_http_url → NVCF_BASE_HTTP_URL)
	// Priority: env vars > config file
	if viper.IsSet(key) {
		return viper.GetString(key)
	}
	return ""
}

// getConfigValueWithDefault reads configuration value with proper priority order:
// 1. Environment variable (via Viper's AutomaticEnv - highest priority)
// 2. Config file via Viper (static configuration)
// 3. Default value (lowest priority - fallback)
func getConfigValueWithDefault(key, defaultValue string) string {
	// Viper handles both environment variables and config file
	// with automatic env mapping (e.g., base_http_url → NVCF_BASE_HTTP_URL)
	// Priority: env vars > config file > default
	if viper.IsSet(key) {
		return viper.GetString(key)
	}
	return defaultValue
}

// baseHTTPURLHost returns the host portion of the configured base_http_url
// (e.g. "elb.example.com" for "http://elb.example.com:8080" or
// "elb.example.com:8080" if a port is present and significant).
// Returns "" if the URL cannot be parsed or has no host, in which case the
// host header transport will leave Host headers untouched.
func baseHTTPURLHost(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

func directInvocationURL(baseInvokeURL, functionID, inferenceURL string) (string, error) {
	u, err := url.Parse(baseInvokeURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse invoke URL %q: %w", baseInvokeURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invoke URL must include scheme and host: %q", baseInvokeURL)
	}

	hostname := u.Hostname()
	if strings.HasPrefix(hostname, functionID+".") {
		// Already function-specific.
	} else if strings.HasPrefix(hostname, "invocation.") {
		hostname = functionID + "." + hostname
	} else if strings.HasPrefix(hostname, "api.") {
		hostname = functionID + ".invocation." + strings.TrimPrefix(hostname, "api.")
	} else {
		hostname = functionID + ".invocation." + hostname
	}
	if port := u.Port(); port != "" {
		u.Host = hostname + ":" + port
	} else {
		u.Host = hostname
	}

	u.Path = path.Join(u.Path, inferenceURL)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	if strings.HasSuffix(inferenceURL, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

// expandTildePath expands ~ to the user's home directory
func expandTildePath(path string) string {
	if path == "" || !strings.HasPrefix(path, "~/") {
		return path
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return path // Return original if we can't get home dir
	}

	return filepath.Join(homeDir, path[2:])
}

// getNamespaceFromKubeconfig extracts the namespace from the kubeconfig context
func getNamespaceFromKubeconfig(kubeconfigPath string) string {
	if kubeconfigPath == "" {
		return ""
	}

	// Try to get the namespace from the current context
	// Using kubectl to avoid adding kubernetes client dependencies
	cmd := exec.Command("kubectl", "config", "view", "--kubeconfig", kubeconfigPath, "--minify", "-o", "jsonpath={.contexts[0].context.namespace}")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	namespace := strings.TrimSpace(string(output))
	if namespace == "" {
		return ""
	}

	return namespace
}

// Client is the NVCF API client
type Client struct {
	httpClient *http.Client
	grpcConn   *grpc.ClientConn
	config     *Config
	baseURL    string
	debug      bool
}

// BearerTokenTransport implements http.RoundTripper for bearer token authentication
type BearerTokenTransport struct {
	Token string
	Base  http.RoundTripper
}

func (t *BearerTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.Token)
	return t.base().RoundTrip(req)
}

func (t *BearerTokenTransport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// NewClient creates a new NVCF client
func NewClient(config *Config) (*Client, error) {
	var httpClient *http.Client

	// Create HTTP client based on authentication type
	switch config.AuthType {
	case AuthTypeBearer:
		// Set up transport chain: Debug -> Auth -> HTTP
		// This way auth transport adds headers first, then debug transport sees them

		var finalTransport http.RoundTripper = http.DefaultTransport

		// Add authentication transport layer (this adds the Authorization header)
		if config.Token != "" {
			// Use multi-token transport when we have a function creation specific token
			finalTransport = newMultiTokenTransport(config.APIKey, config.Token, finalTransport)
			if config.Debug {
				log.Println("DEBUG: HTTP debugging enabled with multi-token support")
				log.Printf("DEBUG: API key available: %t", config.APIKey != "")
				log.Printf("DEBUG: Function token available: %t", config.Token != "")
			}
		} else {
			// Fallback to single bearer token transport
			finalTransport = &BearerTokenTransport{
				Token: config.APIKey,
				Base:  finalTransport,
			}
			if config.Debug {
				log.Println("DEBUG: HTTP debugging enabled with single API key")
			}
		}

		// Add debug transport layer INSIDE the auth layer (so it sees auth headers)
		if config.Debug {
			// Replace the base transport in the auth layer with debug transport
			if config.Token != "" {
				finalTransport = newMultiTokenTransport(config.APIKey, config.Token, newDebugTransport(http.DefaultTransport))
			} else {
				finalTransport = &BearerTokenTransport{
					Token: config.APIKey,
					Base:  newDebugTransport(http.DefaultTransport),
				}
			}
		}

		// Add host header transport layer (outermost - rewrites Host header for
		// requests targeting the configured base API host).
		// Note: Invoke host overrides are applied per request in InvokeFunctionWithOptions.
		if config.APIHost != "" {
			finalTransport = newHostHeaderTransport(baseHTTPURLHost(config.BaseHTTPURL), config.APIHost, config.Debug, finalTransport)
		}

		httpClient = &http.Client{
			Transport: finalTransport,
			Timeout:   config.DefaultTimeout,
		}

	case AuthTypeOAuth2:
		// Set up OAuth2 configuration
		oauth2Config := &clientcredentials.Config{
			ClientID:     config.OAuth2ClientID,
			ClientSecret: config.OAuth2ClientSecret,
			TokenURL:     config.OAuth2TokenEndpoint,
			Scopes: []string{
				"deploy_function",
				"invoke_function",
				"list_functions",
				"list_functions_details",
				"register_function",
				"delete_function",
				"update_instance",
				"list_cluster_groups",
			},
		}

		// Create HTTP client with OAuth2 transport
		ctx := context.Background()
		httpClient = oauth2Config.Client(ctx)
		httpClient.Timeout = config.DefaultTimeout

		// Wrap with debug transport if debug is enabled
		if config.Debug {
			log.Println("DEBUG: HTTP debugging enabled")
			httpClient.Transport = newDebugTransport(httpClient.Transport)
		}

		// Add host header transport layer (outermost - rewrites Host header for
		// requests targeting the configured base API host).
		// Note: Invoke host overrides are applied per request in InvokeFunctionWithOptions.
		if config.APIHost != "" {
			httpClient.Transport = newHostHeaderTransport(baseHTTPURLHost(config.BaseHTTPURL), config.APIHost, config.Debug, httpClient.Transport)
		}

	default:
		return nil, fmt.Errorf("unsupported authentication type: %s", config.AuthType)
	}

	// Set up gRPC connection
	creds := credentials.NewTLS(nil)
	grpcConn, err := grpc.Dial(config.BaseGRPCURL, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("failed to create gRPC connection: %w", err)
	}

	return &Client{
		httpClient: httpClient,
		grpcConn:   grpcConn,
		config:     config,
		baseURL:    config.BaseHTTPURL,
		debug:      config.Debug,
	}, nil
}

// Close closes the client connections
func (c *Client) Close() error {
	if c.grpcConn != nil {
		return c.grpcConn.Close()
	}
	return nil
}

// makeRequest makes an HTTP request to the NVCF API using direct HTTP calls
func (c *Client) makeRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	// Always use direct HTTP client (no more cluster mode)
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	fullURL := c.baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if body != nil {
		req.Header.Set(headerContentType, contentTypeJSON)
	}
	req.Header.Set("Accept", contentTypeJSON)

	return c.httpClient.Do(req)
}

// isAdminOperation checks if this request requires function management privileges
// Based on NVCF service @PreAuthorize annotations requiring function management scopes
func (c *Client) isAdminOperation(method, path string) bool {
	// FUNCTION OPERATIONS requiring standard scopes (work with both NVCF_TOKEN and NVCF_API_KEY):

	// list_functions / list_functions_details - List all functions (GET on functions endpoint)
	if method == "GET" && strings.Contains(path, "/functions") && !strings.Contains(path, "/functions/") {
		return true
	}

	// list_functions / list_functions_details - Get specific function details (GET with function ID)
	if method == "GET" && strings.Contains(path, "/functions/") && !strings.Contains(path, "/queues/") {
		// Cross-account endpoints are admin operations
		if strings.Contains(path, "/accounts/") {
			return true
		}
		// Regular endpoints without /versions are admin operations
		if !strings.Contains(path, "/versions") {
			return true
		}
	}

	// register_function - Function creation
	if method == "POST" && strings.Contains(path, "/v2/nvcf/functions") {
		return true
	}

	// deploy_function - Function deployment (POST, GET, PUT, PATCH)
	if strings.Contains(path, "/deployments/") && (method == "POST" || method == "GET" || method == "PUT" || method == "PATCH") {
		return true
	}

	// delete_function - Function/deployment deletion
	if method == "DELETE" && (strings.Contains(path, "/functions/") || strings.Contains(path, "/deployments/")) {
		return true
	}

	// update_function - Function updates
	if method == "PUT" && strings.Contains(path, "/functions/") {
		return true
	}

	// manage_registry_credentials - Registry credential management (all operations)
	if strings.Contains(path, "/registry-credentials") || strings.Contains(path, "/recognized-registries") {
		return true
	}

	// account_setup - Account management operations
	if strings.Contains(path, "/v2/nvcf/accounts") && !strings.Contains(path, "/functions") && !strings.Contains(path, "/deployments") &&
		!strings.Contains(path, "/registry-credentials") && !strings.Contains(path, "/secrets") && !strings.Contains(path, "/queues") {
		return true
	}

	// manage_registry_credentials - Secret management operations
	if strings.Contains(path, "/secrets/") {
		return true
	}

	// Queue operations are USER operations (queue_details scope with API key)
	// /queues/functions/ - user level
	// /queues/{requestId}/position - user level

	// Default: not an admin operation
	return false
}

// getAccountID returns the account ID to use for cross-account operations
func (c *Client) getAccountID() string {
	// Use cluster config account if available, otherwise default
	if c.config.ClusterConfig != nil && c.config.ClusterConfig.NVCFAccount != "" {
		return c.config.ClusterConfig.NVCFAccount
	}
	// Use client ID if set, otherwise fallback to default
	if c.config.ClientID != "" {
		return c.config.ClientID
	}
	return "nvcf-default"
}

// HealthDto represents function health configuration
type HealthDto struct {
	Protocol           string `json:"protocol"`           // HTTP/gRPC protocol type for health endpoint
	URI                string `json:"uri"`                // Health endpoint for the container or helmChart
	Port               int    `json:"port"`               // Port number where the health listener is running
	Timeout            string `json:"timeout"`            // ISO 8601 duration string in PnDTnHnMn.nS format
	ExpectedStatusCode int    `json:"expectedStatusCode"` // Expected return status code considered as successful
}

// ContainerEnvironmentEntry represents an environment variable for the container
type ContainerEnvironmentEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ArtifactDto represents a model or resource artifact
type ArtifactDto struct {
	Name      string        `json:"name"`
	Version   string        `json:"version,omitempty"`
	URI       string        `json:"uri,omitempty"`
	LLMConfig *LLMConfigDto `json:"llmConfig,omitempty"`
}

// LLMConfigDto represents LLM routing metadata for a model artifact
type LLMConfigDto struct {
	URIs           []string `json:"uris,omitempty"`
	TokenRateLimit *string  `json:"tokenRateLimit,omitempty"`
	RoutingMethod  *string  `json:"routingMethod,omitempty"`
}

// SecretDto represents a secret configuration
type SecretDto struct {
	Name  string      `json:"name"`            // Secret name (required, max 48 chars, pattern: ^[a-z0-9A-Z][a-z0-9A-Z\\_\\.\\-]*$)
	Value interface{} `json:"value,omitempty"` // Secret value (JsonNode - any JSON value, 1-32768 chars)
}

// RateLimitDto represents rate limit configuration
type RateLimitDto struct {
	RateLimit      string   `json:"rateLimit"`                // Rate limit pattern (e.g., "100-S", "50-M", "10-H", "5-D")
	ExemptedNcaIds []string `json:"exemptedNcaIds,omitempty"` // NCA ID exemptions (max 32)
	SyncCheck      bool     `json:"syncCheck,omitempty"`      // Sync check, defaults to false
}

// TelemetriesDto represents telemetry configuration
type TelemetriesDto struct {
	LogsTelemetryId    string `json:"logsTelemetryId,omitempty"`    // UUID for logs telemetry
	MetricsTelemetryId string `json:"metricsTelemetryId,omitempty"` // UUID for metrics telemetry
	TracesTelemetryId  string `json:"tracesTelemetryId,omitempty"`  // UUID for traces telemetry
}

// CreateFunctionRequest represents the request to create a function
type CreateFunctionRequest struct {
	// Required fields
	Name           string `json:"name"`                     // Function name
	InferenceURL   string `json:"inferenceUrl"`             // Entrypoint for invoking the container
	ContainerImage string `json:"containerImage,omitempty"` // Container image
	InferencePort  int    `json:"inferencePort"`            // Port number where the inference listener is running

	// Health configuration
	HealthURI string     `json:"healthUri,omitempty"` // Simple health endpoint (deprecated, use Health instead)
	Health    *HealthDto `json:"health,omitempty"`    // Detailed health configuration

	// Function configuration
	FunctionType         string                      `json:"functionType,omitempty"`         // DEFAULT, STREAMING, or LLM
	APIBodyFormat        string                      `json:"apiBodyFormat,omitempty"`        // Invocation request body format
	ContainerArgs        string                      `json:"containerArgs,omitempty"`        // Args to be passed when launching container
	ContainerEnvironment []ContainerEnvironmentEntry `json:"containerEnvironment,omitempty"` // Environment settings for container

	// Helm configuration
	HelmChart            string `json:"helmChart,omitempty"`            // Optional Helm Chart
	HelmChartServiceName string `json:"helmChartServiceName,omitempty"` // Helm Chart Service Name

	// Resources and models
	Models    []ArtifactDto `json:"models,omitempty"`    // Optional set of models
	Resources []ArtifactDto `json:"resources,omitempty"` // Optional set of resources

	// Security and rate limiting
	Secrets   []SecretDto   `json:"secrets,omitempty"`   // Optional secrets
	RateLimit *RateLimitDto `json:"rateLimit,omitempty"` // Optional rate limit config

	// Monitoring
	Telemetries *TelemetriesDto `json:"telemetries,omitempty"` // Optional telemetry configuration

	// Metadata
	Tags        []string `json:"tags,omitempty"`        // Optional set of tags
	Description string   `json:"description,omitempty"` // Optional function/version description
}

// CreateFunctionResponse represents the response from creating a function
type CreateFunctionResponse struct {
	Function FunctionData `json:"function"`
}

// FunctionData represents function data
type FunctionData struct {
	ID             string `json:"id"`
	VersionID      string `json:"versionId"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	InferenceURL   string `json:"inferenceUrl"`
	InferencePort  int    `json:"inferencePort"`
	ContainerImage string `json:"containerImage"`
	CreationTime   string `json:"creationTime"`
}

// CreateFunction creates a new function
func (c *Client) CreateFunction(ctx context.Context, req *CreateFunctionRequest) (*CreateFunctionResponse, error) {
	// Validate required fields (per OpenAPI spec)
	if req.Name == "" {
		return nil, fmt.Errorf("name is required (1-128 chars, pattern: ^[a-z0-9A-Z][a-z0-9A-Z\\-_]*$)")
	}
	if req.InferenceURL == "" {
		return nil, fmt.Errorf("inferenceUrl is required")
	}

	// Validate Health configuration if provided - all health fields are required when health object is present
	if req.Health != nil {
		if req.Health.Protocol == "" {
			return nil, fmt.Errorf("health.protocol is required when health is specified (must be 'HTTP' or 'gRPC')")
		}
		if req.Health.URI == "" {
			return nil, fmt.Errorf("health.uri is required when health is specified")
		}
		if req.Health.Port <= 0 {
			return nil, fmt.Errorf("health.port is required when health is specified (must be a positive number)")
		}
		if req.Health.Timeout == "" {
			return nil, fmt.Errorf("health.timeout is required when health is specified (ISO 8601 duration format, e.g., 'PT10S')")
		}
		if req.Health.ExpectedStatusCode <= 0 {
			return nil, fmt.Errorf("health.expectedStatusCode is required when health is specified (must be a positive number)")
		}
	}

	// Validate helmChartServiceName is required when helmChart is provided
	if req.HelmChart != "" && req.HelmChartServiceName == "" {
		return nil, fmt.Errorf("helmChartServiceName is required when helmChart is specified")
	}

	// Validate secrets if provided
	for i, secret := range req.Secrets {
		if secret.Name == "" {
			return nil, fmt.Errorf("secrets[%d].name is required (1-48 chars, pattern: ^[a-z0-9A-Z][a-z0-9A-Z\\_\\.\\-]*$)", i)
		}
	}

	if req.APIBodyFormat == "" {
		req.APIBodyFormat = "CUSTOM"
	}

	// Function creation requires authentication with register_function scope
	if c.config.Token == "" && c.config.APIKey == "" {
		return nil, fmt.Errorf("function creation requires NVCF_TOKEN or NVCF_API_KEY with 'register_function' scope")
	}

	// Use regular endpoint (works with both JWT and API key)
	endpoint := "/v2/nvcf/functions"

	resp, err := c.makeRequest(ctx, "POST", endpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result CreateFunctionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// DeleteFunction deletes a function version
func (c *Client) DeleteFunction(ctx context.Context, functionID, versionID string) error {
	// Function deletion requires authentication with delete_function scope
	if c.config.Token == "" && c.config.APIKey == "" {
		return fmt.Errorf("function deletion requires NVCF_TOKEN or NVCF_API_KEY with 'delete_function' scope")
	}

	// Use regular endpoint (works with both JWT and API key)
	endpoint := fmt.Sprintf(functionVersionEndpointFormat,
		url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	return nil
}

// GetFunctionResponse represents function details response
type GetFunctionResponse struct {
	Function FunctionDetails `json:"function"`
}

// FunctionDetails represents detailed function information
type FunctionDetails struct {
	ID              string     `json:"id"`
	VersionID       string     `json:"versionId"`
	Name            string     `json:"name"`
	Status          string     `json:"status"`
	InferenceURL    string     `json:"inferenceUrl"`
	InferencePort   int        `json:"inferencePort"`
	ContainerImage  string     `json:"containerImage"`
	CreationTime    string     `json:"creationTime"`
	ActiveInstances []Instance `json:"activeInstances"`
	Health          HealthInfo `json:"health"`
}

// Instance represents a function instance
type Instance struct {
	InstanceID   string `json:"instanceId"`
	InstanceType string `json:"instanceType"`
	GPU          string `json:"gpu"`
	Status       string `json:"status"`
}

// HealthInfo represents function health information
type HealthInfo struct {
	Status string `json:"status"`
}

// GetFunction gets function details
func (c *Client) GetFunction(ctx context.Context, functionID, versionID string) (*GetFunctionResponse, error) {
	// Choose endpoint based on available authentication
	// Use regular endpoint (works with both JWT and API key)
	endpoint := fmt.Sprintf(functionVersionEndpointFormat,
		url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result GetFunctionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// FunctionDeploymentRequest represents a deployment request
type FunctionDeploymentRequest struct {
	DeploymentSpecifications []GPUSpecificationDto `json:"deploymentSpecifications"`
}

// GPUSpecificationDto represents GPU specification for deployment
type GPUSpecificationDto struct {
	// Identity (populated by server responses)
	GpuSpecificationID string `json:"gpuSpecificationId,omitempty"` // Server-assigned ID; used as path param for PATCH

	// Required fields
	GPU          string `json:"gpu"`          // GPU name from the cluster
	InstanceType string `json:"instanceType"` // Instance type, based on GPU, assigned to a Worker
	MinInstances int    `json:"minInstances"` // Minimum number of spot instances for the deployment
	MaxInstances int    `json:"maxInstances"` // Maximum number of spot instances for the deployment

	// Optional deployment configuration
	Backend               string         `json:"backend,omitempty"`               // Backend/CSP where the GPU powered instance will be launched
	AvailabilityZones     []string       `json:"availabilityZones,omitempty"`     // List of availability-zones(or clusters) in the cluster group
	MaxRequestConcurrency int            `json:"maxRequestConcurrency,omitempty"` // Max request concurrency between 1 (default) and 1024
	Configuration         map[string]any `json:"configuration,omitempty"`         // Typically used when the function is based on Helm Charts
	Clusters              []string       `json:"clusters,omitempty"`              // Specific clusters within spot instance or worker node
	Regions               []string       `json:"regions,omitempty"`               // List of regions allowed to deploy
	Attributes            []string       `json:"attributes,omitempty"`            // Specific attributes capabilities to deploy functions
	PreferredOrder        int            `json:"preferredOrder,omitempty"`        // Preferred order of deployment if there are several gpu specs

	// Hardware specifications
	CPUArch       string `json:"cpuArch,omitempty"`       // Architecture details of the CPU
	OS            string `json:"os,omitempty"`            // Operating system details
	DriverVersion string `json:"driverVersion,omitempty"` // GPU driver version
	Storage       string `json:"storage,omitempty"`       // The amount of available storage, e.g. 80G
	SystemMemory  string `json:"systemMemory,omitempty"`  // The amount of RAM
	GPUMemory     string `json:"gpuMemory,omitempty"`     // The amount of GPU memory
}

// AutoscalingConfigurationPolicy mirrors server AutoscalingConfigurationPolicyEnum.
// CUSTOM_CONFIGURATION (default): use the provided autoscalingConfig.
// PLATFORM_CONFIGURATION: remove custom config and use platform defaults.
type AutoscalingConfigurationPolicy string

const (
	AutoscalingPolicyCustom   AutoscalingConfigurationPolicy = "CUSTOM_CONFIGURATION"
	AutoscalingPolicyPlatform AutoscalingConfigurationPolicy = "PLATFORM_CONFIGURATION"
)

// StickinessWindow is the ISO-8601 duration window used by ScalingDetails.
// Both fields are required when stickiness is set; size must be <= PT1H and
// threshold must be < size (server enforces).
type StickinessWindow struct {
	Size      string `json:"size"`      // ISO-8601 duration, e.g. "PT30M"
	Threshold string `json:"threshold"` // ISO-8601 duration, e.g. "PT5M"
}

// ScalingDetails configures one direction (up or down) of autoscaling.
// Factor must be > 1.0 for scale up and < 1.0 for scale down (server enforces).
type ScalingDetails struct {
	Metric     string            `json:"metric,omitempty"`
	Factor     float32           `json:"factor"`
	Threshold  int               `json:"threshold"`
	Stickiness *StickinessWindow `json:"stickiness,omitempty"`
}

// AutoscalingConfigurationDto mirrors the server DTO for custom autoscaling.
type AutoscalingConfigurationDto struct {
	ScaleUpDetails   *ScalingDetails `json:"scaleUpDetails,omitempty"`
	ScaleDownDetails *ScalingDetails `json:"scaleDownDetails,omitempty"`
}

// UpdateGpuSpecificationRequest is the body for
// PATCH /v2/nvcf/deployments/{deploymentId}/gpu-specifications/{gpuSpecId}.
// All fields are optional; the server requires at least one.
type UpdateGpuSpecificationRequest struct {
	MinInstances                   *int                           `json:"minInstances,omitempty"`
	MaxInstances                   *int                           `json:"maxInstances,omitempty"`
	AutoscalingConfiguration       *AutoscalingConfigurationDto   `json:"autoscalingConfiguration,omitempty"`
	AutoscalingConfigurationPolicy AutoscalingConfigurationPolicy `json:"autoscalingConfigurationPolicy,omitempty"`
}

// UpdateGpuSpecificationResponse is the response body for the PATCH endpoint.
type UpdateGpuSpecificationResponse struct {
	GpuSpecification GPUSpecificationDto `json:"gpuSpecification"`
}

// DeployFunction deploys a function
func (c *Client) DeployFunction(ctx context.Context, functionID, versionID string, req *FunctionDeploymentRequest) error {
	// Validate required fields (per OpenAPI spec)
	if len(req.DeploymentSpecifications) == 0 {
		return fmt.Errorf("deploymentSpecifications is required (at least 1 specification must be provided)")
	}

	// Validate each GPU specification
	for i, spec := range req.DeploymentSpecifications {
		if spec.GPU == "" {
			return fmt.Errorf("deploymentSpecifications[%d].gpu is required", i)
		}
		if spec.InstanceType == "" {
			return fmt.Errorf("deploymentSpecifications[%d].instanceType is required", i)
		}
		if spec.MinInstances < 0 {
			return fmt.Errorf("deploymentSpecifications[%d].minInstances must be >= 0", i)
		}
		if spec.MaxInstances <= 0 {
			return fmt.Errorf("deploymentSpecifications[%d].maxInstances must be > 0", i)
		}
		if spec.MinInstances > spec.MaxInstances {
			return fmt.Errorf("deploymentSpecifications[%d].minInstances (%d) cannot be greater than maxInstances (%d)",
				i, spec.MinInstances, spec.MaxInstances)
		}
	}

	// Function deployment requires authentication with deploy_function scope
	if c.config.Token == "" && c.config.APIKey == "" {
		return fmt.Errorf("function deployment requires NVCF_TOKEN or NVCF_API_KEY with 'deploy_function' scope")
	}

	// Use regular endpoint (works with both JWT and API key)
	endpoint := fmt.Sprintf("/v2/nvcf/deployments/functions/%s/versions/%s",
		url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "POST", endpoint, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	return nil
}

// UpdateFunctionMetadataRequest represents a function update request
type UpdateFunctionMetadataRequest struct {
	Description  string           `json:"description,omitempty"`  // Function description
	Tags         []string         `json:"tags,omitempty"`         // Function tags
	ModelUpdates []ModelUpdateDto `json:"modelUpdates,omitempty"` // Model-specific updates
}

// ModelUpdateDto represents updates for one model.
type ModelUpdateDto struct {
	ModelName string              `json:"modelName"`
	LLMConfig *LLMConfigUpdateDto `json:"llmConfig,omitempty"`
}

// LLMConfigUpdateDto represents mutable LLM config fields.
type LLMConfigUpdateDto struct {
	TokenRateLimit *string `json:"tokenRateLimit,omitempty"`
	RoutingMethod  *string `json:"routingMethod,omitempty"`
}

// UpdateGpuSpecification updates a single GPU specification of an existing
// function deployment via the per-spec PATCH endpoint. Use GetDeployment first
// to look up the deploymentId and gpuSpecificationId.
func (c *Client) UpdateGpuSpecification(ctx context.Context, deploymentID, gpuSpecID string, req *UpdateGpuSpecificationRequest) (*UpdateGpuSpecificationResponse, error) {
	if c.config.Token == "" && c.config.APIKey == "" {
		return nil, fmt.Errorf("deployment update requires NVCF_TOKEN or NVCF_API_KEY with 'deploy_function' scope")
	}

	endpoint := fmt.Sprintf("/v2/nvcf/deployments/%s/gpu-specifications/%s",
		url.PathEscape(deploymentID), url.PathEscape(gpuSpecID))

	resp, err := c.makeRequest(ctx, "PATCH", endpoint, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var result UpdateGpuSpecificationResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &result, nil
}

// UpdateFunctionMetadata updates mutable function fields
func (c *Client) UpdateFunctionMetadata(ctx context.Context, functionID, versionID string, req *UpdateFunctionMetadataRequest) error {
	if c.config.Token == "" && c.config.APIKey == "" {
		return fmt.Errorf("function update requires NVCF_TOKEN or NVCF_API_KEY with 'update_function' scope")
	}

	endpoint := fmt.Sprintf(functionVersionEndpointFormat,
		url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "PUT", endpoint, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	return nil
}

// DeleteDeployment deletes a function deployment
// Requires: deploy_function scope
func (c *Client) DeleteDeployment(ctx context.Context, functionID, versionID string, graceful bool) error {
	// Deployment deletion requires authentication with deploy_function scope
	if c.config.Token == "" && c.config.APIKey == "" {
		return fmt.Errorf("deployment deletion requires NVCF_TOKEN or NVCF_API_KEY with 'deploy_function' scope")
	}

	// Use regular endpoint (not cross-account)
	endpoint := fmt.Sprintf("/v2/nvcf/deployments/functions/%s/versions/%s",
		url.PathEscape(functionID), url.PathEscape(versionID))

	if graceful {
		endpoint += "?graceful=true"
	}

	resp, err := c.makeRequest(ctx, "DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	return nil
}

// GetDeployment gets function deployment details
func (c *Client) GetDeployment(ctx context.Context, functionID, versionID string) (*DeploymentResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/deployments/functions/%s/versions/%s",
		url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result DeploymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// DeploymentResponse represents deployment details response
type DeploymentResponse struct {
	Deployment FunctionDeploymentDto `json:"deployment"`
}

// FunctionDeploymentDto represents detailed deployment information
type FunctionDeploymentDto struct {
	FunctionID               string                `json:"functionId"`
	FunctionVersionID        string                `json:"functionVersionId"`
	DeploymentID             string                `json:"deploymentId"`
	FunctionName             string                `json:"functionName"`
	NcaID                    string                `json:"ncaId"`
	FunctionStatus           string                `json:"functionStatus"`
	RequestQueueURL          string                `json:"requestQueueUrl,omitempty"` // Deprecated
	HealthInfo               []DeploymentHealthDto `json:"healthInfo,omitempty"`
	DeploymentSpecifications []GPUSpecificationDto `json:"deploymentSpecifications"`
	CreatedAt                string                `json:"createdAt"`
	LastUpdatedAt            string                `json:"lastUpdatedAt"`
	// Legacy fields for backward compatibility
	Status       string `json:"status,omitempty"`
	CreationTime string `json:"creationTime,omitempty"`
}

// DeploymentHealthDto represents health information for a deployment
type DeploymentHealthDto struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// WaitForFunctionDeployment waits for a function to be deployed
func (c *Client) WaitForFunctionDeployment(ctx context.Context, functionID, versionID string, timeoutSec int) error {
	timeout := time.Duration(timeoutSec) * time.Second
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := c.GetFunction(ctx, functionID, versionID)
		if err != nil {
			return err
		}

		if c.config.Debug {
			log.Printf("DEBUG: Function status: %s", resp.Function.Status)
		}

		switch resp.Function.Status {
		case "ACTIVE":
			return nil
		case "ERROR", "FAILED":
			return fmt.Errorf("function deployment failed with status: %s", resp.Function.Status)
		case "DEPLOYING", "INACTIVE", "BUILDING":
			// Continue polling - these are intermediate states
			if c.config.Debug {
				log.Printf("DEBUG: Function still deploying, waiting...")
			}
		default:
			// Unknown status - log it but continue polling
			if c.config.Debug {
				log.Printf("DEBUG: Unknown function status '%s', continuing to poll...", resp.Function.Status)
			}
		}

		// Wait before polling again
		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("function deployment timed out after %d seconds", timeoutSec)
}

// InvokeFunctionRequest represents a function invocation request
type InvokeFunctionRequest struct {
	RequestBody         map[string]interface{} `json:"requestBody"`
	PollDurationSeconds int                    `json:"pollDurationSeconds,omitempty"`
}

// InvokeFunctionResponse represents a function invocation response
type InvokeFunctionResponse struct {
	RequestID    string                 `json:"reqId,omitempty"`
	Status       string                 `json:"status"`
	ResponseBody map[string]interface{} `json:"responseBody,omitempty"`
	Response     map[string]interface{} `json:"response,omitempty"`

	// Response headers from the API
	PercentComplete string `json:"percentComplete,omitempty"`
	LocationURL     string `json:"locationUrl,omitempty"`
}

// InvokeFunctionOptions represents options for function invocation
type InvokeFunctionOptions struct {
	InferenceURL        string // Function inference endpoint, or OpenAI path for LLM functions.
	ModelName           string // OpenAI model name for LLM functions.
	PollDurationSeconds int    // Optional invocation hold-open duration in seconds
}

// InvokeFunction invokes a function.
func (c *Client) InvokeFunction(ctx context.Context, functionID, versionID string, requestBody map[string]interface{}, timeoutSec int) (*InvokeFunctionResponse, error) {
	return c.InvokeFunctionWithOptions(ctx, functionID, versionID, requestBody, timeoutSec, nil)
}

// InvokeFunctionWithOptions invokes a function with additional options using direct invocation
func (c *Client) InvokeFunctionWithOptions(ctx context.Context, functionID, versionID string, requestBody map[string]interface{}, timeoutSec int, options *InvokeFunctionOptions) (*InvokeFunctionResponse, error) {
	if timeoutSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		defer cancel()
	}

	fullURL, isLLM, err := c.invokeURL(ctx, functionID, versionID, options)
	if err != nil {
		return nil, err
	}
	resolvedBody, err := invokeRequestBody(functionID, isLLM, requestBody, options)
	if err != nil {
		return nil, err
	}
	req, err := c.newInvokeRequest(ctx, fullURL, functionID, isLLM, resolvedBody, options)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Read the response body first
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return decodeInvokeFunctionResponse(resp, bodyBytes)
}

func (c *Client) invokeURL(ctx context.Context, functionID, versionID string, options *InvokeFunctionOptions) (string, bool, error) {
	funcDetails, err := c.GetFunctionDetails(ctx, functionID, versionID)
	if err != nil {
		if options != nil && options.InferenceURL != "" {
			fullURL, directErr := c.directInvokeURL(functionID, options.InferenceURL)
			if directErr == nil && options.ModelName != "" {
				log.Printf("WARNING: model-name ignored because function details lookup failed; falling back to direct invocation path")
			}
			return fullURL, false, directErr
		}
		return "", false, fmt.Errorf("failed to get function details for invocation (hint: specify inferenceUrl in config to skip this): %w", err)
	}

	if strings.EqualFold(funcDetails.FunctionType, functionTypeLLM) {
		if options == nil || options.InferenceURL == "" {
			return "", true, fmt.Errorf("inference-url is required when invoking LLM functions")
		}
		fullURL, err := c.llmInvokeURL(options.InferenceURL)
		return fullURL, true, err
	}

	inferenceURL := funcDetails.InferenceURL
	if options != nil && options.InferenceURL != "" {
		inferenceURL = options.InferenceURL
	}
	if inferenceURL == "" {
		return "", false, fmt.Errorf("function has no inferenceUrl configured")
	}
	fullURL, err := c.directInvokeURL(functionID, inferenceURL)
	return fullURL, false, err
}

func (c *Client) invokeBaseURL() string {
	if c.config.BaseInvokeURL != "" {
		return c.config.BaseInvokeURL
	}
	return c.baseURL
}

func (c *Client) directInvokeURL(functionID, inferenceURL string) (string, error) {
	if c.config.InvokeHost != "" {
		return gatewayInvocationURL(c.invokeBaseURL(), inferenceURL)
	}
	return directInvocationURL(c.invokeBaseURL(), functionID, inferenceURL)
}

func (c *Client) llmInvokeURL(inferenceURL string) (string, error) {
	if c.config.InvokeHost != "" {
		return gatewayInvocationURL(c.invokeBaseURL(), inferenceURL)
	}
	return llmInvocationURL(c.invokeBaseURL(), inferenceURL)
}

func llmInvocationURL(baseInvokeURL, inferenceURL string) (string, error) {
	u, err := url.Parse(baseInvokeURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse invoke URL %q: %w", baseInvokeURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invoke URL must include scheme and host: %q", baseInvokeURL)
	}

	hostname := llmInvocationHostname(u.Hostname())
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(hostname, port)
	} else {
		u.Host = hostname
	}
	u.Path = path.Join(u.Path, inferenceURL)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	if strings.HasSuffix(inferenceURL, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

func llmInvocationHost(host string) string {
	u := url.URL{Scheme: "http", Host: host}
	hostname := u.Hostname()
	if hostname == "" {
		return llmInvocationHostname(host)
	}

	llmHost := llmInvocationHostname(hostname)
	if port := u.Port(); port != "" {
		return net.JoinHostPort(llmHost, port)
	}
	return llmHost
}

func llmInvocationHostname(hostname string) string {
	switch {
	case strings.HasPrefix(hostname, "llm."):
		return hostname
	case hostname == "invocation.localhost":
		return "llm.localhost"
	case strings.HasPrefix(hostname, "invocation."):
		return "llm." + hostname
	case strings.HasPrefix(hostname, "api."):
		return "llm.invocation." + strings.TrimPrefix(hostname, "api.")
	default:
		return "llm." + hostname
	}
}

func gatewayInvocationURL(baseInvokeURL, inferenceURL string) (string, error) {
	u, err := url.Parse(baseInvokeURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse invoke URL %q: %w", baseInvokeURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invoke URL must include scheme and host: %q", baseInvokeURL)
	}

	u.Path = path.Join(u.Path, inferenceURL)
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	if strings.HasSuffix(inferenceURL, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

func (c *Client) newInvokeRequest(ctx context.Context, fullURL, functionID string, isLLM bool, requestBody map[string]interface{}, options *InvokeFunctionOptions) (*http.Request, error) {
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set(headerContentType, contentTypeJSON)
	req.Header.Set("Accept", "*/*")
	c.applyInvokeRouting(req, functionID, isLLM)
	applyInvokeOptions(req, options)
	return req, nil
}

func invokeRequestBody(functionID string, isLLM bool, requestBody map[string]interface{}, options *InvokeFunctionOptions) (map[string]interface{}, error) {
	if !isLLM {
		return requestBody, nil
	}
	if options == nil || options.ModelName == "" {
		return nil, fmt.Errorf("model-name is required when invoking LLM functions")
	}

	resolvedModel := functionID + "/" + options.ModelName
	if _, ok := requestBody["model"]; ok {
		log.Printf("WARNING: request body model was overridden with %q for LLM invocation", resolvedModel)
	}

	body := make(map[string]interface{}, len(requestBody)+1)
	for key, value := range requestBody {
		body[key] = value
	}
	body["model"] = resolvedModel
	return body, nil
}

func (c *Client) applyInvokeRouting(req *http.Request, functionID string, isLLM bool) {
	if c.config.InvokeHost == "" {
		return
	}

	if isLLM {
		req.Host = llmInvocationHost(c.config.InvokeHost)
	} else {
		req.Host = functionID + "." + c.config.InvokeHost
	}
	if c.config.Debug {
		log.Printf("DEBUG: Using Invoke Host header override: %s", req.Host)
	}
}

func applyInvokeOptions(req *http.Request, options *InvokeFunctionOptions) {
	if options == nil {
		return
	}

	if options.PollDurationSeconds > 0 {
		req.Header.Set("NVCF-POLL-SECONDS", fmt.Sprintf("%d", options.PollDurationSeconds))
	}
}

func decodeInvokeFunctionResponse(resp *http.Response, bodyBytes []byte) (*InvokeFunctionResponse, error) {
	result := invokeFunctionResponseFromHeaders(resp)
	switch {
	case resp.StatusCode == http.StatusOK:
		if len(bodyBytes) > 0 {
			result.Response = decodeInvokeResponsePayload(bodyBytes, resp.Header.Get(headerContentType))
		}
		return &result, nil
	case resp.StatusCode == http.StatusFound:
		return &result, nil
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(bodyBytes))
	default:
		if len(bodyBytes) > 0 {
			result.ResponseBody = decodeInvokeResponsePayload(bodyBytes, resp.Header.Get(headerContentType))
		}
		return &result, nil
	}
}

func invokeFunctionResponseFromHeaders(resp *http.Response) InvokeFunctionResponse {
	var result InvokeFunctionResponse
	if reqID := resp.Header.Get("NVCF-REQID"); reqID != "" {
		result.RequestID = reqID
	}
	if percent := resp.Header.Get("NVCF-PERCENT-COMPLETE"); percent != "" {
		result.PercentComplete = percent
	}
	if status := resp.Header.Get("NVCF-STATUS"); status != "" {
		result.Status = status
	}
	if location := resp.Header.Get("Location"); location != "" {
		result.LocationURL = location
	}
	return result
}

func decodeInvokeResponsePayload(bodyBytes []byte, contentType string) map[string]interface{} {
	var responseData interface{}
	if err := json.Unmarshal(bodyBytes, &responseData); err != nil {
		return map[string]interface{}{
			"rawResponse": string(bodyBytes),
			"contentType": contentType,
		}
	}

	if respMap, ok := responseData.(map[string]interface{}); ok {
		return respMap
	}
	return map[string]interface{}{
		"result": responseData,
	}
}

// GetHTTPClient returns the underlying HTTP client for advanced usage
func (c *Client) GetHTTPClient() *http.Client {
	return c.httpClient
}

// GetGRPCConn returns the gRPC connection for advanced usage
func (c *Client) GetGRPCConn() *grpc.ClientConn {
	return c.grpcConn
}

// === Extended API Support ===

// ListFunctionIdsResponse represents the response from listing function IDs
type ListFunctionIdsResponse struct {
	FunctionIDs []string `json:"functionIds"`
}

// ListFunctionsResponse represents the response from listing functions
type ListFunctionsResponse struct {
	Functions []FunctionDto `json:"functions"`
}

// FunctionDto represents a function data transfer object
type FunctionDto struct {
	ID                      string                      `json:"id"`
	NCAID                   string                      `json:"ncaId"`
	VersionID               string                      `json:"versionId"`
	Name                    string                      `json:"name"`
	Status                  string                      `json:"status"`
	InferenceURL            string                      `json:"inferenceUrl,omitempty"`
	OwnedByDifferentAccount bool                        `json:"ownedByDifferentAccount,omitempty"`
	InferencePort           int                         `json:"inferencePort,omitempty"`
	ContainerArgs           string                      `json:"containerArgs,omitempty"`
	ContainerEnvironment    []ContainerEnvironmentEntry `json:"containerEnvironment,omitempty"`
	ContainerImage          string                      `json:"containerImage,omitempty"`
	APIBodyFormat           string                      `json:"apiBodyFormat,omitempty"`
	HelmChart               string                      `json:"helmChart,omitempty"`
	HelmChartServiceName    string                      `json:"helmChartServiceName,omitempty"`
	HealthURI               string                      `json:"healthUri,omitempty"`
	CreatedAt               string                      `json:"createdAt"`
	Tags                    []string                    `json:"tags,omitempty"`
	Description             string                      `json:"description,omitempty"`
	Health                  *HealthDto                  `json:"health,omitempty"`
	FunctionType            string                      `json:"functionType"`
	Secrets                 []string                    `json:"secrets,omitempty"`
	RateLimit               *RateLimitDto               `json:"rateLimit,omitempty"`
}

// ClusterGroupsResponse represents the response from listing cluster groups
type ClusterGroupsResponse struct {
	ClusterGroups []ClusterGroup `json:"clusterGroups,omitempty"`
}

// ClusterGroup represents a cluster group
type ClusterGroup struct {
	ID               string    `json:"id,omitempty"`
	Name             string    `json:"name,omitempty"`
	NCAID            string    `json:"ncaId,omitempty"`
	AuthorizedNCAIDs []string  `json:"authorizedNcaIds,omitempty"`
	GPUs             []GPU     `json:"gpus,omitempty"`
	Clusters         []Cluster `json:"clusters,omitempty"`
}

// GPU represents GPU information
type GPU struct {
	Name string `json:"name,omitempty"`
}

// Cluster represents cluster information
type Cluster struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// GetPositionInQueueResponse represents queue position response
type GetPositionInQueueResponse struct {
	Position int `json:"position,omitempty"`
}

// GetQueuesResponse represents queue details response
type GetQueuesResponse struct {
	Queues []QueueDto `json:"queues,omitempty"`
}

// QueueDto represents queue information
type QueueDto struct {
	FunctionID        string `json:"functionId,omitempty"`
	FunctionVersionID string `json:"functionVersionId,omitempty"`
	Size              int    `json:"size,omitempty"`
	EstimatedWaitTime int    `json:"estimatedWaitTime,omitempty"`
}

// === Extended Function Management ===

// ListFunctionIDs retrieves a list of function IDs for the account
func (c *Client) ListFunctionIDs(ctx context.Context) (*ListFunctionIdsResponse, error) {
	// Choose endpoint based on available authentication
	// Use regular endpoint (works with both JWT and API key)
	endpoint := "/v2/nvcf/functions/ids"

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result ListFunctionIdsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// ListFunctions retrieves a list of functions for the account
func (c *Client) ListFunctions(ctx context.Context) (*ListFunctionsResponse, error) {
	// Per OpenAPI spec, list functions uses /v2/nvcf/functions endpoint
	// This endpoint accepts both API key (list_functions scope) and JWT token
	endpoint := "/v2/nvcf/functions"

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result ListFunctionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// ListFunctionVersions retrieves all versions of a specific function
func (c *Client) ListFunctionVersions(ctx context.Context, functionID string) (*ListFunctionsResponse, error) {
	// Use regular endpoint (works with both JWT and API key, requires list_functions scope)
	endpoint := fmt.Sprintf("/v2/nvcf/functions/%s/versions", url.PathEscape(functionID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result ListFunctionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetFunctionDetails retrieves details for a specific function version
func (c *Client) GetFunctionDetails(ctx context.Context, functionID, versionID string) (*FunctionDto, error) {
	endpoint := fmt.Sprintf(functionVersionEndpointFormat, url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	// API returns wrapped response: {"function": {...}}
	var wrapper struct {
		Function FunctionDto `json:"function"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrapper); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &wrapper.Function, nil
}

// === Cluster Groups ===

// ListClusterGroups retrieves available cluster groups
func (c *Client) ListClusterGroups(ctx context.Context) (*ClusterGroupsResponse, error) {
	resp, err := c.makeRequest(ctx, "GET", "/v2/nvcf/clusterGroups", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result ClusterGroupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// === Queue Management ===

// GetQueuePosition retrieves the position in queue for a request
func (c *Client) GetQueuePosition(ctx context.Context, requestID string) (*GetPositionInQueueResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/queues/%s/position", url.PathEscape(requestID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result GetPositionInQueueResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetQueueDetails retrieves queue details for a function
func (c *Client) GetQueueDetails(ctx context.Context, functionID string) (*GetQueuesResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/queues/functions/%s", url.PathEscape(functionID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result GetQueuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// GetQueueDetailsForVersion retrieves queue details for a specific function version
func (c *Client) GetQueueDetailsForVersion(ctx context.Context, functionID, versionID string) (*GetQueuesResponse, error) {
	endpoint := fmt.Sprintf("/v2/nvcf/queues/functions/%s/versions/%s", url.PathEscape(functionID), url.PathEscape(versionID))

	resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf(apiErrorFormat, resp.StatusCode, string(body))
	}

	var result GetQueuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}
