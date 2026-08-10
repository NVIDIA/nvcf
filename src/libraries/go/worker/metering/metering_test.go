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

package metering

import (
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"regexp"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func configureLogCapture() (*zap.Logger, *observer.ObservedLogs) {
	core, observedLogs := observer.New(zap.InfoLevel)
	return zap.New(core), observedLogs
}

func TestNewEvent(t *testing.T) {
	requestId := "reqid-abc-123"
	subject := "sub-abc-123"
	event := New(&Config{}, requestId, subject, "nca-abc-123", nil)
	if event.RequestId != requestId {
		t.Fatalf("request id = %s, want = %s", event.RequestId, requestId)
	}
	if event.Subject != subject {
		t.Fatalf("subject = %s, want = %s", event.Subject, subject)
	}
}

func TestLog(t *testing.T) {
	config := Config{
		Backend:           "LocalBackend",
		BillingNcaId:      "billing-nca-abc-123",
		NcaId:             "nca-abc-123",
		FunctionId:        "fid-abc-123",
		FunctionVersionId: "fvid-abc-123",
		ICMSEnvironment:   "LocalEnvironment",
		ZoneName:          "LocalZone",
		NspectId:          "TEST-NSPECT-ID",
		GpuCount:          2,
		GpuType:           "L40S",
		FunctionTags:      []string{"Tag-1", "Tag-2", "Tag-3"},
	}
	event := New(&config, "reqid-abc-123", "sub-abc-123", "invoker-nca-id", []*pb.StringKV{{Key: "X-BILLING-ID", Value: "billing-id-abc-123"}})
	event.InferenceDurationSeconds = 10
	event.InferenceSize = 100
	event.ResponseType = pb.InvokeStatus_FULFILLED.String()

	logger, observedLogs := configureLogCapture()
	zap.ReplaceGlobals(logger)

	event.Log()

	if observedLogs.Len() != 1 {
		t.Fatalf("observed logs = %d, want 1", observedLogs.Len())
	}

	log := observedLogs.All()[0]
	if log.Entry.Level != zap.InfoLevel {
		t.Fatalf("metering log level = %s, want %s", log.Entry.Level, zap.InfoLevel)
	}

	// XXX: here be dragons... RFC3339 and UUID regexes
	want := regexp.MustCompile(`{"level":"info","msg":"metering event","env":"LocalEnvironment","data":{"timestamp":"([0-9]+)-(0[1-9]|1[012])-(0[1-9]|[12][0-9]|3[01])[Tt]([01][0-9]|2[0-3]):([0-5][0-9]):([0-5][0-9]|60)(\.[0-9]+)?(([Zz])|([\+|\-]([01][0-9]|2[0-3]):[0-5][0-9]))","transaction_id":"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}","nca_id":"invoker-nca-id","nspect_id":"TEST-NSPECT-ID","event_type":"NVCF_Invocation","properties":{"request_id":"reqid-abc-123","sub":"sub-abc-123","instance_type":"","backend":"LocalBackend","function_id":"fid-abc-123","function_version_id":"fvid-abc-123","size":100,"duration":10,"response_type":"FULFILLED","gpus":2,"gpu_type":"L40S","owner_nca_id":"nca-abc-123","user_function_tags":\["Tag-1","Tag-2","Tag-3"],"billing_headers":{"x-billing-id":"billing-id-abc-123"}}}}`)
	if !want.MatchString(log.Entry.Message) {
		t.Fatalf("log message = %s, want = %s", log.Entry.Message, want)
	}
	if log.Context[0].Key != "metering" || log.Context[0].Type != zapcore.BoolType || log.Context[0].Integer != 1 {
		t.Fatalf("missing metering field")
	}
}
