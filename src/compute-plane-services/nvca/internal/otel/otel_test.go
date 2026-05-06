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

package otel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/common"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/function"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/icms-translate/translate/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	otelattr "go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	nvcav2beta1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1"
)

func TestOTel_NewTracer(t *testing.T) {
	fooTracer := NewTracer(WithName("nvca"))
	assert.NotNil(t, fooTracer)
}

func TestOTel_GetOTelAttributesFromICMSRequest_Function(t *testing.T) {
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action:            common.FunctionCreationAction,
			NCAId:             "some-nca-id-0",
			FunctionID:        "my-function",
			FunctionVersionID: "my-function-version-ID",
			FunctionDetails: function.Details{
				FunctionID:        "my-function",
				FunctionVersionID: "my-function-version-ID",
			},
			RequestID:      "some-request",
			MessageBatchID: "some-request-batch-id",
		},
	}

	expectedAttrs := map[string]string{
		NCAIDAttributeKey:             icmsReq.Spec.NCAId,
		ICMSRequestIDAttributeKey:     icmsReq.Spec.RequestID,
		FunctionIDAttributeKey:        icmsReq.Spec.FunctionDetails.FunctionID,
		FunctionVersionIDAttributeKey: icmsReq.Spec.FunctionDetails.FunctionVersionID,
		MessageBatchIDAttributeKey:    icmsReq.Spec.MessageBatchID,
		ICMSInstanceIDsAttributeKey:   otelattr.StringSlice(ICMSInstanceIDsAttributeKey, icmsReq.Status.GetInstanceIDs()).Value.AsString(),
	}
	otelAttrs := GetOTelAttributesFromICMSRequest(icmsReq)
	assert.Len(t, otelAttrs, len(expectedAttrs))
	actualAttrs := map[string]string{}
	for _, attr := range otelAttrs {
		actualAttrs[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, expectedAttrs, actualAttrs)

	// Test with instanceIDs populated
	icmsReq.Status.Instances = map[string]nvcav2beta1.InstanceStatus{
		"id-1234": {},
		"id-5678": {},
	}
	expectedAttrs[ICMSInstanceIDsAttributeKey] = otelattr.StringSlice(ICMSInstanceIDsAttributeKey, icmsReq.Status.GetInstanceIDs()).Value.AsString()
	otelAttrs = GetOTelAttributesFromICMSRequest(icmsReq)
	assert.Len(t, otelAttrs, len(expectedAttrs))
	actualAttrs = map[string]string{}
	for _, attr := range otelAttrs {
		actualAttrs[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, expectedAttrs, actualAttrs)
}

func TestOTel_GetOTelAttributesFromICMSRequest_Task(t *testing.T) {
	icmsReq := &nvcav2beta1.ICMSRequest{
		Spec: nvcav2beta1.ICMSRequestSpec{
			Action: common.TaskCreationAction,
			NCAId:  "some-nca-id-0",
			TaskDetails: task.Details{
				TaskID:   "task-id",
				TaskType: "CONTAINER",
			},
			RequestID:      "some-request",
			MessageBatchID: "some-request-batch-id",
		},
	}

	expectedAttrs := map[string]string{
		NCAIDAttributeKey:           icmsReq.Spec.NCAId,
		ICMSRequestIDAttributeKey:   icmsReq.Spec.RequestID,
		TaskIDAttributeKey:          icmsReq.Spec.TaskDetails.TaskID,
		MessageBatchIDAttributeKey:  icmsReq.Spec.MessageBatchID,
		ICMSInstanceIDsAttributeKey: otelattr.StringSlice(ICMSInstanceIDsAttributeKey, icmsReq.Status.GetInstanceIDs()).Value.AsString(),
	}
	otelAttrs := GetOTelAttributesFromICMSRequest(icmsReq)
	assert.Len(t, otelAttrs, len(expectedAttrs))
	actualAttrs := map[string]string{}
	for _, attr := range otelAttrs {
		actualAttrs[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, expectedAttrs, actualAttrs)

	// Test with instanceIDs populated
	icmsReq.Status.Instances = map[string]nvcav2beta1.InstanceStatus{
		"id-1234": {},
		"id-5678": {},
	}
	expectedAttrs[ICMSInstanceIDsAttributeKey] = otelattr.StringSlice(ICMSInstanceIDsAttributeKey, icmsReq.Status.GetInstanceIDs()).Value.AsString()
	otelAttrs = GetOTelAttributesFromICMSRequest(icmsReq)
	assert.Len(t, otelAttrs, len(expectedAttrs))
	actualAttrs = map[string]string{}
	for _, attr := range otelAttrs {
		actualAttrs[string(attr.Key)] = attr.Value.AsString()
	}
	assert.Equal(t, expectedAttrs, actualAttrs)
}

type mockAttributesGetter struct {
	attributes []otelattr.KeyValue
}

func (m *mockAttributesGetter) GetOTelAttributes() []otelattr.KeyValue {
	return m.attributes
}

func TestGetDefaultAttributes(t *testing.T) {
	tests := []struct {
		name     string
		setAttrs []otelattr.KeyValue
		want     []otelattr.KeyValue
	}{
		{
			name: "initially empty",
			want: []otelattr.KeyValue{},
		},
		{
			name: "set and get",
			setAttrs: []otelattr.KeyValue{
				otelattr.String("key1", "value1"),
				otelattr.String("key2", "value2"),
			},
			want: []otelattr.KeyValue{
				otelattr.String("key1", "value1"),
				otelattr.String("key2", "value2"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setAttrs != nil {
				SetDefaultAttributes(tt.setAttrs)
			}
			got := GetDefaultAttributes()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConcurrentGetDefaultAttributes(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var got []otelattr.KeyValue

	SetDefaultAttributes([]otelattr.KeyValue{
		otelattr.String("key1", "value1"),
		otelattr.String("key2", "value2"),
	})

	// Start 10 goroutines to concurrently call GetDefaultAttributes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			got = GetDefaultAttributes()
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Check that all goroutines got the same value
	assert.NotNil(t, got)
	assert.Len(t, got, 2)
	assert.Equal(t, "value1", got[0].Value.AsString())
	assert.Equal(t, "value2", got[1].Value.AsString())
}

func TestConcurrentSetAndGetDefaultAttributes(t *testing.T) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var got []otelattr.KeyValue

	// Start 10 goroutines to concurrently call SetDefaultAttributes and GetDefaultAttributes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			SetDefaultAttributes([]otelattr.KeyValue{
				otelattr.String("key1", fmt.Sprintf("value1-%d", i)),
				otelattr.String("key2", fmt.Sprintf("value2-%d", i)),
			})
			mu.Lock()
			got = GetDefaultAttributes()
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	assert.NotNil(t, got)
	assert.Len(t, got, 2)
	assert.Contains(t, got[0].Value.AsString(), "value1-")
	assert.Contains(t, got[1].Value.AsString(), "value2-")
}

func TestInvokeWithSpan(t *testing.T) {
	ctx := context.Background()
	exporter := tracetest.NewInMemoryExporter()
	spanProcessor := sdktrace.NewSimpleSpanProcessor(exporter)
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanProcessor))
	otel.SetTracerProvider(tracerProvider)
	t.Cleanup(func() {
		exporter.Shutdown(ctx)
		spanProcessor.Shutdown(ctx)
		tracerProvider.Shutdown(ctx)
	})

	defaultAttr := otelattr.String("default-value", "default")
	SetDefaultAttributes([]otelattr.KeyValue{defaultAttr})

	tracer := otel.Tracer("github.com/NVIDIA/nvcf-go/otel")
	InvokeWithSpan(ctx, tracer, "some-span", func(ctx context.Context) error {
		return nil
	}, oteltrace.WithAttributes(otelattr.String("some-string", "value")))
	stubs := exporter.GetSpans()
	require.Len(t, stubs, 1)
	assert.Equal(t, otelcodes.Ok, stubs[0].Status.Code)
	assert.Subset(t, stubs[0].Attributes, []otelattr.KeyValue{otelattr.String("some-string", "value"), defaultAttr})
	exporter.Reset()

	spanErr := errors.New("span-error")
	InvokeWithSpan(ctx, tracer, "some-span", func(ctx context.Context) error {
		return spanErr
	}, oteltrace.WithAttributes(otelattr.String("some-other-string", "other-value")))
	stubs = exporter.GetSpans()
	require.Len(t, stubs, 1)
	assert.Equal(t, otelcodes.Error, stubs[0].Status.Code)
	require.Subset(t, stubs[0].Attributes, []otelattr.KeyValue{
		otelattr.String("some-other-string", "other-value"),
		otelattr.Bool("error", true),
		defaultAttr,
	})
}
