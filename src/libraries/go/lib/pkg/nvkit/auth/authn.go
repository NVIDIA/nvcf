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

package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc/credentials"
	"gopkg.in/yaml.v3"

	nverrors "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

var (
	authnRefreshErrorCounter = promauto.NewCounterVec(prometheus.CounterOpts{Name: "nv_client_authn_refresh_errors_total"}, []string{"client_name", "client_id"})
)

// AuthnConfig holds all config necessary to fetch access tokens from authn server
type AuthnConfig struct {
	OIDCConfig    *ProviderConfig `mapstructure:"oidc"`
	RefreshConfig *RefreshConfig  `mapstructure:"refresh"`

	// tell grpc if this auth config requires transport security
	DisableTransportSecurity bool `mapstructure:"disableTransportSecurity"`

	// ignoreFileWatcherEvents when true, file watcher events will be ignored.
	// Used for testing to isolate periodic refresh behavior. Default is false (events processed).
	ignoreFileWatcherEvents atomic.Bool
	// ignoreTickerEvents when true, periodic ticker events will be ignored.
	// Used for testing to isolate file watcher behavior. Default is false (events processed).
	ignoreTickerEvents atomic.Bool
}

// ProviderConfig holds all configuration needed to generate OIDC device tokens for this client's requests.
// This is used if requests have to be authenticated using OIDC auth - typically for out-of-cluster access.
type ProviderConfig struct {
	// Required - Host is the hostname of the authn server.
	Host string
	// Optional - CredentialsFile is the containing Client ID/Secret
	CredentialsFile string
	// Optional - ClientID is used for OIDC authentication of requests
	ClientID string
	// Optional - ClientSecret to use for OIDC authentication of requests
	ClientSecret string //nolint:gosec // G117: false positive - this is a configuration struct
	// Required - Scope(s) to request
	Scopes []string
	// Client name to use for authn setup
	ClientName string `mapstructure:",omitempty"`
	// Mutex for handling any credentials changes
	credsMutex sync.RWMutex
}

type ClientCredentials struct {
	// Required - ClientID is used for OIDC authentication of requests
	ClientID string `yaml:"id"`
	// Required - ClientSecret to use for OIDC authentication of requests
	ClientSecret string `yaml:"secret"` //nolint:gosec // G117: false positive - this is a configuration struct
}

// RefreshConfig Used to describe all vars needed for StartAsyncRefresh, extendable
type RefreshConfig struct {
	// Optional - Interval to use for token refresh. If not provided, background token refresh is not enabled.
	Interval int64
	// Optional - CredentialsRefreshInterval is the interval in seconds to periodically re-read the credentials file.
	// This is more reliable than file watching alone, as it handles atomic file replacements (e.g., vault-agent).
	// Recommended value: 300 (5 minutes). If 0, periodic refresh is disabled and only file watching is used.
	CredentialsRefreshInterval int64 `mapstructure:"credentialsRefreshInterval"`
}

// AuthnRefresher allows for registering a callback if clients need to be notified of credentials change due to hot-reload of config
type AuthnRefresher interface {
	Update(creds *ClientCredentials)
}

// HttpClientWithAuth - Adds an oauth2 middleware that automatically fetches and refreshes token based on the config
func (cfg *AuthnConfig) HttpClientWithAuth(client *http.Client) (*http.Client, error) {
	if err := cfg.validateConfig(); err != nil {
		if errors.Is(err, nverrors.ErrUninitializedConfig) {
			zap.L().Info("Skipping auth setup", zap.Error(err))
			return client, nil
		}
		return nil, err
	}

	if cfg.OIDCConfig.ClientName == "" {
		cfg.OIDCConfig.ClientName = cfg.OIDCConfig.ClientID
	}

	client = cfg.getHttpAuthNClient(client)

	// The client returned here automatically adds the oauth2 client-credentials based middleware capability
	return client, nil
}

