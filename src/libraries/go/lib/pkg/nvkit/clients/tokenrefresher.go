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

package clients

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/auth"
)

const (
	// AuthTokenCacheTTLSeconds min validity for auth tokens is 900s. Hard coding to 600s keeps the code simple compared to extracting the expiry time
	// and dynamically setting TTL.
	AuthTokenCacheTTLSeconds            = 600
	AuthTokenStalenessTTLSeconds        = AuthTokenCacheTTLSeconds + 200
	TokenRefresherJobRunFrequencyMillis = 20000
)

type TokenResponse struct {
	AccessToken string `json:"access_token"` //nolint:gosec // G117: false positive - this is a response struct, not a secret storage
}

type tokenRefresherMetrics struct {
	numErrors5xx         *prometheus.CounterVec
	numErrors4xx         *prometheus.CounterVec
	numErrors            *prometheus.CounterVec
	numHeartbeats        *prometheus.CounterVec
	numCredentialUpdates *prometheus.CounterVec
	authTokenStaleness   *prometheus.GaugeVec
}

type metrics struct {
	once sync.Once
	m    *tokenRefresherMetrics
}

var (
	metricsMap               sync.Map
	tokenRefresherHTTPClient *http.Client
	tokenRefresherOnce       sync.Once
)

func initTokenRefresherMetrics(namespace string) *tokenRefresherMetrics {
	return &tokenRefresherMetrics{
		numErrors5xx: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "token_refresher",
			Name: "num_5xx_errors",
		}, []string{"response_code", "client_name"}),
		numErrors4xx: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "token_refresher",
			Name: "num_4xx_errors",
		}, []string{"response_code", "client_name"}),
		numErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "token_refresher",
			Name: "num_errors",
		}, []string{"client_name"}),
		numHeartbeats: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "token_refresher",
			Name: "num_heartbeats",
		}, []string{"client_name"}),
		numCredentialUpdates: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "token_refresher",
			Name: "num_credential_updates",
		}, []string{"client_name"}),
		authTokenStaleness: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "token_refresher",
			Name: "auth_token_staleness",
		}, []string{"client_name"}),
	}
}

type TokenRefresher struct {
	httpClient                *http.Client
	tokenEndpoint             string
	clientId                  string
	clientSecret              string
	credsFile                 string
	tokenRequestPayload       string
	clientName                string
	authToken                 string
	authTokenRefreshTimestamp int64
	rwMutex                   sync.RWMutex
	metricsNamespace          string
}

func (tr *TokenRefresher) getMetricsInstance() *tokenRefresherMetrics {
	v, _ := metricsMap.LoadOrStore(tr.metricsNamespace, &metrics{})
	e := v.(*metrics)

	e.once.Do(func() {
		e.m = initTokenRefresherMetrics(tr.metricsNamespace)
	})

	return e.m
}

func (tr *TokenRefresher) Token() string {
	tr.rwMutex.RLock()
	defer tr.rwMutex.RUnlock()
	return tr.authToken
}

func (tr *TokenRefresher) getTokenData() (string, int64) {
	tr.rwMutex.RLock()
	defer tr.rwMutex.RUnlock()
	return tr.authToken, tr.authTokenRefreshTimestamp
}

func (tr *TokenRefresher) updateClientCreds(clientId string, clientSecret string) {
	tr.rwMutex.Lock()
	tr.clientId = clientId
	tr.clientSecret = clientSecret
	tr.rwMutex.Unlock()
	tr.getMetricsInstance().numCredentialUpdates.WithLabelValues(tr.clientName).Inc()
	zap.L().Info("client credentials updated, refreshing token", zap.String("credsFile", tr.credsFile), zap.String("tokenEndpoint", tr.tokenEndpoint),
		zap.String("clientName", tr.clientName))
}

func (tr *TokenRefresher) getClientCreds() (string, string) {
	tr.rwMutex.RLock()
	defer tr.rwMutex.RUnlock()
	return tr.clientId, tr.clientSecret
}

