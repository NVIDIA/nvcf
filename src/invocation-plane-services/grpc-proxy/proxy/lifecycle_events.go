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
package proxy

import (
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/google/uuid"

	"nvcf-grpc-proxy/proxy/metrics"
	"nvcf-grpc-proxy/proxy/worker"
)

const (
	ledgerSource        = "grpc-proxy"
	ledgerSchemaVersion = 1

	eventTypeAttachmentEstablished = "GrpcProxy.Attachment.Established"
)

// Pre-initialized so the series exist at zero before the first publish.
func init() {
	for _, result := range []string{"sent", "failed", "dropped"} {
		metrics.EventLedgerPublishCounter.WithLabelValues(eventTypeAttachmentEstablished, result)
	}
}

type attachmentEstablishedData struct {
	SchemaVersion  int    `json:"schema_version"`
	RequestId      string `json:"request_id"`
	AttachmentId   string `json:"attachment_id"`
	IsRejoin       bool   `json:"is_rejoin"`
	SetupLatencyMs int64  `json:"setup_latency_ms"`
}

// newAttachmentEstablished describes wc gaining its worker at connectedAt.
// The ledger keeps the latest event of each type per request ID, so a rejoin
// replaces the first attachment's event; attachment_id tells them apart.
func newAttachmentEstablished(wc *worker.WorkerConnection, info workerAuthInfo, connectedAt time.Time) cloudevents.Event {
	event := cloudevents.NewEvent()
	event.SetID(uuid.NewString())
	event.SetSource(ledgerSource)
	event.SetType(eventTypeAttachmentEstablished)
	event.SetTime(connectedAt)
	event.SetExtension("functionversionid", info.functionVersionId)
	event.SetExtension("functionid", info.functionId)
	event.SetExtension("resourceid", info.requestId.String())
	event.SetExtension("ncaid", info.ncaId)
	// Marshalling a struct of strings, numbers and a bool cannot fail.
	_ = event.SetData(cloudevents.ApplicationJSON, attachmentEstablishedData{
		SchemaVersion:  ledgerSchemaVersion,
		RequestId:      info.requestId.String(),
		AttachmentId:   wc.AttachmentId.String(),
		IsRejoin:       info.isRejoin,
		SetupLatencyMs: connectedAt.Sub(info.setupStartedAt).Milliseconds(),
	})
	return event
}