func (cfg *AuthnConfig) validateConfig() error {
	// NOTE: Treat unset OIDCConfig.Host as un-initialized auth config
	if cfg == nil || cfg.OIDCConfig == nil || cfg.OIDCConfig.Host == "" {
		return nverrors.ErrUninitializedConfig
	}

	if cfg.OIDCConfig.CredentialsFile != "" {
		if cfg.OIDCConfig.ClientID != "" || cfg.OIDCConfig.ClientSecret != "" {
			return &nverrors.ConfigError{Message: "unsupported credentials format - either creds-file or client-id/client-secret should be provided"}
		}
		contents, err := os.ReadFile(cfg.OIDCConfig.CredentialsFile)
		if err != nil {
			return &nverrors.ConfigError{Message: fmt.Sprintf("cannot read client-credentials file: %+v, err: %+v", cfg.OIDCConfig.CredentialsFile, err)}
		}
		credentials := &ClientCredentials{}
		if err = yaml.Unmarshal(contents, credentials); err != nil {
			return fmt.Errorf("cannot unmarshal client-credentials file contents: %+v, err: %+v", cfg.OIDCConfig.CredentialsFile, err)
		}
		cfg.OIDCConfig.ClientID = credentials.ClientID
		cfg.OIDCConfig.ClientSecret = credentials.ClientSecret
	}
	if cfg.OIDCConfig.ClientID == "" {
		return &nverrors.ConfigError{FieldName: "oidc.client-id", Message: "not found"}
	}
	if cfg.OIDCConfig.ClientSecret == "" {
		return &nverrors.ConfigError{FieldName: "oidc.client-secret", Message: "not found"}
	}

	if cfg.RefreshConfig == nil {
		cfg.RefreshConfig = &RefreshConfig{}
	}
	if cfg.RefreshConfig.Interval < 0 {
		return &nverrors.ConfigError{FieldName: "refresh.interval", Message: fmt.Sprintf("client-id: %+v, interval: %d", cfg.OIDCConfig.ClientID, cfg.RefreshConfig.Interval)}
	}

	return nil
}

func (cfg *AuthnConfig) RefreshHttpAuthNClient(client *http.Client) *http.Client {
	return cfg.getHttpAuthNClient(client)
}

// UpdateCredentials safely updates the OIDC credentials with proper mutex protection.
// Returns true if credentials were actually changed, false if they were already the same.
// This method is thread-safe and should be used instead of directly modifying OIDCConfig fields.
func (cfg *AuthnConfig) UpdateCredentials(creds *ClientCredentials) bool {
	if cfg == nil || cfg.OIDCConfig == nil || creds == nil {
		return false
	}

	cfg.OIDCConfig.credsMutex.Lock()
	defer cfg.OIDCConfig.credsMutex.Unlock()

	changed := false
	if cfg.OIDCConfig.ClientID != creds.ClientID {
		cfg.OIDCConfig.ClientID = creds.ClientID
		changed = true
	}
	if cfg.OIDCConfig.ClientSecret != creds.ClientSecret {
		cfg.OIDCConfig.ClientSecret = creds.ClientSecret
		changed = true
	}

	return changed
}

// GetCredentials safely reads the OIDC credentials with proper mutex protection.
// This method is thread-safe and should be used instead of directly reading OIDCConfig fields
// when credentials may be updated concurrently by SetupCredentialsFileWatcher.
func (cfg *AuthnConfig) GetCredentials() (clientID, clientSecret string) {
	if cfg == nil || cfg.OIDCConfig == nil {
		return "", ""
	}

	cfg.OIDCConfig.credsMutex.RLock()
	defer cfg.OIDCConfig.credsMutex.RUnlock()

	return cfg.OIDCConfig.ClientID, cfg.OIDCConfig.ClientSecret
}

func (cfg *AuthnConfig) getHttpAuthNClient(client *http.Client) *http.Client {
	config := &clientcredentials.Config{
		ClientID:     cfg.OIDCConfig.ClientID,
		ClientSecret: cfg.OIDCConfig.ClientSecret,
		Scopes:       cfg.OIDCConfig.Scopes,
		TokenURL:     fmt.Sprintf("%s/token", cfg.OIDCConfig.Host),
	}

	// Adding this HTTPClient value in the context allows the oauth2 library to use the same httpClient
	// and hence the same cert/any other http transport configs while reaching out the oauth2 server
	ctx, _ := otel.GetTracerProvider().Tracer("authn").Start(context.Background(), "refresh")
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	client = config.Client(ctx)
	// If refresh interval is configured, get a client with async token refreshing capabilities
	if cfg.RefreshConfig.Interval > 0 {
		refresher := NewConfigAndStartRefresher(ctx, config, cfg.RefreshConfig.Interval)
		client = refresher.Client(ctx)
	}
	return client
}

