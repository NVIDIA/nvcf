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

package logical

import (
	"testing"
	"time"
)

func TestLeaseOptionsLeaseTotal(t *testing.T) {
	var l LeaseOptions
	l.TTL = 1 * time.Hour

	actual := l.LeaseTotal()
	expected := l.TTL
	if actual != expected {
		t.Fatalf("bad: %s", actual)
	}
}

func TestLeaseOptionsLeaseTotal_grace(t *testing.T) {
	var l LeaseOptions
	l.TTL = 1 * time.Hour

	actual := l.LeaseTotal()
	if actual != l.TTL {
		t.Fatalf("bad: %s", actual)
	}
}

func TestLeaseOptionsLeaseTotal_negLease(t *testing.T) {
	var l LeaseOptions
	l.TTL = -1 * 1 * time.Hour

	actual := l.LeaseTotal()
	expected := time.Duration(0)
	if actual != expected {
		t.Fatalf("bad: %s", actual)
	}
}

func TestLeaseOptionsExpirationTime(t *testing.T) {
	var l LeaseOptions
	l.TTL = 1 * time.Hour

	limit := time.Now().Add(time.Hour)
	exp := l.ExpirationTime()
	if exp.Before(limit) {
		t.Fatalf("bad: %s", exp)
	}
}

func TestLeaseOptionsExpirationTime_noLease(t *testing.T) {
	var l LeaseOptions
	if !l.ExpirationTime().IsZero() {
		t.Fatal("should be zero")
	}
}
