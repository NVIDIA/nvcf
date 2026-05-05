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
	"encoding/base64"
	"fmt"
	"net/url"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	volcenginecr "github.com/volcengine/volcengine-go-sdk/service/cr"
	"github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/request"
)

func Test_VolcEngine(t *testing.T) {
	type spec struct {
		ref      string
		registry string
		expError string
	}

	for _, tt := range []spec{
		{
			ref:      "project-myreg1-cn-shanghai.cr.volces.com",
			registry: "project-myreg1",
		},
		{
			ref:      "project-myreg1-cn-shanghai.cr.volces.com",
			registry: "project-otherreg",
			expError: "auth not found for registry: project-myreg1",
		},
		{
			ref:      "project-myreg1-cn-shanghai.cr.volces.com/foo/bar:latest",
			registry: "project-myreg1",
		},
		{
			ref:      "//project-myreg1-cn-shanghai.cr.volces.com",
			registry: "project-myreg1",
		},
		{
			ref:      "oci://project-myreg1-cn-shanghai.cr.volces.com",
			registry: "project-myreg1",
		},
		{
			ref:      "oci://project-myreg1-cn-shanghai.cr.volces.com/foo/bar:latest",
			registry: "project-myreg1",
		},
		{
			ref:      "https://project-myreg1-cn-shanghai.cr.volces.com",
			registry: "project-myreg1",
		},
	} {
		t.Run(tt.ref, func(t *testing.T) {
			ctx := context.Background()
			helpers := []CustomAuthHelper{
				volcengineHelper{
					newClient: func(_ context.Context, _, _ string, creds AuthHelperCredentials) (volcengineCRAPI, error) {
						return fakeVolcEngineClient{
							auths: map[string]volcenginecr.GetAuthorizationTokenOutput{
								tt.registry: {Username: volcengine.String("VE"), Token: volcengine.String(creds.Password + "-token")},
							},
						}, nil
					},
				},
			}
			gotUsername, gotToken, gotNeedsUpdate, err := getAuthUpdate(ctx, helpers, tt.ref, common.RegistryAuth{
				Auth: base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s:%s", "foo", "bar")),
			})
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else if assert.NoError(t, err) {
				assert.True(t, gotNeedsUpdate)
				assert.Equal(t, "VE", gotUsername)
				assert.Equal(t, "bar-token", gotToken)
			}

			creds := AuthHelperCredentials{Username: "foo", Password: "bar"}
			gotUsername, gotToken, err = credHelper{}.getRegistryCredentials(ctx, helpers, tt.ref, creds)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else if assert.NoError(t, err) {
				assert.Equal(t, "VE", gotUsername)
				assert.Equal(t, "bar-token", gotToken)
			}
		})
	}
}

func Test_VolcEngine_extractMetadata(t *testing.T) {
	type spec struct {
		image       string
		expMatch    bool
		expPublic   bool
		expRegistry string
		expRegion   string
		expEndpoint string
		expError    string
	}

	for _, tt := range []spec{
		{
			image:       "project-myreg1-cn-shanghai.cr.volces.com/namespace-myns-private/repo-private-test1:1.0.0",
			expMatch:    true,
			expPublic:   false,
			expRegistry: "project-myreg1",
			expRegion:   "cn-shanghai",
			expEndpoint: "open.volcengineapi.com",
		},
		{
			image:       "project-myreg1-us-east-1.cr.volces.com/namespace-myns-private/repo-private-test1:1.0.0",
			expMatch:    true,
			expPublic:   false,
			expRegistry: "project-myreg1",
			expRegion:   "us-east-1",
			expEndpoint: "open.volcengineapi.com",
		},
		{
			image:    "project-myreg1-us-east-1.cr.volces.io/repo-private-test:1.0.0",
			expMatch: false,
			expError: "no VolcEngine registry match",
		},
	} {
		t.Run(tt.image, func(t *testing.T) {
			h := volcengineHelper{}
			u, err := url.Parse("oci://" + tt.image)
			require.NoError(t, err)

			gotMatch, gotPublic := h.Matches(u)
			assert.Equal(t, tt.expMatch, gotMatch)
			assert.Equal(t, tt.expPublic, gotPublic)

			gotRegistry, gotRegion, gotEndpoint, gotErr := h.extractMetadata(u.Hostname())
			if tt.expError != "" {
				assert.EqualError(t, gotErr, tt.expError)
			} else if assert.NoError(t, gotErr) {
				assert.Equal(t, tt.expRegistry, gotRegistry)
				assert.Equal(t, tt.expRegion, gotRegion)
				assert.Equal(t, tt.expEndpoint, gotEndpoint)
			}
		})
	}
}

type fakeVolcEngineClient struct {
	auths map[string]volcenginecr.GetAuthorizationTokenOutput
}

func (c fakeVolcEngineClient) GetAuthorizationTokenWithContext(_ volcengine.Context, input *volcenginecr.GetAuthorizationTokenInput, _ ...request.Option) (*volcenginecr.GetAuthorizationTokenOutput, error) {
	auth, ok := c.auths[*input.Registry]
	if !ok {
		return nil, fmt.Errorf("auth not found for registry: %s", *input.Registry)
	}
	return &auth, nil
}
