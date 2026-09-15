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
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc/credentials"
)

var NoContext = context.Background()

func TestTokenStoreReadWrite(t *testing.T) {

	mocked := NewInmemoryTokenStore()

	token := oauth2.Token{
		AccessToken: "token",
	}

	mocked.write(&token)

	ret := mocked.read()

	assert.Equal(t, "token", ret.AccessToken)
}

func TestRefresher_IsRunning(t *testing.T) {
	conf := clientcredentials.Config{
		ClientID:     "CLIENT_ID",
		ClientSecret: "CLIENT_SECRET",
		Scopes:       []string{"scope"},
		TokenURL:     "/token",
	}
	mockedConfig := NewTokenRefresherConfig(&conf, 900, "id")
	refresher := NewIntervalTokenRefresher(mockedConfig)

	refresher.StartAsyncRefresh(NoContext)
	assert.True(t, refreshScheduler.IsRunning())
}

func TestNewInmemoryTokenStore(t *testing.T) {
	conf := clientcredentials.Config{
		ClientID:     "CLIENT_ID",
		ClientSecret: "CLIENT_SECRET",
		Scopes:       []string{"scope"},
		TokenURL:     "/token",
	}
	mockedConfig := NewTokenRefresherConfig(&conf, 900, "id")
	store := NewInmemoryTokenStore()
	mockedConfig.store = store

	cached := oauth2.Token{
		AccessToken: "cached",
	}

	store.write(&cached)

	ts := mockedConfig.TokenSource(NoContext)

	// without re-fetch, should take cached first that override original
	token, err := ts.Token()

	assert.Nil(t, err)
	assert.Equal(t, "cached", token.AccessToken)
}

func TestFallBackToClientCredential(t *testing.T) {

	wantGrantType := "password"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ioutil.ReadAll(r.Body) == %v, %v, want _, <nil>", body, err)
		}
		if err := r.Body.Close(); err != nil {
			t.Errorf("r.Body.Close() == %v, want <nil>", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("url.ParseQuery(%q) == %v, %v, want _, <nil>", body, values, err)
		}
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=sometoken&token_type=bearer"))
	}))
	config := &clientcredentials.Config{
		ClientID:     "CLIENT_ID",
		ClientSecret: "CLIENT_SECRET",
		Scopes:       []string{"scope"},
		TokenURL:     ts.URL + "/token",
		EndpointParams: url.Values{
			"grant_type": {wantGrantType},
		},
	}

	mocked := NewTokenRefresherConfig(config, 900, "id")

	mocked.store = NewInmemoryTokenStore()

	// no token set, should fall back to request from client credential again.
	token, err := mocked.TokenSource(NoContext).Token()

	assert.Nil(t, err)
	assert.Equal(t, "sometoken", token.AccessToken)
}

func TestNewGRPCOauth2TokenSource(t *testing.T) {
	testToken := "90d64460d14870c08c81352a05dedd3465940a7c"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ioutil.ReadAll(r.Body) == %v, %v, want _, <nil>", body, err)
		}
		if err := r.Body.Close(); err != nil {
			t.Errorf("r.Body.Close() == %v, want <nil>", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("url.ParseQuery(%q) == %v, %v, want _, <nil>", body, values, err)
		}
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=" + testToken + "&token_type=bearer"))
	}))
	type requestInfoKey struct{}
	config := &clientcredentials.Config{
		ClientID:     "CLIENT_ID",
		ClientSecret: "CLIENT_SECRET",
		Scopes:       []string{"scope"},
		TokenURL:     ts.URL + "/token",
	}
	mockedTokenSource := NewGRPCOauth2TokenSource(config)
	ctx := context.Background()
	ri := credentials.RequestInfo{
		Method:   "testInfo",
		AuthInfo: nil,
	}
	ctx = context.WithValue(ctx, requestInfoKey{}, ri)
	rsp, err := mockedTokenSource.GetRequestMetadata(ctx)

	assert.Nil(t, err)
	assert.Equal(t, "Bearer "+testToken, rsp["authorization"])
}

