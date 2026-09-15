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

package ratelimiter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/olric-data/olric"
	"go.uber.org/zap"
)

// OlricKeyStats holds statistics about keys in Olric DMap
type OlricKeyStats struct {
	TotalKeys      int
	KeysWithoutTTL int
	KeysWithTTL    int
	ScanDurationMs int64
}

// CountOlricKeys scans the DMap and counts total keys and keys without TTL
func CountOlricKeys(ctx context.Context, dmap olric.DMap, dmapName string) (*OlricKeyStats, error) {
	start := time.Now()
	stats := &OlricKeyStats{}

	// Use Scan to iterate over all keys
	iter, err := dmap.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scan DMap: %w", err)
	}
	defer iter.Close()

	for iter.Next() {
		stats.TotalKeys++
		key := iter.Key()

		// Get the entry to check TTL
		entry, err := dmap.Get(ctx, key)
		if err != nil {
			// Key might have been deleted during iteration, skip it
			continue
		}

		// Check if key has no TTL (immortal key)
		ttl := entry.TTL()
		if ttl <= 0 {
			stats.KeysWithoutTTL++
			zap.L().Debug("Found key without TTL",
				zap.String("dmap", dmapName),
				zap.String("key", key),
				zap.Int64("ttl", ttl))
		} else {
			stats.KeysWithTTL++
		}
	}

	stats.ScanDurationMs = time.Since(start).Milliseconds()
	return stats, nil
}

// OlricStatsHandler returns an HTTP handler that displays Olric key statistics on-demand
func OlricStatsHandler(dmap olric.DMap, dmapName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		zap.L().Info("Olric stats requested", zap.String("dmap", dmapName))

		stats, err := CountOlricKeys(ctx, dmap, dmapName)
		if err != nil {
			zap.L().Error("Failed to count Olric keys", zap.Error(err))
			http.Error(w, fmt.Sprintf("Error counting keys: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Olric DMap Statistics\n")
		fmt.Fprintf(w, "=====================\n")
		fmt.Fprintf(w, "DMap Name:           %s\n", dmapName)
		fmt.Fprintf(w, "Total Keys:          %d\n", stats.TotalKeys)
		fmt.Fprintf(w, "Keys With TTL:       %d\n", stats.KeysWithTTL)
		fmt.Fprintf(w, "Keys WITHOUT TTL:    %d ⚠️\n", stats.KeysWithoutTTL)
		fmt.Fprintf(w, "Scan Duration:       %dms\n", stats.ScanDurationMs)
		fmt.Fprintf(w, "\n")

		if stats.KeysWithoutTTL > 0 {
			fmt.Fprintf(w, "⚠️  WARNING: Found %d immortal keys (no TTL set)\n", stats.KeysWithoutTTL)
			fmt.Fprintf(w, "These keys will never expire and cause memory leaks!\n")
		} else {
			fmt.Fprintf(w, "✅ All keys have TTL set - no immortal keys detected\n")
		}

		zap.L().Info("Olric stats completed",
			zap.String("dmap", dmapName),
			zap.Int("total_keys", stats.TotalKeys),
			zap.Int("keys_without_ttl", stats.KeysWithoutTTL),
			zap.Int64("scan_duration_ms", stats.ScanDurationMs))
	}
}
