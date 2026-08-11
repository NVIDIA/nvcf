/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package config

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
)

type CliArgs struct {
	rootCmd  *cobra.Command
	vpr      *viper.Viper
	sugarLog *otelzap.SugaredLogger
}

func NewCliArgs(rootCmd *cobra.Command, vpr *viper.Viper, sugarLog *otelzap.SugaredLogger) *CliArgs {
	return &CliArgs{
		rootCmd:  rootCmd,
		vpr:      vpr,
		sugarLog: sugarLog,
	}
}

func (c *CliArgs) Register(svcName string) {
	c.SetupAuth()
	c.SetupPorts()
	c.SetupDatabase()
	c.SetupTelemetry(svcName)
	c.SetupTracing()
	c.SetupLogging(svcName)
	c.SetupIndexer()
	c.SetupProfiling()
	c.SetupPublisher()
	c.SetupHTTP()
	c.SetupClientCredentials()
}

func (c *CliArgs) SetupLogging(svcName string) {
	c.string("logging.level", "info", "Logging level (options: debug, info, warn, error)", true)
	c.string("logging.zap-configuration", "development", "Zap configuration (options: development, production)", true)
}

func (c *CliArgs) SetupPorts() {
	c.int("service.api-port", 8080, "Port where API will be served", true)
	c.int("service.internal-port", 8081, "Internal port to service metrics", true)
}

func (c *CliArgs) SetupAuth() {
	c.bool("disable-authentication", false, "Disable authentication", false)
	c.string("auth.provider", "jwt", "Authentication provider (options: jwt, policy)", true)
	c.string("auth.jwk-set-url", "", "JWKS URL", true)
	c.string("auth.issuer", "", "Expected JWT issuer", true)
	c.string("auth.audience", "", "Expected JWT audience", true)
	c.string("auth.tenant-claim", "", "JWT claim containing the authorized tenant or tenants", true)
	c.int("auth.cache-refresh-interval", 900, "Authn JWK cache refresh interval in seconds", true)
	c.string("auth.policy.namespace", "", "Policy namespace", true)
	c.string("auth.policy.policy-fqdn", "", "Policy FQDN", true)
	c.string("auth.policy.token-issuer-addr", "", "Token issuer address", true)
	c.string("auth.policy.policy-evaluator-addr", "", "Policy evaluator address", true)
	c.string("auth.policy.client-id", "", "OAuth client ID", true)
	c.string("auth.policy.client-secret", "", "OAuth client secret", true)
	c.string("auth.policy.creds-file", "", "Path to credentials file for OAuth client credentials (enables hot-reload)", true)
	c.int64("auth.policy.creds-refresh-interval", 300, "Interval in seconds to periodically refresh credentials from file (recommended: 300)", true)
	c.string("auth.policy.subject-field", "subject", "Policy input field name for JWT subject", true)
	c.string("auth.policy.api-key-field", "apiKey", "Policy input field name for API key tokens", true)
}

// SetupDatabase defines database providers arguments and configuration settings
func (c *CliArgs) SetupDatabase() {
	// Database Provider
	c.string("database.provider", "cassandra", "Database provider (options: cassandra)", true)

	// Cassandra
	c.stringSlice("database.cassandra.hosts", []string{"localhost"}, "A comma-separated list of database hosts", true)
	c.int("database.cassandra.port", 9042, "Cassandra port", true)
	c.string("database.cassandra.keyspace", "app", "Cassandra keyspace", true)
	c.string("database.cassandra.username", "", "Cassandra username", true)
	c.string("database.cassandra.password", "", "Cassandra password", true)
	c.string("database.cassandra.consistency", "LOCAL_QUORUM", "Cassandra consistency (options: QUORUM, LOCAL_QUORUM)", true)
	c.int("database.cassandra.num-conns", 2, "Connections per host", true)
	c.string("database.cassandra.pub-key-b64", "", "Cassandra public key as base64-encoded string", true)
	c.string("database.cassandra.priv-key-b64", "", "Cassandra private key as base64-encoded string", true)
	c.string("database.cassandra.pub-key-path", "", "Cassandra public key path", true)
	c.string("database.cassandra.priv-key-path", "", "Cassandra private key path", true)
	c.bool("database.cassandra.insecure-skip-verify", false, "Insecurely skip certificate verification", true)
}

func (c *CliArgs) SetupTelemetry(svcName string) {
	// Global settings
	c.string("telemetry.servicename", svcName, "Service name reported in telemetry", true)
	c.string("telemetry.serviceversion", "", "Service version reported in telemetry, e.g. 1.0.0", true)
	c.string("telemetry.environmentname", "", "Deployment environment reported in telemetry", true)
}

