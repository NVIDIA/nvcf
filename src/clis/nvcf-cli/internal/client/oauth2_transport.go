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
	"context"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

// oauth2RequestTransport shares a token while acquiring replacements with the
// context of the request that needs one.
type oauth2RequestTransport struct {
	config      *clientcredentials.Config
	tokenClient *http.Client
	base        http.RoundTripper

	mu         sync.Mutex
	token      *oauth2.Token
	refreshing chan struct{}
}

func (t *oauth2RequestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.tokenFor(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	authorized := req.Clone(req.Context())
	token.SetAuthHeader(authorized)
	return t.base.RoundTrip(authorized)
}

func (t *oauth2RequestTransport) tokenFor(ctx context.Context) (*oauth2.Token, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t.mu.Lock()
		if t.token.Valid() {
			token := t.token
			t.mu.Unlock()
			return token, nil
		}
		if t.refreshing != nil {
			refreshing := t.refreshing
			t.mu.Unlock()
			select {
			case <-refreshing:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		refreshing := make(chan struct{})
		t.refreshing = refreshing
		t.mu.Unlock()

		tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, t.tokenClient)
		token, err := t.config.TokenSource(tokenCtx).Token()
		t.mu.Lock()
		if err == nil {
			t.token = token
		}
		t.refreshing = nil
		close(refreshing)
		t.mu.Unlock()
		return token, err
	}
}
