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
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	ecrloginapi "github.com/awslabs/amazon-ecr-credential-helper/ecr-login/api"
	"github.com/stretchr/testify/assert"
)

func Test_ECR(t *testing.T) {
	type spec struct {
		ref      string
		server   string
		expError string
	}

	for _, tt := range []spec{
		{
			ref:    "779846807222.dkr.ecr.us-west-2.amazonaws.com",
			server: "779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:      "779846807222.dkr.ecr.us-west-2.amazonaws.com",
			server:   "779846807221.dkr.ecr.us-west-2.amazonaws.com",
			expError: "auth not found for server: 779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:    "779846807222.dkr.ecr.us-west-2.amazonaws.com/foo/bar:latest",
			server: "779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:    "//779846807222.dkr.ecr.us-west-2.amazonaws.com",
			server: "779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:    "oci://779846807222.dkr.ecr.us-west-2.amazonaws.com",
			server: "779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:    "oci://779846807222.dkr.ecr.us-west-2.amazonaws.com/foo/bar:latest",
			server: "779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:    "https://779846807222.dkr.ecr.us-west-2.amazonaws.com",
			server: "779846807222.dkr.ecr.us-west-2.amazonaws.com",
		},
		{
			ref:    ecrPublicName,
			server: ecrPublicName,
		},
		{
			ref:    "oci://" + ecrPublicName,
			server: ecrPublicName,
		},
	} {
		t.Run(tt.ref, func(t *testing.T) {
			ctx := context.Background()
			helpers := []CustomAuthHelper{
				ecrHelper{
					newClient: func(_ context.Context, _ *ecrloginapi.Registry, creds AuthHelperCredentials) (ecrClient, error) {
						return fakeECRClient{
							auths: map[string]ecrloginapi.Auth{
								tt.server: {Username: "AWS", Password: creds.Password + "-token"},
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
				assert.Equal(t, "AWS", gotUsername)
				assert.Equal(t, "bar-token", gotToken)
			}

			creds := AuthHelperCredentials{Username: "foo", Password: "bar"}
			gotUsername, gotToken, err = credHelper{}.getRegistryCredentials(ctx, helpers, tt.ref, creds)
			if tt.expError != "" {
				assert.EqualError(t, err, tt.expError)
			} else if assert.NoError(t, err) {
				// Anonymous pull must work in reval.
				if tt.server == ecrPublicName {
					assert.Empty(t, gotUsername)
					assert.Empty(t, gotToken)
				} else {
					assert.Equal(t, "AWS", gotUsername)
					assert.Equal(t, "bar-token", gotToken)
				}
			}
		})
	}
}

type fakeECRClient struct {
	auths map[string]ecrloginapi.Auth
}

func (c fakeECRClient) GetCredentials(serverURL string) (*ecrloginapi.Auth, error) {
	auth, ok := c.auths[serverURL]
	if !ok {
		return nil, fmt.Errorf("auth not found for server: %s", serverURL)
	}
	return &auth, nil
}