func (tr *TokenRefresher) RefreshToken() (int, error) {
	tr.getMetricsInstance().numHeartbeats.WithLabelValues(tr.clientName).Inc()
	clientId, clientSecret, err := tr.readClientCreds()
	if err != nil {
		return 0, err
	}
	currentClientId, currentClientSecret := tr.getClientCreds()
	if clientId != currentClientId || clientSecret != currentClientSecret {
		tr.updateClientCreds(clientId, clientSecret)
	}
	httpStatus, err := tr.refreshTokenInternal()

	if err != nil {
		tr.EmitErrorMetrics(httpStatus, err)
		return httpStatus, err
	}
	return httpStatus, err
}

func (tr *TokenRefresher) refreshTokenOnce() (int, error) {
	tr.getMetricsInstance().numHeartbeats.WithLabelValues(tr.clientName).Inc()
	clientId, clientSecret, err := tr.readClientCreds()
	if err != nil {
		return 0, err
	}
	currentClientId, currentClientSecret := tr.getClientCreds()
	if clientId != currentClientId || clientSecret != currentClientSecret {
		tr.updateClientCreds(clientId, clientSecret)
		return tr.refreshTokenInternal()
	}
	authToken, ts := tr.getTokenData()
	if authToken == "" || (time.Now().Unix()-ts) > AuthTokenCacheTTLSeconds {
		zap.L().Info("auth token is stale, refreshing token", zap.String("credsFile", tr.credsFile), zap.String("tokenEndpoint", tr.tokenEndpoint),
			zap.String("clientName", tr.clientName))
		return tr.refreshTokenInternal()
	}
	return http.StatusOK, nil
}

func (tr *TokenRefresher) refreshTokenInternal() (int, error) {
	var data = strings.NewReader(tr.tokenRequestPayload)
	req, err := http.NewRequest("POST", tr.tokenEndpoint, data)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	clientId, clientSecret := tr.getClientCreds()
	req.SetBasicAuth(clientId, clientSecret)
	resp, err := tr.httpClient.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
	defer func() {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}()
	if err != nil {
		zap.L().Error("HTTP request failed", zap.Error(err), zap.String("tokenEndpoint", tr.tokenEndpoint), zap.String("clientName", tr.clientName))
		return http.StatusInternalServerError, err
	}

	bodyText, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}

	if resp.StatusCode != http.StatusOK {
		// in case of a non 200 response, treat response body as error message.
		return resp.StatusCode, errors.New(string(bodyText))
	}

	var tokenResponse TokenResponse
	err = json.Unmarshal(bodyText, &tokenResponse)
	if err != nil || tokenResponse.AccessToken == "" {
		zap.L().Error("Failed to parse token response",
			zap.Error(err),
			zap.String("responseBody", string(bodyText)),
			zap.String("tokenEndpoint", tr.tokenEndpoint),
			zap.String("clientName", tr.clientName),
			zap.Int("statusCode", resp.StatusCode))
		return resp.StatusCode, err
	}

	tr.rwMutex.Lock()
	defer tr.rwMutex.Unlock()
	tr.authToken = tokenResponse.AccessToken
	tr.authTokenRefreshTimestamp = time.Now().Unix()
	return resp.StatusCode, nil
}

func (tr *TokenRefresher) GetLastRefreshTimestamp() int64 {
	tr.rwMutex.RLock()
	defer tr.rwMutex.RUnlock()
	return tr.authTokenRefreshTimestamp
}

func (tr *TokenRefresher) readClientCreds() (string, string, error) {
	if tr.credsFile == "" {
		clientId, clientSecret := tr.getClientCreds()
		return clientId, clientSecret, nil
	}
	contents, err := os.ReadFile(tr.credsFile)
	if err != nil {
		return "", "", fmt.Errorf("cannot read client-credentials file: %+v, err: %+v", tr.credsFile, err)
	}
	credentials := &auth.ClientCredentials{}
	if err = yaml.Unmarshal(contents, credentials); err != nil {
		return "", "", fmt.Errorf("cannot unmarshal client-credentials file contents: %+v, err: %+v", tr.credsFile, err)
	}
	if credentials.ClientID == "" || credentials.ClientSecret == "" {
		return credentials.ClientID, credentials.ClientSecret, fmt.Errorf("invalid client id or secret: %+v", tr.credsFile)
	}
	return credentials.ClientID, credentials.ClientSecret, nil
}

