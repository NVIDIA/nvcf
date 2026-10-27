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

package storefactory

import (
	"context"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/dynamodb"
)

func init() {
	Register(config.StoreDynamoDB, openDynamoDB)
}

// openDynamoDB builds the DynamoDB store from the server configuration. The
// AWS client reads credentials and the region from the environment, with
// --region and --dynamodb-endpoint as overrides.
func openDynamoDB(ctx context.Context, cfg *config.Config) (store.Store, error) {
	client, err := dynamodb.NewClient(ctx, dynamodb.ClientOptions{Region: cfg.Region, Endpoint: cfg.DynamoDBEndpoint})
	if err != nil {
		return nil, err
	}
	return dynamodb.New(dynamodb.Options{Client: client, TableName: cfg.DynamoDBTable})
}
