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
	"testing"
	"time"

	"github.com/olric-data/olric"
	"github.com/ulule/limiter/v3"
)

// fakePutCall records a single call made to fakeDMap.Put.
type fakePutCall struct {
	key        string
	value      interface{}
	numOptions int
}

// fakeDMap is a minimal olric.DMap test double. Store.Get and Store.Reset
// only ever call Put, Get and Delete, so every other method panics if
// called - an unexpected dependency on them should fail the test loudly
// rather than silently return a zero value.
type fakeDMap struct {
	getResult *olric.GetResponse
	getErr    error

	putCalls []fakePutCall
	putErr   error
}

var _ olric.DMap = (*fakeDMap)(nil)

func (f *fakeDMap) Name() string { return "test" }

func (f *fakeDMap) Put(_ context.Context, key string, value interface{}, options ...olric.PutOption) error {
	f.putCalls = append(f.putCalls, fakePutCall{key: key, value: value, numOptions: len(options)})
	return f.putErr
}

func (f *fakeDMap) Get(context.Context, string) (*olric.GetResponse, error) {
	return f.getResult, f.getErr
}

func (f *fakeDMap) Delete(context.Context, ...string) (int, error) { panic("not implemented") }
func (f *fakeDMap) Incr(context.Context, string, int) (int, error) { panic("not implemented") }
func (f *fakeDMap) Decr(context.Context, string, int) (int, error) { panic("not implemented") }
func (f *fakeDMap) GetPut(context.Context, string, interface{}) (*olric.GetResponse, error) {
	panic("not implemented")
}
func (f *fakeDMap) IncrByFloat(context.Context, string, float64) (float64, error) {
	panic("not implemented")
}
func (f *fakeDMap) Expire(context.Context, string, time.Duration) error { panic("not implemented") }
func (f *fakeDMap) Lock(context.Context, string, time.Duration) (olric.LockContext, error) {
	panic("not implemented")
}
func (f *fakeDMap) LockWithTimeout(context.Context, string, time.Duration, time.Duration) (olric.LockContext, error) {
	panic("not implemented")
}
func (f *fakeDMap) Scan(context.Context, ...olric.ScanOption) (olric.Iterator, error) {
	panic("not implemented")
}
func (f *fakeDMap) Destroy(context.Context) error { panic("not implemented") }
func (f *fakeDMap) Pipeline(...olric.PipelineOption) (*olric.DMapPipeline, error) {
	panic("not implemented")
}
func (f *fakeDMap) Close(context.Context) error { panic("not implemented") }

// TestGet_NewKey_SetsTTLOnCreatingWrite is the core regression test for
// #1571: a brand-new key (dmap.Get returns ErrKeyNotFound) must be created
// with exactly one Put call that already carries its TTL. Before the fix,
// this path wrote the key once with no TTL and only set the TTL in a
// second, separate Put - which is exactly the gap that produced immortal
// keys.
func TestGet_NewKey_SetsTTLOnCreatingWrite(t *testing.T) {
	f := &fakeDMap{getErr: olric.ErrKeyNotFound}
	store := &Store{Prefix: "test", dmap: f}

	if _, err := store.Get(context.Background(), "user-1", limiter.Rate{Period: time.Second, Limit: 10}); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if len(f.putCalls) != 1 {
		t.Fatalf("expected exactly 1 Put call for a new key, got %d - a second follow-up write reopens the immortal-key gap from #1571", len(f.putCalls))
	}
	if got := f.putCalls[0]; got.numOptions != 1 {
		t.Fatalf("expected the creating Put to carry 1 TTL option, got %d - a new key must never be written without a TTL (#1571)", got.numOptions)
	}
	if f.putCalls[0].value != 1 {
		t.Fatalf("expected new key value 1, got %v", f.putCalls[0].value)
	}
}