func (tr *TokenRefresher) EmitErrorMetrics(statusCode int, err error) {
	trMetrics := tr.getMetricsInstance()
	zap.L().Error("error refreshing auth token",
		zap.Error(err),
		zap.String("credsFile", tr.credsFile),
		zap.String("tokenEndpoint", tr.tokenEndpoint),
		zap.Int("statusCode", statusCode),
		zap.String("clientName", tr.clientName))
	trMetrics.numErrors.WithLabelValues(tr.clientName).Inc()
	if statusCode >= 500 && statusCode <= 599 {
		trMetrics.numErrors5xx.WithLabelValues(fmt.Sprint(statusCode), tr.clientName).Inc()
	} else if statusCode >= 400 && statusCode <= 499 {
		trMetrics.numErrors4xx.WithLabelValues(fmt.Sprint(statusCode), tr.clientName).Inc()
	}
}

func getSingletonRetryableHttpClient() *http.Client {
	tokenRefresherOnce.Do(func() {
		// Create a client with custom retry configuration for token refresher
		client := NewHTTPClientV2(
			WithRetryMax(3),
			WithRetryWait(500*time.Millisecond, 5000*time.Millisecond),
		)
		tokenRefresherHTTPClient = client.httpClient
	})
	return tokenRefresherHTTPClient
}

func NewTokenRefresher(oidcConfig *auth.ProviderConfig, metricsNamespace string) (*TokenRefresher, error) {
	joined := strings.Join(oidcConfig.Scopes, " ")
	tokenRequestPayload := "scope=" + joined + "&grant_type=client_credentials"

	tr := TokenRefresher{
		httpClient:          getSingletonRetryableHttpClient(),
		tokenEndpoint:       oidcConfig.Host + "/token",
		credsFile:           oidcConfig.CredentialsFile,
		tokenRequestPayload: tokenRequestPayload,
		clientName:          oidcConfig.ClientName,
		clientId:            oidcConfig.ClientID,
		clientSecret:        oidcConfig.ClientSecret,
		rwMutex:             sync.RWMutex{},
		metricsNamespace:    metricsNamespace,
	}
	// init
	sc, err := tr.refreshTokenOnce()
	if err != nil {
		tr.EmitErrorMetrics(sc, err)
		return nil, err
	}
	// rand.Seed is deprecated in Go 1.20+ - the global random source is automatically seeded
	jitter := rand.New(rand.NewSource(time.Now().UnixNano())).Intn(6000) //nolint:gosec
	s := gocron.NewScheduler(time.UTC)
	if _, err = s.Every(TokenRefresherJobRunFrequencyMillis + jitter).Milliseconds().SingletonMode().Do(func() {
		statusCode, err := tr.refreshTokenOnce()
		if err != nil {
			tr.EmitErrorMetrics(statusCode, err)
		} else {
			zap.L().Debug("auth token refreshed successfully",
				zap.String("credsFile", tr.credsFile),
				zap.String("tokenEndpoint", tr.tokenEndpoint),
				zap.String("clientName", tr.clientName))
		}
		_, ts := tr.getTokenData()
		trMetrics := tr.getMetricsInstance()
		if (time.Now().Unix() - ts) > AuthTokenStalenessTTLSeconds {
			trMetrics.authTokenStaleness.WithLabelValues(tr.clientName).Set(1)
		} else {
			trMetrics.authTokenStaleness.WithLabelValues(tr.clientName).Set(0)
		}
	}); err != nil {
		return nil, err
	}
	s.SetMaxConcurrentJobs(1, 0)
	s.StartAsync()
	return &tr, nil
}

func NewPassiveTokenRefresher(oidcConfig *auth.ProviderConfig, metricsNamespace string) (*TokenRefresher, error) {
	joined := strings.Join(oidcConfig.Scopes, " ")
	tokenRequestPayload := "scope=" + joined + "&grant_type=client_credentials"
	tr := TokenRefresher{
		httpClient:          getSingletonRetryableHttpClient(),
		tokenEndpoint:       oidcConfig.Host + "/token",
		credsFile:           oidcConfig.CredentialsFile,
		tokenRequestPayload: tokenRequestPayload,
		clientName:          oidcConfig.ClientName,
		clientId:            oidcConfig.ClientID,
		clientSecret:        oidcConfig.ClientSecret,
		rwMutex:             sync.RWMutex{},
		metricsNamespace:    metricsNamespace,
	}
	_, err := tr.RefreshToken()
	if err != nil {
		return nil, err
	}
	return &tr, nil
}
