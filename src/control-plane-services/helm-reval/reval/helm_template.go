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

package reval

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/image-credential-helper/credhelper"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/release"

	orasremote "oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	orascredentials "oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/NVIDIA/nvcf/src/control-plane-services/helm-reval/pkg/telemetry/metrics"
)

func (h *Handler) runHelmTemplate(
	ctx context.Context,
	cfg Config,
	settings *cli.EnvSettings,
	workingDir string,
	out io.Writer,
) error {
	ctx, span := otel.Tracer(h.ServiceName).Start(
		ctx,
		"reval.handler.helm.runTemplate",
	)
	defer span.End()

	logger := logFromContext(ctx)

	helmCredsStore := credhelper.NewCredentialStore()
	helmImageClient := newORASClient(h.httpClient, helmCredsStore)
	// The resettable cache lets this code save an API call to re-auth after each API call
	// while getting around invalid cache entries for the same host.
	helmImageClientCache := newAuthCache()
	helmImageClient.Cache = helmImageClientCache

	helmRegistryClient, err := newRegistryClient(ctx, settings, helmImageClient)
	if err != nil {
		return err
	}

	actionConfig := &action.Configuration{
		RegistryClient: helmRegistryClient,
	}
	const helmDriver = "memory"
	if err := actionConfig.Init(
		settings.RESTClientGetter(), cfg.Namespace,
		helmDriver, logger.Sugar().Debugf,
	); err != nil {
		return err
	}
	client := helmInstallClient{
		Install:              action.NewInstall(actionConfig),
		helmImageClient:      helmImageClient,
		helmCredsStore:       helmCredsStore,
		helmImageClientCache: helmImageClientCache,
	}

	if cfg.K8sVersion != "" {
		// Already validated.
		client.KubeVersion, _ = chartutil.ParseKubeVersion(cfg.K8sVersion)
	}

	valuesJSON, err := cfg.Values.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal values: %v", err)
	}
	valuesFile, err := os.Create(filepath.Join(workingDir, "values.json"))
	if err != nil {
		return err
	}
	defer valuesFile.Close()
	if _, err := io.Copy(valuesFile, bytes.NewReader(valuesJSON)); err != nil {
		return fmt.Errorf("write values file: %v", err)
	}
	valueOpts := &values.Options{
		ValueFiles: []string{valuesFile.Name()},
	}

	client.DryRunOption = "true"
	client.DryRun = true
	client.Namespace = cfg.Namespace
	client.ReleaseName = cfg.ReleaseName
	client.Replace = true
	client.ClientOnly = true
	client.APIVersions = chartutil.VersionSet(cfg.APIVersions)
	client.IncludeCRDs = true
	client.DisableHooks = true
	// Allow devel versions.
	client.Devel = true
	client.EnableDNS = false
	client.RepoURL = ""

	rel, err := h.runDownload(ctx, cfg, settings, client, valueOpts, out)
	if err != nil {
		return err
	}

	_, err = io.WriteString(out, rel.Manifest)
	if err != nil {
		return err
	}

	return nil
}

type helmInstallClient struct {
	*action.Install
	helmCredsStore       orascredentials.Store
	helmImageClient      orasauth.Client
	helmImageClientCache *resettableAuthCache
}

func initHelmEnv() (settings *cli.EnvSettings, tmpDir string, err error) {
	if tmpDir, err = os.MkdirTemp("", "reval.*"); err != nil {
		return nil, "", err
	}

	cacheHomeDir := filepath.Join(tmpDir, ".helmcache")
	configHomeDir := filepath.Join(tmpDir, ".helmconfig")
	dataHomeDir := filepath.Join(tmpDir, ".helmdata")

	settings = cli.New()
	settings.PluginsDirectory = filepath.Join(dataHomeDir, "plugins")
	settings.RegistryConfig = filepath.Join(configHomeDir, "registry", "config.json")
	settings.RepositoryConfig = filepath.Join(configHomeDir, "repository", "repositories.yaml")
	settings.RepositoryCache = filepath.Join(cacheHomeDir, "repository")

	for _, dir := range []string{
		settings.PluginsDirectory,
		filepath.Dir(settings.RegistryConfig),
		filepath.Dir(settings.RepositoryConfig),
		settings.RepositoryCache,
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, "", err
		}
	}

	// The registry config must be non-empty at first.
	rcfgFile, err := os.OpenFile(settings.RegistryConfig, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return nil, "", err
	}
	defer rcfgFile.Close()

	if _, err := rcfgFile.WriteString("{}"); err != nil {
		return nil, "", err
	}

	return settings, tmpDir, nil
}

