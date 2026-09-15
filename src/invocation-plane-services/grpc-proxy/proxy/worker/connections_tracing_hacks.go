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
package worker

import (
	"context"
	"go.opentelemetry.io/otel/trace"
	"reflect"
)

// DANGER unsafe pointers and reflection
// make sure you understand how this works before modifying
func (c *ConnectionTrackingConn) setParentSpan(ctx context.Context) {
	spanFromContext := trace.SpanFromContext(ctx)
	if !spanFromContext.SpanContext().IsValid() {
		return
	}
	span := reflect.ValueOf(spanFromContext).Elem() // type: recordingSpan
	parentContext := forceGet(span, "parent").(trace.SpanContext)
	if !parentContext.IsRemote() || !parentContext.IsValid() {
		return
	}

	correctParent := reflect.ValueOf(parentContext) // type: SpanContext

	spanContext := c.span.SpanContext().WithTraceID(parentContext.TraceID())
	correctContext := reflect.ValueOf(spanContext) // type: SpanContext

	sessionSpan := reflect.ValueOf(c.span).Elem() // type: recordingSpan

	forceSet(sessionSpan, "parent", correctParent)
	forceSet(sessionSpan, "spanContext", correctContext)
}

func (c *ConnectionTrackingConn) SetSessionSpanAsParent(ctx context.Context) {
	span := trace.SpanFromContext(ctx)
	if !span.SpanContext().IsValid() {
		return
	}
	grpcSpan := reflect.ValueOf(span).Elem() // type: recordingSpan
	parentContext := forceGet(grpcSpan, "parent").(trace.SpanContext)
	if parentContext.IsValid() {
		return
	}

	parentContext = c.span.SpanContext()
	correctParent := reflect.ValueOf(parentContext) // type: SpanContext

	spanContext := span.SpanContext().WithTraceID(parentContext.TraceID())
	correctContext := reflect.ValueOf(spanContext) // type: SpanContext

	forceSet(grpcSpan, "parent", correctParent)
	forceSet(grpcSpan, "spanContext", correctContext)
}

func forceSet(unexportedStruct reflect.Value, fieldName string, newValue reflect.Value) {
	field := unexportedStruct.FieldByName(fieldName)
	if !newValue.Type().AssignableTo(field.Type()) {
		panic("parent span struct has changed types")
	}
	if !field.CanSet() {
		// get a writeable reflect.Value by constructing a new reflect.Value from the pointer of the read only reflect.Value
		field = reflect.NewAt(field.Type(), field.Addr().UnsafePointer()).Elem()
	}
	field.Set(newValue)
}

func forceGet(unexportedStruct reflect.Value, fieldName string) any {
	field := unexportedStruct.FieldByName(fieldName)
	if !field.CanInterface() {
		// get an exportable reflect.Value by constructing a new reflect.Value from the pointer of the unexportable reflect.Value
		field = reflect.NewAt(field.Type(), field.Addr().UnsafePointer()).Elem()
	}
	return field.Interface()
}