func TestNewGRPCOauth2TokenSourceFromParams(t *testing.T) {
	testToken := "90d64460d14870c08c81352a05dedd3465940a7c"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ioutil.ReadAll(r.Body) == %v, %v, want _, <nil>", body, err)
		}
		if err := r.Body.Close(); err != nil {
			t.Errorf("r.Body.Close() == %v, want <nil>", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("url.ParseQuery(%q) == %v, %v, want _, <nil>", body, values, err)
		}
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=" + testToken + "&token_type=bearer"))
	}))
	type requestInfoKey struct{}
	config := &clientcredentials.Config{
		ClientID:     "CLIENT_ID",
		ClientSecret: "CLIENT_SECRET",
		Scopes:       []string{"scope"},
		TokenURL:     ts.URL + "/token",
	}

	// test with transport security enabled
	mockedTokenSource := NewGRPCOauth2TokenSource(config)
	assert.True(t, mockedTokenSource.RequireTransportSecurity())
	ctx := context.Background()
	ri := credentials.RequestInfo{
		Method:   "testInfo",
		AuthInfo: nil,
	}
	ctx = context.WithValue(ctx, requestInfoKey{}, ri)
	rsp, err := mockedTokenSource.GetRequestMetadata(ctx)

	assert.Nil(t, err)
	assert.Equal(t, "Bearer "+testToken, rsp["authorization"])

	// makesure that setting noTransportationSecurityBehaves
	mockedTokenSource = NewGRPCOauth2TokenSource(config, DisableTransportSecurity)
	rsp, err = mockedTokenSource.GetRequestMetadata(ctx)

	assert.False(t, mockedTokenSource.RequireTransportSecurity())

	assert.Nil(t, err)
	assert.Equal(t, "Bearer "+testToken, rsp["authorization"])
}

func TestNewLoggingRoundTripper(t *testing.T) {
	transport := http.DefaultTransport
	lt := NewLoggingRoundTripper(transport)
	assert.NotNil(t, lt)
	assert.Equal(t, transport, lt.internal)
	assert.NotNil(t, lt.tracer)
}

func TestLoggingTransport_RoundTrip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	lt := NewLoggingRoundTripper(http.DefaultTransport)
	req, err := http.NewRequest("GET", ts.URL, nil)
	require.NoError(t, err)

	resp, err := lt.RoundTrip(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

func TestNewConfigAndStartRefresher(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=testtoken&token_type=bearer"))
	}))
	defer ts.Close()

	conf := &clientcredentials.Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		TokenURL:     ts.URL + "/token",
		Scopes:       []string{"scope"},
	}
	ctx := context.Background()
	config := NewConfigAndStartRefresher(ctx, conf, 60)
	assert.NotNil(t, config)
	assert.Equal(t, "client-id", config.Id)
}

func TestTokenRefresherConfig_Client(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=mytoken&token_type=bearer"))
	}))
	defer ts.Close()

	conf := &clientcredentials.Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		TokenURL:     ts.URL + "/token",
		Scopes:       []string{"scope"},
	}
	config := NewTokenRefresherConfig(conf, 900, "test-client")

	ctx := context.Background()
	client := config.Client(ctx)
	assert.NotNil(t, client)
}

func TestGRPCOauth2TokenSource_Update(t *testing.T) {
	config := &clientcredentials.Config{
		ClientID:     "original-id",
		ClientSecret: "original-secret",
		TokenURL:     "http://localhost/token",
	}
	ts := NewGRPCOauth2TokenSource(config)

	grpcTs, ok := ts.(*grpcOauth2TokenSource)
	require.True(t, ok)

	assert.Equal(t, "original-id", grpcTs.cfg.ClientID)

	creds := &ClientCredentials{
		ClientID:     "new-id",
		ClientSecret: "new-secret",
	}
	grpcTs.Update(creds)

	assert.Equal(t, "new-id", grpcTs.cfg.ClientID)
	assert.Equal(t, "new-secret", grpcTs.cfg.ClientSecret)
}

func TestTokenRefresherConfig_TokenSourceFallback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=fallback-token&token_type=bearer"))
	}))
	defer ts.Close()

	conf := &clientcredentials.Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		TokenURL:     ts.URL + "/token",
	}
	config := NewTokenRefresherConfig(conf, 900, "test")
	// store an expired token to trigger fallback
	config.store.write(&oauth2.Token{AccessToken: "expired"})

	source := config.TokenSource(context.Background())
	token, err := source.Token()
	require.NoError(t, err)
	assert.NotEmpty(t, token.AccessToken)
}

func TestGRPCOauth2TokenSource_GetRequestMetadata(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=grpc-token&token_type=bearer"))
	}))
	defer ts.Close()

	conf := &clientcredentials.Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		TokenURL:     ts.URL + "/token",
	}
	grpcTs := NewGRPCOauth2TokenSource(conf)
	require.NotNil(t, grpcTs)

	// Test GetRequestMetadata
	md, err := grpcTs.GetRequestMetadata(context.Background())
	require.NoError(t, err)
	assert.Contains(t, md, "authorization")

	// Test RequireTransportSecurity
	assert.True(t, grpcTs.RequireTransportSecurity())
}

func TestGRPCOauth2TokenSource_DisableTransportSecurity(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte("access_token=grpc-token&token_type=bearer"))
	}))
	defer ts.Close()

	conf := &clientcredentials.Config{
		ClientID:     "cid",
		ClientSecret: "csecret",
		TokenURL:     ts.URL + "/token",
	}
	grpcTs := NewGRPCOauth2TokenSource(conf, DisableTransportSecurity)
	require.NotNil(t, grpcTs)

	// Test RequireTransportSecurity returns false when disabled
	assert.False(t, grpcTs.RequireTransportSecurity())
}
