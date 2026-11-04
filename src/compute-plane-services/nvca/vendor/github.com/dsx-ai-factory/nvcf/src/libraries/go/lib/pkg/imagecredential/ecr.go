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

package imagecredential

import (
	"context"
	"net/url"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/smithy-go/middleware"
	"github.com/aws/smithy-go/transport/http"
	"github.com/awslabs/amazon-ecr-credential-helper/ecr-login/version"

	ecrloginapi "github.com/awslabs/amazon-ecr-credential-helper/ecr-login/api"
)

const (
	// ociRegistryScheme is the OCI scheme from helm.sh/helm/v3/pkg/registry.OCIScheme.
	ociRegistryScheme = "oci"
	ecrPublicName     = "public.ecr.aws"
)

var ecrPattern = regexp.MustCompile(
	`^(\d{12})\.dkr[.-]ecr(-fips)?\.([a-zA-Z0-9][a-zA-Z0-9-_]*)\.` +
		`(amazonaws\.com(?:\.cn)?|on\.(?:aws|amazonwebservices\.com\.cn)|` +
		`sc2s\.sgov\.gov|c2s\.ic\.gov|cloud\.adc-e\.uk|csp\.hci\.ic\.gov)$`,
)

type ecrHelper struct {
	newClient func(ctx context.Context, reg *ecrloginapi.Registry, creds AuthHelperCredentials) (ecrClient, error)
}

func (h ecrHelper) Matches(serverURL *url.URL) (isECR, isPublic bool) {
	hostname := serverURL.Hostname()
	if hostname == ecrPublicName {
		return true, true
	}
	matches := ecrPattern.FindStringSubmatch(hostname)
	return len(matches) >= 3, false
}

func (h ecrHelper) Run(ctx context.Context, refURL *url.URL, creds AuthHelperCredentials) (username, password string, err error) {
	ref := strings.TrimPrefix(refURL.String(), ociRegistryScheme+"://")
	reg, err := ecrloginapi.ExtractRegistry(ref)
	if err != nil {
		return "", "", err
	}

	if h.newClient == nil {
		h.newClient = h.newRemoteClient
	}
	client, err := h.newClient(ctx, reg, creds)
	if err != nil {
		return "", "", err
	}

	auth, err := client.GetCredentials(refURL.Hostname())
	if err != nil {
		return "", "", err
	}
	return auth.Username, auth.Password, nil
}

var userAgentLoadOption = config.WithAPIOptions([]func(*middleware.Stack) error{
	http.AddHeaderValue("User-Agent", "amazon-ecr-credential-helper/"+version.Version),
})

type ecrClient interface {
	GetCredentials(serverURL string) (*ecrloginapi.Auth, error)
}

func (h ecrHelper) newRemoteClient(ctx context.Context, reg *ecrloginapi.Registry, creds AuthHelperCredentials) (ecrClient, error) {
	optFns := []func(*config.LoadOptions) error{
		userAgentLoadOption,
		config.WithRegion(reg.Region),
	}
	if reg.FIPS {
		optFns = append(optFns, config.WithEndpointDiscovery(aws.EndpointDiscoveryEnabled))
	}

	if !creds.LoadFromEnv {
		optFns = append(optFns, config.WithCredentialsProvider(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{
				AccessKeyID:     creds.Username,
				SecretAccessKey: creds.Password,
				AccountID:       reg.ID,
				Source:          credentials.StaticCredentialsName,
			}, nil
		})))
	}

	awsConfig, err := config.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, err
	}

	return ecrloginapi.DefaultClientFactory{}.
		NewClientWithOptions(ecrloginapi.Options{Config: awsConfig}), nil
}
