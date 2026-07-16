/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package nvca

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Serialized-herd cold-start (pioneer election) metrics. The
// MiniService-creation gate elects exactly one cold-start pioneer per
// function-version and defers the rest until the pioneer's capture warms
// the function. Both counters are package-level (same pattern as the
// reconciler's metrics.go) so the gate can record without threading a
// metrics handle through applyMiniServiceCreationMessage.
var (
	// coldStartPioneersElected counts MiniService-creation reconciles
	// that won the pioneer slot and were allowed to cold-start. Roughly
	// one increment per cold function-version (plus one per stolen
	// expired-lease slot under pioneer crash recovery).
	coldStartPioneersElected = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nvca_nvsnap_coldstart_pioneers_elected_total",
		Help: "Count of MiniService-creation reconciles that won the cold-start " +
			"pioneer slot and were allowed to cold-start + capture (serialized-herd).",
	})

	// coldStartRepliacasDeferred counts MiniService-creation reconciles
	// that observed a live foreign pioneer claim and deferred (requeued)
	// rather than creating their MiniService. A healthy herd produces
	// (N-1) deferrals per cold function-version of N replicas, each
	// repeated until the pioneer warms the function.
	coldStartReplicasDeferred = promauto.NewCounter(prometheus.CounterOpts{
		Name: "nvca_nvsnap_coldstart_replicas_deferred_total",
		Help: "Count of MiniService-creation reconciles deferred (requeued) because " +
			"another replica holds the cold-start pioneer claim (serialized-herd). " +
			"Counts every requeue, so it is cumulative across a deferred replica's retries.",
	})
)