// For testing
var plainHTTP bool

func newRegistryClient(ctx context.Context,
	settings *cli.EnvSettings,
	authorizer orasauth.Client,
) (*registry.Client, error) {
	logger := logFromContext(ctx)

	// The registry client writes some human-readable stuff to its writer,
	// so wrap in the logger for standardization.
	var clientWriter = io.Discard
	if logger.Level().Enabled(zap.DebugLevel) {
		clientWriter = printLogWriter{logger: logger.WithOptions(zap.AddCallerSkip(3))}
	}

	opts := []registry.ClientOption{
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptWriter(clientWriter),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
		registry.ClientOptHTTPClient(authorizer.Client),
		registry.ClientOptAuthorizer(authorizer),
	}
	if plainHTTP {
		opts = append(opts, registry.ClientOptPlainHTTP())
	}

	// Create a new registry client
	registryClient, err := registry.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return registryClient, nil
}

type printLogWriter struct {
	logger *zap.Logger
}

func (w printLogWriter) Write(b []byte) (int, error) {
	w.logger.Debug(string(bytes.TrimSpace(b)))
	return len(b), nil
}

func newORASClient(
	httpClient *http.Client,
	credsStore orascredentials.Store,
) orasauth.Client {
	authorizer := orasauth.Client{
		Client:     httpClient,
		Credential: orascredentials.Credential(credsStore),
	}
	authorizer.SetUserAgent("helm-reval/0.1.0")
	return authorizer
}

func (h *Handler) runDownload(
	ctx context.Context,
	cfg Config,
	settings *cli.EnvSettings,
	client helmInstallClient,
	valueOpts *values.Options,
	out io.Writer,
) (rel *release.Release, err error) {
	tracer := otel.Tracer(h.ServiceName)
	chart := cfg.ChartURL

	ctx, span := tracer.Start(
		ctx,
		"reval.handler.helm.runDownload",
		trace.WithAttributes(
			attribute.String("helmChart", chart),
		),
	)
	defer span.End()

	timer := metrics.RunTimerFromContext(ctx)
	timer.RecordHelmDownloadStart()
	defer timer.RecordHelmDownloadEnd()

	logger := logFromContext(ctx)

	cp, err := h.tryLocateChart(ctx, cfg, settings, client)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}

	logger.Debug("Downloaded chart")

	p := getter.All(settings)
	vals, err := valueOpts.MergeValues(p)
	if err != nil {
		return nil, err
	}

	// Check chart dependencies to make sure all are present in /charts
	chartRequested, err := loader.Load(cp)
	if err != nil {
		return nil, err
	}

	if err := checkIfInstallable(chartRequested); err != nil {
		return nil, err
	}

	if req := chartRequested.Metadata.Dependencies; req != nil {
		// If CheckDependencies returns an error, we have unfulfilled dependencies.
		// As of Helm 2.4.0, this is treated as a stopping condition:
		// https://github.com/helm/helm/issues/2209
		if err := action.CheckDependencies(chartRequested, req); err != nil {
			err = fmt.Errorf("an error occurred while checking for chart dependencies. You may need to run `helm dependency build` to fetch missing dependencies: %v", err)
			if client.DependencyUpdate {
				man := &downloader.Manager{
					Out:              out,
					ChartPath:        cp,
					Keyring:          client.Keyring,
					SkipUpdate:       false,
					Getters:          p,
					RepositoryConfig: settings.RepositoryConfig,
					RepositoryCache:  settings.RepositoryCache,
					Debug:            settings.Debug,
					RegistryClient:   client.GetRegistryClient(),
				}
				if err := man.Update(); err != nil {
					return nil, err
				}
				// Reload the chart with the updated Chart.lock file.
				if chartRequested, err = loader.Load(cp); err != nil {
					return nil, fmt.Errorf("failed reloading chart after repo update: %v", err)
				}
			} else {
				return nil, err
			}
		}
	}

	return client.RunWithContext(ctx, chartRequested, vals)
}

var ngcHostRe = regexp.MustCompile(`^(.+\.)?ngc\.nvidia\.com$`)

const legacyUsername = "$oauthtoken"

