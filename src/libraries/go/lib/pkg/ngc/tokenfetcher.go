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

package ngc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
)

type TokenFetcher struct {
	ngcAPIKey  string
	authAPIKey string

	ngcAuthURL string
	ngcOrg     string

	httpClient *http.Client
}

type TokenFetcherOption func(*TokenFetcher)

func WithNGCAPIKey(ngcAPIKey, ngcOrg string) TokenFetcherOption {
	return func(tf *TokenFetcher) {
		tf.ngcAPIKey = ngcAPIKey
		tf.ngcOrg = ngcOrg
	}
}

func WithNGCAuthURL(ngcAuthURL string) TokenFetcherOption {
	return func(tf *TokenFetcher) {
		tf.ngcAuthURL = ngcAuthURL
	}
}

func WithAuthAPIKey(authAPIKey string) TokenFetcherOption {
	return func(tf *TokenFetcher) {
		tf.authAPIKey = authAPIKey
	}
}

func NewTokenFetcher(opts ...TokenFetcherOption) (*TokenFetcher, error) {
	tokenFetcher := &TokenFetcher{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		ngcAuthURL: "https://authn.nvidia.com",
	}

	for _, o := range opts {
		o(tokenFetcher)
	}

	if tokenFetcher.ngcAPIKey == "" && tokenFetcher.authAPIKey == "" {
		return nil, errors.New("NGC API Key or Auth API Key must be specified")
	}

	return tokenFetcher, nil
}

func (c *TokenFetcher) FetchToken(ctx context.Context) (string, error) {
	log := core.GetLogger(ctx)

	if c.authAPIKey != "" {
		return c.authAPIKey, nil
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/token?scope=group/ngc:%s", c.ngcAuthURL, c.ngcOrg), nil)
	if err != nil {
		log.Errorf("error creating NGC auth request, error: %v", err)
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("$oauthtoken", c.ngcAPIKey)
	log.Debugf("authenticating to NGC URL %s", req.URL)
	resp, err := c.httpClient.Do(req) //nolint:gosec // G704: false positive - URL is validated before use
	if err != nil {
		log.Errorf("error authenticating to NGC URL %s, error: %v", req.URL, err)
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.WithError(err).Errorf("Error authenticating to NGC URL %s with status code %d", req.URL, resp.StatusCode)
		return "", core.HTTPCodeError(resp.StatusCode)
	}

	tokenResp := struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expires_in"`
	}{}
	err = json.NewDecoder(resp.Body).Decode(&tokenResp)
	if err != nil {
		log.Errorf("unmarshalling json response from NGC URL %s failed, error: %v", req.URL, err)
		return "", err
	}

	log.Debugf("successfully authenticated to NGC URL %s", req.URL)
	return tokenResp.Token, nil
}
