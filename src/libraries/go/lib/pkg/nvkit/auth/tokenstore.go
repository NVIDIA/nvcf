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
	"net/http"
	"net/http/httptrace"
	"reflect"
	"sync"
	"time"

	"github.com/go-co-op/gocron"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc/credentials"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/errors"
)

type TokenStore interface {
	read() *oauth2.Token
	write(*oauth2.Token)
}

type InmemoryTokenStore struct {
	cached *oauth2.Token

	// guard persisted token
	mutex sync.RWMutex
}

// TokenRefresherConfig Authn config replace client credential config, customized token source for using it own
type TokenRefresherConfig struct {
	ClientConfig *clientcredentials.Config
	store        *InmemoryTokenStore
	// to discriminate different usage - mygroup or controller
	Id string
	// second, interval for running refresh
	duration time.Duration
}

type RefresherTokenSource struct {
	ctx    context.Context
	config *TokenRefresherConfig
}

type LoggingTransport struct {
	internal http.RoundTripper
	tracer   trace.Tracer
}

func (l *LoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	reqCtx, span := l.tracer.Start(req.Context(), "refresh")
	defer span.End()
	req = req.WithContext(reqCtx)
	ret, err := l.internal.RoundTrip(req)
	// Validate the response and the error
	if reflect.ValueOf(ret).IsZero() || err != nil {
		return ret, err
	}
	// Validate the request and its header
	if ret.Request != nil && len(ret.Request.Header) > 0 {
		// TODO: This statement could be removed, it is to verify if the request is actually using token we just acquired
		zap.L().Debug("RequestHeader", zap.String("Header", ret.Request.Header.Get("Authorization")))
	}
	return ret, err
}

func NewLoggingRoundTripper(roundTripper http.RoundTripper) *LoggingTransport {
	tracer := otel.Tracer("authn")
	return &LoggingTransport{
		internal: roundTripper,
		tracer:   tracer,
	}
}

func NewInmemoryTokenStore() *InmemoryTokenStore {
	store := InmemoryTokenStore{}
	return &store
}

func (c *InmemoryTokenStore) read() *oauth2.Token {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return c.cached
}

func (c *InmemoryTokenStore) write(token *oauth2.Token) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.cached = token
}

type IntervalTokenRefresher struct {
	config *TokenRefresherConfig
}

var refreshScheduler *gocron.Scheduler

func init() {
	refreshScheduler = gocron.NewScheduler(time.UTC)
	refreshScheduler.TagsUnique()
}

func NewIntervalTokenRefresher(config *TokenRefresherConfig) *IntervalTokenRefresher {
	return &IntervalTokenRefresher{
		config: config,
	}
}

func (r *IntervalTokenRefresher) StartAsyncRefresh(ctx context.Context) {
	if err := refreshScheduler.RemoveByTag(r.config.Id); err == nil {
		zap.L().Info("Removed existing async token refresher", zap.String("id", r.config.Id))
	}

	zap.L().Info("Starting async token refresher", zap.String("id", r.config.Id), zap.String("interval", r.config.duration.String()))
	_, err := refreshScheduler.Every(r.config.duration).SingletonMode().Tag(r.config.Id).Do(func() {
		err := r.doRefreshToken(ctx)
		if err != nil {
			zap.L().Error("RefreshOnceFail", zap.Error(err), zap.String("Id", r.config.Id))
			// log not panic
		}
	})
	if err != nil {
		zap.L().Error("StartRefresherFail", zap.Error(err))
		return
	}
	refreshScheduler.StartAsync()
}

func (r *IntervalTokenRefresher) doRefreshToken(ctx context.Context) error {
	if r.config == nil || r.config.ClientConfig == nil {
		zap.L().Error("NoConfigForGettingToken")
		return errors.ErrBadTokenConfig
	}

	// currently just use default token retriever
	newToken, err := r.config.ClientConfig.Token(ctx)

	if err != nil || newToken == nil {
		zap.L().Error("ErrorRetrievingToken", zap.Error(err), zap.String("id", r.config.Id))
		authnRefreshErrorCounter.With(map[string]string{"client_name": r.config.Id, "client_id": r.config.ClientConfig.ClientID}).Inc()
		return errors.ErrFetchingToken
	}

	// no need to check if cached token is expired or not, just refreshing it
	r.config.store.write(newToken)
	zap.L().Info("TokenRefreshSucceed", zap.String("Id", r.config.Id),
		zap.String("TokenExpiry", newToken.Expiry.String()))
	return nil
}