// Since there is no easy way to HEAD a chart URL directly (depends on registry implementation),
// tryLocateChart attempts each configured credential to GET the chart until one succeeds.
func (h *Handler) tryLocateChart(
	ctx context.Context,
	cfg Config,
	settings *cli.EnvSettings,
	client helmInstallClient,
) (cp string, err error) {
	tracer := otel.Tracer(h.ServiceName)
	chart := cfg.ChartURL

	logger := logFromContext(ctx)

	checkFuncs := []func() error{}

	u, err := url.Parse(chart)
	if err != nil {
		return "", err
	}
	host := u.Host

	// Backwards compatibility.
	if cfg.NGCAPIKey != "" && ngcHostRe.MatchString(host) {
		checkFuncs = append(checkFuncs, func() (err error) {
			defer client.helmImageClientCache.reset()

			_, span := tracer.Start(
				ctx,
				"reval.handler.helm.locateChartWithCreds",
				trace.WithAttributes(
					attribute.Bool("legacy", true),
				),
			)
			defer span.End()

			logger.Debug("Locating chart with legacy creds")

			client.Username, client.Password = legacyUsername, cfg.NGCAPIKey
			if cp, err = client.LocateChart(chart, settings); err == nil {
				logger.Debug("Found chart with legacy API key")
			} else if !isErrHTTPAuthIssue(err) {
				span.RecordError(err)
				return err
			} else {
				logger.Debug("Locate chart with legacy API key failed", zap.String("error", err.Error()))
			}
			return nil
		})
	}

	// Collect errors to join later.
	dlErrs := map[string]struct{}{}

	credHelper := credhelper.NewCredHelper()
	for i, authSecret := range cfg.HelmRegistryAuthConfig.K8sSecrets {
		auth, ok := authSecret.Auths[host]
		if !ok {
			continue
		}
		checkFuncs = append(checkFuncs, func() error {
			defer client.helmImageClientCache.reset()

			_, span := tracer.Start(
				ctx,
				"reval.handler.helm.locateChartWithCreds",
				trace.WithAttributes(
					attribute.Int("credsIndex", i),
				),
			)
			defer span.End()

			logger := logger.With(zap.Int("credsIndex", i))
			logger.Debug("Locating chart")

			initialUsername, initialPassword, err := parseAuthToBasicCreds(logger, auth.Auth)
			if err != nil {
				return err
			}

			// Direct HTTP URL fetches do not need oauth2 or login, only OCI.
			if registry.IsOCI(chart) {
				creds := credhelper.AuthHelperCredentials{
					Username: initialUsername,
					Password: initialPassword,
				}
				if client.Username, client.Password, err = credHelper.GetRegistryCredentials(ctx, chart, creds); err != nil {
					return err
				}

				if client.Username != "" || client.Password != "" {
					if err := loginToRegistry(ctx,
						client.helmImageClient, client.helmCredsStore,
						host, client.Username, client.Password); err != nil {
						return err
					}
				}
			} else {
				client.Username, client.Password = initialUsername, initialPassword
			}

			if cp, err = client.LocateChart(chart, settings); err == nil {
				logger.Debug("Found chart")
				return nil
			} else if !isErrHTTPAuthIssue(err) {
				return err
			} else {
				logger.Debug("Locate chart failed", zap.String("error", err.Error()))
			}
			return nil
		})
	}

	// Attempt to fetch credentials using specific registry configuration from environment.
	// This process' environment must be configured to authenticate with the registry hoster's auth provider.
	checkFuncs = append(checkFuncs, func() error {
		defer client.helmImageClientCache.reset()

		_, span := tracer.Start(
			ctx,
			"reval.handler.helm.locateChartWithEnvCreds",
		)
		defer span.End()

		logger := logger.With(zap.Bool("credsFromEnv", true))
		logger.Debug("Locating chart")

		creds := credhelper.AuthHelperCredentials{
			LoadFromEnv: true,
		}
		if client.Username, client.Password, err = credHelper.GetRegistryCredentials(ctx, chart, creds); err != nil {
			return err
		}

		// Direct HTTP URL fetches do not need oauth2 or login, only OCI.
		if registry.IsOCI(chart) && (client.Username != "" || client.Password != "") {
			if err := loginToRegistry(ctx,
				client.helmImageClient, client.helmCredsStore,
				host, client.Username, client.Password); err != nil {
				return err
			}
		}

		if cp, err = client.LocateChart(chart, settings); err == nil {
			logger.Debug("Found chart")
			return nil
		} else if !isErrHTTPAuthIssue(err) {
			return err
		} else {
			logger.Debug("Locate chart failed", zap.String("error", err.Error()))
		}
		return nil
	})

	// Assume public chart last since most charts used in NVCF are not.
	checkFuncs = append(checkFuncs, func() (err error) {
		defer client.helmImageClientCache.reset()

		_, span := tracer.Start(
			ctx,
			"reval.handler.helm.locateChartWithCreds",
			trace.WithAttributes(
				attribute.Bool("public", true),
			),
		)
		defer span.End()

		logger.Debug("Locating public chart")

		client.Username, client.Password = "", ""
		if cp, err = client.LocateChart(chart, settings); err == nil {
			logger.Debug("Found public chart")
		} else if !isErrHTTPAuthIssue(err) {
			span.RecordError(err)
			return err
		} else {
			logger.Debug("Locate public chart failed", zap.String("error", err.Error()))
		}
		return nil
	})

	for _, f := range checkFuncs {
		if err := f(); err != nil {
			dlErrs[err.Error()] = struct{}{}
			continue
		}
		if cp != "" {
			return cp, nil
		}
	}

	if len(dlErrs) != 0 {
		eb := &strings.Builder{}
		i := 0
		for errStr := range dlErrs {
			eb.WriteString(errStr)
			i++
			if i < len(dlErrs) {
				eb.WriteByte('\n')
			}
		}
		err = errors.New(eb.String())
	} else {
		err = fmt.Errorf("no credential was valid to pull chart %q, manually verify credentials are valid", chart)
	}
	return "", err
}

