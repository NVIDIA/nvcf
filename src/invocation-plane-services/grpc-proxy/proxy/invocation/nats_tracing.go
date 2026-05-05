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
package invocation

import (
	"github.com/nats-io/nats.go"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel/propagation"
)

// NatsHeaderCarrier covers the case-sensitive nats.Header since propagation.HeaderCarrier is case-insensitive.
type NatsHeaderCarrier nats.Header

// Compile time check that NatsHeaderCarrier implements the propagation.TextMapCarrier.
var _ propagation.TextMapCarrier = NatsHeaderCarrier{}

// Get returns the value associated with the passed key.
func (c NatsHeaderCarrier) Get(key string) string {
	return nats.Header(c).Get(key)
}

// Set stores the key-value pair.
func (c NatsHeaderCarrier) Set(key, value string) {
	nats.Header(c).Set(key, value)
}

// Keys lists the keys stored in this carrier.
func (c NatsHeaderCarrier) Keys() []string {
	return lo.Keys(c)
}