func (c *CliArgs) SetupTracing() {
	// Enable tracing
	c.bool("tracing.enabled", false, "Enable distributed tracing", true)

	// Tracing provider
	c.string("tracing.provider", "jaeger", "Tracing provider (options: lightstep, jaeger)", true)

	// Global settings
	c.bool("tracing.https", false, "Connect to tracing endpoint using HTTPS", true)
	c.string("tracing.level", "info", "Tracing level (options: debug, info, warn, error)", true)

	// Jaeger
	c.string("tracing.jaeger.endpoint", "localhost:4317", "Jaeger endpoint", true)

	// Lightstep
	c.string("tracing.lightstep.endpoint", "", "Lightstep endpoint", true)
	c.string("tracing.lightstep.token", "", "Lightstep access token", true)
}

func (c *CliArgs) SetupProfiling() {
	// Enable pprof server
	c.bool("enable-profiling", false, "Enable profiling endpoint", false)
}

func (c *CliArgs) SetupIndexer() {
	c.bool("indexer.enabled", false, "Enable the indexer service", true)
	c.int("indexer.channel-buffer-size", 1000, "Buffer size for indexer channels", true)
	c.int("indexer.worker-count", 10, "Number of indexer workers", true)
}

func (c *CliArgs) SetupPublisher() {
	// Batched Publisher
	c.int("publisher.batched.queue-size", 100000, "Publisher queue size", true)
	c.int("publisher.batched.batch-size", 1000, "Publisher batch size", true)
	c.int("publisher.batched.batch-interval-seconds", 10, "Publisher batch interval in seconds", true)
	// CloudEvents Storage Client
	c.bool("publisher.cloudevents.enabled", true, "Enable CloudEvents client", true)
	c.bool("disable-cloudevent", false, "Disable CloudEvents client", false)
	c.string("publisher.cloudevents.endpoint", "", "HTTP endpoint for CloudEvents batch publishing", true)
	c.string("publisher.cloudevents.token_endpoint", "", "Optional token endpoint for CloudEvents authentication", true)
	c.string("publisher.cloudevents.creds_file", "", "Path to credentials file for CloudEvents OAuth client (enables hot-reload)", true)
	c.int64("publisher.cloudevents.creds_refresh_interval", 0, "Interval in seconds to periodically refresh credentials from file (recommended: 300)", true)
	c.string("publisher.cloudevents.client_id", "", "OAuth client ID for CloudEvents (used when creds_file is not specified)", true)
	c.string("publisher.cloudevents.client_secret", "", "OAuth client secret for CloudEvents (used when creds_file is not specified)", true)
}

func (c *CliArgs) SetupHTTP() {
	c.int("http.max-idle-conns", 100, "Maximum number of idle HTTP connections across all hosts", true)
	c.int("http.max-idle-conns-per-host", 100, "Maximum number of idle HTTP connections per host", true)
	c.int("http.idle-conn-timeout-sec", 90, "Maximum time in seconds that an idle connection will remain idle before closing", true)
	c.int("http.tls-handshake-timeout-sec", 10, "Maximum time in seconds for a TLS handshake", true)
	c.int("http.expect-continue-timeout-sec", 1, "Maximum time in seconds to wait for a server's first response headers", true)
}

func (c *CliArgs) SetupClientCredentials() {
	c.string("id", "", "OAuth client ID", true)
	c.string("secret", "", "OAuth client secret", true)
}

func (c *CliArgs) string(name string, defaultValue string, usage string, bindToFlag bool) {
	c.rootCmd.Flags().String(name, defaultValue, usage)
	if bindToFlag {
		err := c.vpr.BindPFlag(name, c.rootCmd.Flags().Lookup(name))
		if err != nil {
			c.sugarLog.Fatal(err)
		}
	}
}

func (c *CliArgs) stringSlice(name string, defaultValue []string, usage string, bindToFlag bool) {
	c.rootCmd.Flags().StringSlice(name, defaultValue, usage)
	if bindToFlag {
		err := c.vpr.BindPFlag(name, c.rootCmd.Flags().Lookup(name))
		if err != nil {
			c.sugarLog.Fatal(err)
		}
	}
}

func (c *CliArgs) int(name string, defaultValue int, usage string, bindToFlag bool) {
	c.rootCmd.Flags().Int(name, defaultValue, usage)
	if bindToFlag {
		err := c.vpr.BindPFlag(name, c.rootCmd.Flags().Lookup(name))
		if err != nil {
			c.sugarLog.Fatal(err)
		}
	}
}

func (c *CliArgs) int64(name string, defaultValue int64, usage string, bindToFlag bool) {
	c.rootCmd.Flags().Int64(name, defaultValue, usage)
	if bindToFlag {
		err := c.vpr.BindPFlag(name, c.rootCmd.Flags().Lookup(name))
		if err != nil {
			c.sugarLog.Fatal(err)
		}
	}
}

func (c *CliArgs) bool(name string, defaultValue bool, usage string, bindToFlag bool) {
	c.rootCmd.Flags().Bool(name, defaultValue, usage)
	if bindToFlag {
		err := c.vpr.BindPFlag(name, c.rootCmd.Flags().Lookup(name))
		if err != nil {
			c.sugarLog.Fatal(err)
		}
	}
}
