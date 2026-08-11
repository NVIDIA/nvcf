/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

package registrations

import (
	"fmt"

	"github.com/uptrace/opentelemetry-go-extra/otelzap"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/db_client/cassandra"
)

type DBProvider interface {
	NewConnection(config config.DBConfig) (data_access.DBHandler, error)
}

var dbProviders = make(map[string]DBProvider)

func RegisterDBProviders(logger *otelzap.Logger) {
	providers := map[string]DBProvider{
		"cassandra": cassandra.NewCassandraProvider(logger),
	}
	for name, provider := range providers {
		dbProviders[name] = provider
	}
}

func NewDBConnection(config config.DBConfig) (data_access.DBHandler, error) {
	if provider, exists := dbProviders[config.Provider]; exists {
		return provider.NewConnection(config)
	}
	return nil, fmt.Errorf("unsupported database type: %s", config.Provider)
}
