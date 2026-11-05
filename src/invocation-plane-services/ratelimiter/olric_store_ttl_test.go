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
	"reflect"
	"testing"
	"time"

	"github.com/olric-data/olric"
	"github.com/ulule/limiter/v3"
)

// ttlTestApply runs opt against a fresh PutConfig. That type lives in an
// internal olric package we can't import, so build it with reflection.
func ttlTestApply(opt olric.PutOption) reflect.Value {
	fn := reflect.ValueOf(opt)
	cfg := reflect.New(fn.Type().In(0).Elem())
	fn.Call([]reflect.Value{cfg})
	return cfg.Elem()
}

func TestPreserveExpiryOption_UsesAbsoluteExpiry(t *testing.T) {
	expiry := time.Now().Add(30 * time.Second).UnixMilli()
	cfg := ttlTestApply(preserveExpiryOption(expiry))

	if !cfg.FieldByName("HasPXAT").Bool() {
		t.Fatal("expected PXAT so the key keeps its original expiry")
	}
	if cfg.FieldByName("HasPX").Bool() {
		t.Fatal("relative PX restarts the clock on every increment and truncates sub-ms values to 0")
	}
	if got := time.Duration(cfg.FieldByName("PXAT").Int()).Milliseconds(); got != expiry {
		t.Fatalf("PXAT = %dms, want %dms", got, expiry)
	}
}

func TestNewKeyPutOptions_NeverTruncatesToZeroMillis(t *testing.T) {
	for _, period := range []time.Duration{time.Nanosecond, 400 * time.Microsecond, time.Millisecond, time.Minute} {
		opts := newKeyPutOptions(limiter.Rate{Period: period, Limit: 1})
		if len(opts) != 1 {
			t.Fatalf("period %v: got %d options, want 1", period, len(opts))
		}
		cfg := ttlTestApply(opts[0])
		if !cfg.FieldByName("HasPX").Bool() {
			t.Fatalf("period %v: PX not set", period)
		}
		if ms := time.Duration(cfg.FieldByName("PX").Int()).Milliseconds(); ms < 1 {
			t.Fatalf("period %v: PX truncates to %dms; Olric treats 0 as no TTL", period, ms)
		}
	}
}