func NewTokenRefresherConfig(conf *clientcredentials.Config, interval int64, name string) *TokenRefresherConfig {
	config := TokenRefresherConfig{
		ClientConfig: conf,
		duration:     time.Duration(interval) * time.Second,
		Id:           name,
		store:        NewInmemoryTokenStore(),
	}

	return &config
}

func NewConfigAndStartRefresher(ctx context.Context, conf *clientcredentials.Config, interval int64) *TokenRefresherConfig {
	config := NewTokenRefresherConfig(conf, interval, conf.ClientID)

	refresher := NewIntervalTokenRefresher(config)

	// start when creating cred config
	refresher.StartAsyncRefresh(ctx)

	return config
}

func (c *RefresherTokenSource) Token() (*oauth2.Token, error) {
	if c.config != nil && c.config.store != nil {
		token := c.config.store.read()
		if token.Valid() {
			zap.L().Debug("UsingCachedToken")
			return token, nil
		}
	}
	// should never happen
	zap.L().Error("CachedTokenFail", zap.String("id", c.config.Id))

	// by default, fall back to client credentials' token source
	token, err := c.config.ClientConfig.Token(c.ctx)

	if err != nil || token == nil || !token.Valid() {
		zap.L().Error("DefaultTokenFail", zap.Error(err), zap.String("id", c.config.Id))
	} else {
		c.config.store.write(token)
	}

	return token, err
}

func (c *TokenRefresherConfig) TokenSource(ctx context.Context) oauth2.TokenSource {
	source := &RefresherTokenSource{
		ctx:    ctx,
		config: c,
	}
	return oauth2.ReuseTokenSource(c.store.read(), source)
}

func (c *TokenRefresherConfig) Client(ctx context.Context) *http.Client {
	ctx = httptrace.WithClientTrace(ctx, otelhttptrace.NewClientTrace(ctx))
	client := oauth2.NewClient(ctx, c.TokenSource(ctx))
	client.Transport = NewLoggingRoundTripper(client.Transport)
	return client
}

type GRPCTokenSource interface {
	credentials.PerRPCCredentials
	AuthnRefresher
}

// grpcOauth2TokenSource supplies PerRPCCredentials.
type grpcOauth2TokenSource struct {
	src                 oauth2.TokenSource
	cfg                 *clientcredentials.Config
	noTransportSecurity bool

	mutex *sync.RWMutex
}

func (ts *grpcOauth2TokenSource) Update(creds *ClientCredentials) {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()
	ts.cfg.ClientID = creds.ClientID
	ts.cfg.ClientSecret = creds.ClientSecret
}

type Option func(ts *grpcOauth2TokenSource)

func DisableTransportSecurity(ts *grpcOauth2TokenSource) {
	ts.noTransportSecurity = true
}

// NewGRPCOauth2TokenSource constructs the PerRPCCredentials .
func NewGRPCOauth2TokenSource(config *clientcredentials.Config, opts ...Option) GRPCTokenSource {
	src := config.TokenSource(context.Background())
	tokenSource := &grpcOauth2TokenSource{src: src, cfg: config, mutex: &sync.RWMutex{}}
	for _, opt := range opts {
		opt(tokenSource)
	}
	return tokenSource
}

func (ts *grpcOauth2TokenSource) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	m := map[string]string{}

	ts.mutex.RLock()
	defer ts.mutex.RUnlock()
	token, err := ts.src.Token()
	if err != nil {
		return nil, err
	}
	m["authorization"] = token.Type() + " " + token.AccessToken

	return m, nil
}

func (ts *grpcOauth2TokenSource) RequireTransportSecurity() bool {
	return !ts.noTransportSecurity
}
