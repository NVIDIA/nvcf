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

package cassandra

import (
	"context"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
)

// The account reads filter inside the partition being read. Rows written
// without nca_id or function_id hold NULL there, so they never match.
const (
	accountEventsV3Query = `SELECT event_name, source, details, timestamp, created_at, updated_at
				  FROM events_v3
				  WHERE namespace = ? AND context = ? AND nca_id = ? AND function_id = ?
				  ALLOW FILTERING`

	accountStatsV3Query = `SELECT context, event_name, timestamp, created_at, updated_at
				  FROM stats_v3
				  WHERE namespace = ? AND nca_id = ? AND function_id = ?
				  ALLOW FILTERING`
)

// GetAccountEventsV3 retrieves the events of a namespace and context that were
// recorded for the given account and function.
func (c *CassandraHandler) GetAccountEventsV3(traceCtx context.Context, namespace, eventContext, ncaID, functionID string) ([]data_access.EventV3Record, error) {
	var records []data_access.EventV3Record

	err := c.executeWithSessionRecreation(traceCtx, func() error {
		records = nil

		iter := c.session.Query(accountEventsV3Query, namespace, eventContext, ncaID, functionID).WithContext(traceCtx).Iter()

		var record data_access.EventV3Record
		for iter.Scan(&record.EventName, &record.Source, &record.Details, &record.Timestamp, &record.CreatedAt, &record.UpdatedAt) {
			records = append(records, record)
			record = data_access.EventV3Record{}
		}

		return iter.Close()
	}, "getAccountEventsV3")
	if err != nil {
		return nil, fmt.Errorf("failed to get account events from events_v3: %w", err)
	}

	return records, nil
}

// GetAccountStatsV3 retrieves the stats_v3 rows of a namespace that were
// recorded for the given account and function.
func (c *CassandraHandler) GetAccountStatsV3(traceCtx context.Context, namespace, ncaID, functionID string) ([]data_access.StatsV3Record, error) {
	var records []data_access.StatsV3Record

	err := c.executeWithSessionRecreation(traceCtx, func() error {
		records = nil

		iter := c.session.Query(accountStatsV3Query, namespace, ncaID, functionID).WithContext(traceCtx).Iter()

		var (
			statsContext, eventName         string
			timestamp, createdAt, updatedAt time.Time
		)
		for iter.Scan(&statsContext, &eventName, &timestamp, &createdAt, &updatedAt) {
			records = append(records, data_access.StatsV3Record{
				Context:   statsContext,
				EventName: eventName,
				Timestamp: timestamp,
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			})
		}

		return iter.Close()
	}, "getAccountStatsV3")
	if err != nil {
		return nil, fmt.Errorf("failed to get account stats from stats_v3: %w", err)
	}

	return records, nil
}