// TestGet_NewKey_UnboundedTier_NoTTLOption preserves the pre-existing
// behavior for an unbounded limiter tier (rate.Period == 0): still exactly
// one Put, but with no TTL option, matching what the code did before #1571
// for this specific case.
func TestGet_NewKey_UnboundedTier_NoTTLOption(t *testing.T) {
	f := &fakeDMap{getErr: olric.ErrKeyNotFound}
	store := &Store{Prefix: "test", dmap: f}

	if _, err := store.Get(context.Background(), "user-1", limiter.Rate{Period: 0, Limit: 10}); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if len(f.putCalls) != 1 {
		t.Fatalf("expected exactly 1 Put call, got %d", len(f.putCalls))
	}
	if got := f.putCalls[0].numOptions; got != 0 {
		t.Fatalf("expected no TTL option for an unbounded (Period == 0) tier, got %d options", got)
	}
}

// TestGet_ExpiredKey_ResetGetsTTLOnSameWrite covers the second branch #1571
// identified: an existing key whose TTL has already lapsed (dmap.Get
// returns a nil result with no error) is reset to value 1. That reset must
// also carry the TTL on the same write, or the key goes immortal exactly
// like the new-key case above.
func TestGet_ExpiredKey_ResetGetsTTLOnSameWrite(t *testing.T) {
	f := &fakeDMap{} // getResult == nil, getErr == nil: key exists but expired/untimed
	store := &Store{Prefix: "test", dmap: f}

	if _, err := store.Get(context.Background(), "user-1", limiter.Rate{Period: time.Second, Limit: 10}); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if len(f.putCalls) != 1 {
		t.Fatalf("expected exactly 1 Put call when resetting an expired key, got %d", len(f.putCalls))
	}
	if got := f.putCalls[0]; got.numOptions != 1 {
		t.Fatalf("expected the reset Put to carry 1 TTL option, got %d - a key whose TTL already lapsed must never be rewritten without one (#1571)", got.numOptions)
	}
}

func (f *fakeDMap) CompareAndSwap(context.Context, string, []byte, interface{}, ...olric.PutOption) (bool, *olric.GetResponse, error) {
	panic("not implemented")
}

// TestGet_NewKey_SubMillisecondPeriod_TTLRoundedUp covers a direct Store.Get
// caller supplying a period below Olric's one-millisecond PX precision.
// Before this fix, newKeyPutOptions treated any period under 1ms as
// unbounded and wrote no TTL at all - reopening the immortal-key gap from
// #1571 for non-configured (programmatic) callers. Configured rates
// (parseRates) only ever produce S/M/H/D-granularity periods, so this case
// cannot occur from config today, but Store.Get itself accepts any
// limiter.Rate.
func TestGet_NewKey_SubMillisecondPeriod_TTLRoundedUp(t *testing.T) {
	f := &fakeDMap{getErr: olric.ErrKeyNotFound}
	store := &Store{Prefix: "test", dmap: f}

	if _, err := store.Get(context.Background(), "user-1", limiter.Rate{Period: 500 * time.Microsecond, Limit: 10}); err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if len(f.putCalls) != 1 {
		t.Fatalf("expected exactly 1 Put call, got %d", len(f.putCalls))
	}
	if got := f.putCalls[0].numOptions; got != 1 {
		t.Fatalf("expected a TTL option even for a sub-millisecond period, got %d options - a positive period must never be silently treated as unbounded", got)
	}
}

// TestExpirationFromTTLMillis_TreatsValueAsAbsoluteEpoch is the regression
// test for CodeRabbit's "Use result.TTL() as an absolute expiry" comment on
// PR #1942. olric.GetResponse.TTL() returns an absolute Unix-millisecond
// timestamp of when a key expires (confirmed against Olric's own internal
// atomic-increment code, which does time.UnixMilli(ttl) on the same value).
// The code previously treated that value as a millisecond *duration* and
// added it to time.Now(), which would push the computed expiration tens of
// thousands of years into the future instead of returning the key's real
// remaining lifetime.
func TestExpirationFromTTLMillis_TreatsValueAsAbsoluteEpoch(t *testing.T) {
	want := time.Now().Add(30 * time.Second)
	ttlMillis := want.UnixMilli()

	got := expirationFromTTLMillis(ttlMillis)

	diff := got.Sub(want)
	if diff < -time.Millisecond || diff > time.Millisecond {
		t.Fatalf("expected expiration ~%v, got %v (diff %v) - result.TTL() is an absolute epoch-ms timestamp, not a millisecond duration from now", want, got, diff)
	}
}
