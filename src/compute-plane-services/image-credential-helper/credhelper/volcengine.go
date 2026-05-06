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

package credhelper

import (
	"context"
	"fmt"
	"net/url"
	"regexp"

	"github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/credentials"
	"github.com/volcengine/volcengine-go-sdk/volcengine/request"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"
	"github.com/volcengine/volcengine-go-sdk/volcengine/volcengineutil"

	volcenginecr "github.com/volcengine/volcengine-go-sdk/service/cr"
)

var (
	volcenginePattern = regexp.MustCompile(`^([a-zA-Z0-9][a-zA-Z0-9-_]+)-(cn|ap|us)-([^-]+)(-[1-9])?\.cr\.volces\.com$`)
)

type volcengineHelper struct {
	newClient func(ctx context.Context, region, endpoint string, creds AuthHelperCredentials) (volcengineCRAPI, error)
}

func (h volcengineHelper) Matches(serverURL *url.URL) (match, isPublic bool) {
	matches := volcenginePattern.FindStringSubmatch(serverURL.Hostname())
	return len(matches) >= 4, false
}

func (h volcengineHelper) Run(ctx context.Context, refURL *url.URL, creds AuthHelperCredentials) (username, password string, err error) {
	if creds.LoadFromEnv {
		return "", "", fmt.Errorf("loading credentials from environment is not supported for VolcEngine")
	}

	regHost := refURL.Hostname()
	reg, region, endpoint, err := h.extractMetadata(regHost)
	if err != nil {
		return "", "", err
	}

	if h.newClient == nil {
		h.newClient = h.newRemoteClient
	}
	client, err := h.newClient(ctx, region, endpoint, creds)
	if err != nil {
		return "", "", err
	}

	auth, err := client.GetAuthorizationTokenWithContext(ctx, &volcenginecr.GetAuthorizationTokenInput{
		Registry: volcengine.String(reg),
	})
	if err != nil {
		return "", "", err
	}
	if auth.Username == nil || auth.Token == nil {
		return "", "", fmt.Errorf("empty username or token")
	}
	return *auth.Username, *auth.Token, nil
}

func (h volcengineHelper) extractMetadata(hostname string) (registry, region, endpoint string, err error) {
	matches := volcenginePattern.FindStringSubmatch(hostname)
	if len(matches) < 4 {
		err = fmt.Errorf("no VolcEngine registry match")
		return
	}
	registry = matches[1]
	region = fmt.Sprintf("%s-%s%s", matches[2], matches[3], matches[4])
	ep := volcengineutil.GetDefaultEndpointByServiceInfo(volcenginecr.EndpointsID, region, nil, nil)
	endpoint = *ep
	return
}

type volcengineCRAPI interface {
	GetAuthorizationTokenWithContext(volcengine.Context, *volcenginecr.GetAuthorizationTokenInput, ...request.Option) (*volcenginecr.GetAuthorizationTokenOutput, error)
}

func (h volcengineHelper) newRemoteClient(ctx context.Context, region, endpoint string, creds AuthHelperCredentials) (volcengineCRAPI, error) {
	cfg := volcengine.NewConfig().
		WithCredentials(credentials.NewStaticCredentials(creds.Username, creds.Password, "")).
		WithEndpoint(endpoint).
		WithRegion(region)

	sess, err := session.NewSession(cfg)
	if err != nil {
		return nil, err
	}
	return volcenginecr.New(sess), nil
}