// This was mostly copied from https://github.com/helm/helm/blob/b76a950f6835474e0906b96c9ec68a2eff3a6430/pkg/registry/client.go#L279
// but modified to add the username/password to the creds store before login.
func loginToRegistry(ctx context.Context,
	imageClient orasauth.Client,
	credentialsStore orascredentials.Store,
	host, username, password string,
) error {
	log := logFromContext(ctx)

	cred := orasauth.Credential{Username: username, Password: password}
	key := orascredentials.ServerAddressFromRegistry(host)
	key = orascredentials.ServerAddressFromHostname(key)
	if err := credentialsStore.Put(ctx, key, cred); err != nil {
		return err
	}

	reg, err := orasremote.NewRegistry(host)
	if err != nil {
		return err
	}
	reg.PlainHTTP = plainHTTP
	imageClient.ForceAttemptOAuth2 = true
	reg.Client = &imageClient

	if err := reg.Ping(ctx); err != nil {
		imageClient.ForceAttemptOAuth2 = false
		if err := reg.Ping(ctx); err != nil {
			return fmt.Errorf("authenticating to %q: %w", host, err)
		}
	}

	log.Debug("Login succeeded")
	return nil
}

func parseAuthToBasicCreds(logger *zap.Logger, auth string) (username, password string, err error) {
	decodedBytes, err := base64.StdEncoding.DecodeString(auth)
	if err != nil {
		logger.Debug("Failed to decode registry secret", zap.Error(err))
		return "", "", err
	}
	split := bytes.SplitN(decodedBytes, []byte{':'}, 2)
	if len(split) != 2 {
		logger.Debug("Malformed registry auth")
		return "", "", fmt.Errorf("registry secret does not contain ':'")
	}
	username, password = string(bytes.TrimSpace(split[0])), string(bytes.TrimSpace(split[1]))
	return username, password, nil
}

// checkIfInstallable validates if a chart can be installed
//
// Application chart type is only installable
func checkIfInstallable(ch *chart.Chart) error {
	switch ch.Metadata.Type {
	case "", "application":
		return nil
	}
	return fmt.Errorf("%s charts are not installable", ch.Metadata.Type)
}

var httpStatusErrRe = regexp.MustCompile(`failed to fetch .* : (?P<status>.+)`)

func isErrHTTPAuthIssue(err error) bool {
	if err == nil {
		return false
	}
	matches := httpStatusErrRe.FindStringSubmatch(err.Error())
	if len(matches) == 0 {
		return false
	}
	idx := httpStatusErrRe.SubexpIndex("status")
	if idx >= len(matches) {
		return false
	}
	status := matches[idx]
	if status == "" {
		return false
	}
	return strings.HasPrefix(status, "401") || strings.HasPrefix(status, "403")
}