// SetupCredentialsFileWatcher - sets up the file watcher on creds-file if provided for client setup.
// It also supports periodic re-reading of the credentials file via RefreshConfig.CredentialsRefreshInterval,
// which is more reliable than file watching alone for atomic file replacements (e.g., vault-agent).
func (cfg *AuthnConfig) SetupCredentialsFileWatcher(refresher AuthnRefresher) {
	if cfg.OIDCConfig.CredentialsFile == "" || refresher == nil {
		zap.L().Info("Skipping creds-file file watch setup")
		return
	}

	// creates a new file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		zap.L().Error("Error setting up file watcher", zap.String("creds-file", cfg.OIDCConfig.CredentialsFile), zap.Error(err))
		return
	}
	defer watcher.Close()

	// Set up periodic credentials refresh ticker if configured
	// This handles atomic file replacements (mv/rename) that fsnotify misses when watching the file directly
	var credsTicker *time.Ticker
	var credsTickerChan <-chan time.Time
	if cfg.RefreshConfig != nil && cfg.RefreshConfig.CredentialsRefreshInterval > 0 {
		credsTicker = time.NewTicker(time.Duration(cfg.RefreshConfig.CredentialsRefreshInterval) * time.Second)
		credsTickerChan = credsTicker.C
		zap.L().Info("Periodic credentials refresh enabled",
			zap.String("creds-file", cfg.OIDCConfig.CredentialsFile),
			zap.Int64("interval_seconds", cfg.RefreshConfig.CredentialsRefreshInterval))
		defer credsTicker.Stop()
	}

	done := make(chan bool)
	go func() {
		defer close(done)
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	loop:
		for {
			select {
			// watch for events
			case event := <-watcher.Events:
				zap.L().Debug("Received file watcher event", zap.String("creds-file", event.Name), zap.Any("event", event))
				if !event.Op.Has(fsnotify.Write) {
					// In the case of any other events, file gets removed from the watcher. Add it again to continue receiving changes to the credentials file
					// See https://github.com/fsnotify/fsnotify/issues/363#issuecomment-993622433
					if err := watcher.Add(event.Name); err != nil {
						zap.L().Error("Error from watcher.Add", zap.Error(err))
						continue
					}
				}
				if cfg.ignoreFileWatcherEvents.Load() {
					zap.L().Debug("Ignoring file watcher event (disabled for testing)")
					continue
				}
				cfg.reloadCredentialsFile(refresher)
			// periodic credentials refresh - handles atomic file replacements that fsnotify misses
			case <-credsTickerChan:
				zap.L().Debug("Periodic credentials refresh triggered", zap.String("creds-file", cfg.OIDCConfig.CredentialsFile))
				if cfg.ignoreTickerEvents.Load() {
					zap.L().Debug("Ignoring ticker event (disabled for testing)")
					continue
				}
				cfg.reloadCredentialsFile(refresher)
			// watch for errors
			case err := <-watcher.Errors:
				zap.L().Error("Error from watcher", zap.String("creds-file", cfg.OIDCConfig.CredentialsFile), zap.Error(err))
			case sig := <-c:
				zap.L().Info("Received signal in file watcher", zap.String("os-signal", sig.String()))
				break loop
			}
		}
	}()

	if err = watcher.Add(cfg.OIDCConfig.CredentialsFile); err != nil {
		zap.L().Error("File watch failed", zap.String("creds-file", cfg.OIDCConfig.CredentialsFile), zap.Error(err))
	}
	cfg.OIDCConfig.credsMutex.RLock()
	zap.L().Info("Credentials file watcher setup done",
		zap.String("client_name", cfg.OIDCConfig.ClientName),
		zap.String("client_id", cfg.OIDCConfig.ClientID),
		zap.String("creds-file", cfg.OIDCConfig.CredentialsFile))
	cfg.OIDCConfig.credsMutex.RUnlock()
	<-done
}

