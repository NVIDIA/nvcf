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

	"nvcf-grpc-proxy/proxy/invocation"
	"nvcf-grpc-proxy/proxy/metrics"
	"nvcf-grpc-proxy/proxy/worker"
)

const (
	ledgerSource        = "grpc-proxy"
	ledgerSchemaVersion = 1

	eventTypeWorkDispatched        = "GrpcProxy.Work.Dispatched"
	eventTypeAttachmentEstablished = "GrpcProxy.Attachment.Established"
	eventTypeAttachmentSetupFailed = "GrpcProxy.Attachment.Setup_failed"
	eventTypeAttachmentClosed      = "GrpcProxy.Attachment.Closed"

	// Auth, rate limit and rejoin lookup failures are not published: their
	// callers are unverified or rejected, and every attempt would add a row.
	setupPhaseDispatch   = "dispatch"
	setupPhaseWorkerWait = "worker_wait"
)

// Pre-initialized so the series exist at zero before the first publish.
func init() {
	for _, eventType := range []string{eventTypeWorkDispatched, eventTypeAttachmentEstablished, eventTypeAttachmentSetupFailed, eventTypeAttachmentClosed} {
		for _, result := range []string{"sent", "failed", "dropped"} {
			metrics.EventLedgerPublishCounter.WithLabelValues(eventType, result)
		}
	}
}

type eventHeader struct {
	SchemaVersion int    `json:"schema_version"`
	RequestId     string `json:"request_id"`
}

func header(requestId uuid.UUID) eventHeader {
	return eventHeader{SchemaVersion: ledgerSchemaVersion, RequestId: requestId.String()}
}

type workDispatchedData struct {
	eventHeader
	IsRejoin bool `json:"is_rejoin"`
}

type attachmentEstablishedData struct {
	eventHeader
	AttachmentId   string `json:"attachment_id"`
	IsRejoin       bool   `json:"is_rejoin"`
	SetupLatencyMs int64  `json:"setup_latency_ms"`
}

type attachmentSetupFailedData struct {
	eventHeader
	Phase        string `json:"phase"`
	AttachmentId string `json:"attachment_id,omitempty"`
}

type attachmentClosedData struct {
	eventHeader
	AttachmentId string `json:"attachment_id"`
	Cause        string `json:"cause"`
	HeldForMs    int64  `json:"held_for_ms"`
	// A fixed category. The close detail can carry worker text and is never sent.
	CloseCode string `json:"close_code"`
}

func newLedgerEvent(eventType string, at time.Time, requestId uuid.UUID, functionId, functionVersionId, callerNcaId string, data any) cloudevents.Event {
	event := cloudevents.NewEvent()
	event.SetID(uuid.NewString())
	event.SetSource(ledgerSource)
	event.SetType(eventType)
	event.SetTime(at)
	event.SetExtension("functionversionid", functionVersionId)
	event.SetExtension("functionid", functionId)
	event.SetExtension("resourceid", requestId.String())
	event.SetExtension("ncaid", callerNcaId)
	// Marshalling a struct of strings, numbers and bools cannot fail.
	_ = event.SetData(cloudevents.ApplicationJSON, data)
	return event
}

func newWorkDispatched(auth invocation.WorkerAuth, isRejoin bool, at time.Time) cloudevents.Event {
	return newLedgerEvent(eventTypeWorkDispatched, at, auth.RequestId, auth.FunctionId, auth.FunctionVersionId, auth.CallerNcaId,
		workDispatchedData{eventHeader: header(auth.RequestId), IsRejoin: isRejoin})
}

func newDispatchFailed(auth invocation.WorkerAuth, at time.Time) cloudevents.Event {
	return newLedgerEvent(eventTypeAttachmentSetupFailed, at, auth.RequestId, auth.FunctionId, auth.FunctionVersionId, auth.CallerNcaId,
		attachmentSetupFailedData{eventHeader: header(auth.RequestId), Phase: setupPhaseDispatch})
}

// newAttachmentEstablished describes wc gaining its worker at connectedAt.
// The ledger keeps the latest event of each type per request ID, so a rejoin
// replaces the first attachment's event; attachment_id tells them apart.
func newAttachmentEstablished(wc *worker.WorkerConnection, info workerAuthInfo, connectedAt time.Time) cloudevents.Event {
	return newLedgerEvent(eventTypeAttachmentEstablished, connectedAt, info.requestId, info.functionId, info.functionVersionId, info.callerNcaId,
		attachmentEstablishedData{
			eventHeader:    header(info.requestId),
			AttachmentId:   wc.AttachmentId.String(),
			IsRejoin:       info.isRejoin,
			SetupLatencyMs: connectedAt.Sub(info.setupStartedAt).Milliseconds(),
		})
}

// attachmentEndEvent reports how wc left the cache, when anyone is waiting to
// hear: Closed for an attachment reported established, or Setup_failed when no
// worker arrived before the slot's TTL ran out.
func attachmentEndEvent(wc *worker.WorkerConnection, cause string, closedAt time.Time, heldFor time.Duration, closeCode string) (cloudevents.Event, bool) {
	switch {
	case wc.MarkEnded():
		return newLedgerEvent(eventTypeAttachmentClosed, closedAt, wc.RequestId, wc.FunctionId, wc.FunctionVersionId, wc.CallerNcaId,
			attachmentClosedData{
				eventHeader:  header(wc.RequestId),
				AttachmentId: wc.AttachmentId.String(),
				Cause:        cause,
				HeldForMs:    heldFor.Milliseconds(),
				CloseCode:    closeCode,
			}), true
	case !wc.EverConnected() && cause == metrics.CloseReasonTTLExpired:
		return newLedgerEvent(eventTypeAttachmentSetupFailed, closedAt, wc.RequestId, wc.FunctionId, wc.FunctionVersionId, wc.CallerNcaId,
			attachmentSetupFailedData{eventHeader: header(wc.RequestId), Phase: setupPhaseWorkerWait, AttachmentId: wc.AttachmentId.String()}), true
	}
	return cloudevents.Event{}, false
}