// reloadCredentialsFile reads the credentials file and updates the config if changed
func (cfg *AuthnConfig) reloadCredentialsFile(refresher AuthnRefresher) {
	contents, err := os.ReadFile(cfg.OIDCConfig.CredentialsFile)
	if err != nil {
		authnRefreshErrorCounter.With(map[string]string{"client_name": cfg.OIDCConfig.ClientName, "client_id": cfg.OIDCConfig.ClientID}).Inc()
		zap.L().Error("Cannot read client-credentials", zap.String("creds-file", cfg.OIDCConfig.CredentialsFile), zap.Error(err))
		return
	}
	credentials := &ClientCredentials{}
	if err = yaml.Unmarshal(contents, credentials); err != nil {
		authnRefreshErrorCounter.With(map[string]string{"client_name": cfg.OIDCConfig.ClientName, "client_id": cfg.OIDCConfig.ClientID}).Inc()
		zap.L().Error("Cannot unmarshal client-credentials", zap.String("creds-file", cfg.OIDCConfig.CredentialsFile), zap.Error(err))
		return
	}
	// Update credentials using thread-safe method
	shouldRefresh := cfg.UpdateCredentials(credentials)

	if shouldRefresh {
		refresher.Update(credentials)
	}
}

// GRPCClientWithAuth - Adds an oauth2 middleware that automatically fetches and refreshes token based on the config
func (cfg *AuthnConfig) GRPCClientWithAuth() (credentials.PerRPCCredentials, error) {
	if err := cfg.validateConfig(); err != nil {
		if errors.Is(err, nverrors.ErrUninitializedConfig) {
			zap.L().Info("Skipping auth setup", zap.Error(err))
			return nil, nil
		}
		return nil, err
	}

	config := &clientcredentials.Config{
		ClientID:     cfg.OIDCConfig.ClientID,
		ClientSecret: cfg.OIDCConfig.ClientSecret,
		Scopes:       cfg.OIDCConfig.Scopes,
		TokenURL:     fmt.Sprintf("%s/token", cfg.OIDCConfig.Host),
	}

	options := make([]Option, 0)

	// this option ensures that the new tokenSource won't force the use of TLS
	if cfg.DisableTransportSecurity {
		options = append(options, DisableTransportSecurity)
	}

	grpcTokenSource := NewGRPCOauth2TokenSource(config, options...)
	go cfg.SetupCredentialsFileWatcher(grpcTokenSource)

	return grpcTokenSource, nil
}

func (cfg *AuthnConfig) AddClientFlags(cmd *cobra.Command, clientName string) bool {
	if cmd == nil || cfg == nil {
		return false
	}
	prefix := clientName
	if clientName == "" {
		prefix = "authn"
	}
	clientFlag := func(flag string) string {
		return fmt.Sprintf("%s.%s", prefix, flag)
	}
	cfg.OIDCConfig = &ProviderConfig{ClientName: clientName}
	cmd.Flags().StringVarP(&cfg.OIDCConfig.Host, clientFlag("oidc.host"), "", "", "AuthN host")
	cmd.Flags().StringVarP(&cfg.OIDCConfig.ClientID, clientFlag("oidc.client-id"), "", "", "Client ID to use during authentication")
	cmd.Flags().StringVarP(&cfg.OIDCConfig.ClientSecret, clientFlag("oidc.client-secret"), "", "", "Client secret to use during authentication")
	cmd.Flags().StringVarP(&cfg.OIDCConfig.CredentialsFile, clientFlag("oidc.creds-file"), "", "",
		"Credentials file with client-id/secret. Either creds-file or client-id/client-secret can be provided.")
	cmd.Flags().StringSliceVarP(&cfg.OIDCConfig.Scopes, clientFlag("oidc.scopes"), "", []string{}, "Authentication scopes")

	cfg.RefreshConfig = &RefreshConfig{}
	cmd.Flags().Int64VarP(&cfg.RefreshConfig.Interval, clientFlag("refresh.interval"), "", 0, "Interval by seconds, if not specified, its on-demand refresh")
	return true
}
