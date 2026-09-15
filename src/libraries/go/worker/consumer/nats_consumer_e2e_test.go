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

package consumer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/test/testutils"
)

const (
	testFunctionVersionId = "fv1"
	testPrimaryRegion     = "region-1"
)

// streamName / consumerName mirror the naming scheme in getRegionalNatsConsumer.
func streamName(region string) string {
	return fmt.Sprintf("rq_%s_%s", region, testFunctionVersionId)
}

func consumerName(region string) string {
	return streamName(region) + "_workers"
}

// subjectFor is a concrete subject under the stream's wildcard subject space.
func subjectFor(region string) string {
	return streamName(region) + ".work"
}

// newJetStream spins up an in-process NATS JetStream server and returns a
// connected JetStream context. The server and connection are torn down via
// t.Cleanup so tests stay parallel-safe with no fixed ports.
func newJetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	sc, err := testutils.NewNatsSuperCluster(t)
	require.NoError(t, err)
	t.Cleanup(sc.Shutdown)

	url := sc.Clusters[0].Servers[0].ClientURL()
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	return js
}

// provisionRegion creates the stream and the durable pull consumer that
// getRegionalNatsConsumer expects to find for the given region. It is wired in
// as the ProvisionRegionFunc so the consumer's retry/provision path is
// exercised end to end.
func provisionRegion(js jetstream.JetStream) ProvisionRegionFunc {
	return func(ctx context.Context, region string) error {
		_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     streamName(region),
			Subjects: []string{streamName(region) + ".>"},
			Storage:  jetstream.MemoryStorage,
			Replicas: 1,
		})
		if err != nil {
			return err
		}
		_, err = js.CreateOrUpdateConsumer(ctx, streamName(region), jetstream.ConsumerConfig{
			Durable:   consumerName(region),
			AckPolicy: jetstream.AckExplicitPolicy,
		})
		return err
	}
}

// publishWorkRequest marshals a valid WorkRequest proto and publishes it to the
// region's stream subject. Returns the request id for assertions.
func publishWorkRequest(t *testing.T, js jetstream.JetStream, region, requestId string) {
	t.Helper()
	req := &pb.WorkerInvokeFunctionRequest{RequestId: requestId}
	data, err := proto.Marshal(req)
	require.NoError(t, err)
	_, err = js.Publish(context.Background(), subjectFor(region), data)
	require.NoError(t, err)
}

// TestFetchWorkStreamDispatchesValidWork verifies the happy path: a valid
// WorkRequest proto published to the primary region's stream is decoded and
// delivered on the FetchWorkStream channel with its RequestId intact, and a
// work-limiter slot is acquired for it (released only when Close is called).
func TestFetchWorkStreamDispatchesValidWork(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := NewNatsConsumer(ctx, js, provisionRegion(js), testFunctionVersionId, testPrimaryRegion, nil, 4)
	require.NoError(t, err)

	publishWorkRequest(t, js, testPrimaryRegion, "req-happy")

	ch, err := c.FetchWorkStream(ctx)
	require.NoError(t, err)

	select {
	case wr := <-ch:
		require.NotNil(t, wr)
		assert.Equal(t, "req-happy", wr.RequestData.GetRequestId())
		assert.Equal(t, testPrimaryRegion, wr.Region)
		// one slot consumed by the dispatched work
		assert.Equal(t, int64(3), c.workLimiter.MaxAvailable())
		// releasing the work returns the slot, and is idempotent
		assert.NoError(t, wr.Close())
		assert.NoError(t, wr.Close())
		assert.Eventually(t, func() bool {
			return c.workLimiter.MaxAvailable() == 4
		}, 2*time.Second, 5*time.Millisecond)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for dispatched work")
	}
}

// TestFetchWorkStreamMultipleMessages verifies several valid requests are all
// dispatched and that each acquires a distinct work-limiter slot.
func TestFetchWorkStreamMultipleMessages(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const n = 3
	c, err := NewNatsConsumer(ctx, js, provisionRegion(js), testFunctionVersionId, testPrimaryRegion, nil, n)
	require.NoError(t, err)

	for i := 0; i < n; i++ {
		publishWorkRequest(t, js, testPrimaryRegion, fmt.Sprintf("req-%d", i))
	}

	ch, err := c.FetchWorkStream(ctx)
	require.NoError(t, err)

	seen := map[string]struct{}{}
	requests := make([]*WorkRequest, 0, n)
	for i := 0; i < n; i++ {
		select {
		case wr := <-ch:
			require.NotNil(t, wr)
			seen[wr.RequestData.GetRequestId()] = struct{}{}
			requests = append(requests, wr)
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for message %d", i)
		}
	}
	assert.Len(t, seen, n)
	// all slots consumed
	assert.Equal(t, int64(0), c.workLimiter.MaxAvailable())

	for _, wr := range requests {
		assert.NoError(t, wr.Close())
	}
	assert.Eventually(t, func() bool {
		return c.workLimiter.MaxAvailable() == int64(n)
	}, 2*time.Second, 5*time.Millisecond)
}

// TestFetchWorkStreamNakWhenAtCapacity documents that when the work limiter is
// already exhausted, FetchWorkStream does not dispatch. We saturate the limiter
// manually before publishing, then confirm via consumer state that the message
// stays pending and undelivered (the fetch is sized by the limiter, so it
// blocks rather than pulling) rather than dispatched. Once capacity frees up the
// message is delivered and dispatched.
func TestFetchWorkStreamNakWhenAtCapacity(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c, err := NewNatsConsumer(ctx, js, provisionRegion(js), testFunctionVersionId, testPrimaryRegion, nil, 1)
	require.NoError(t, err)

	// Take the only slot so the dispatcher must nak.
	require.True(t, c.workLimiter.TryAcquire(1))

	publishWorkRequest(t, js, testPrimaryRegion, "req-capacity")

	ch, err := c.FetchWorkStream(ctx)
	require.NoError(t, err)

	// State-driven confirmation that capacity gating holds. The dispatcher sizes
	// its fetch by the work limiter, which we have exhausted, so fetchMax blocks
	// waiting for a free slot and never pulls the message: it sits pending in the
	// stream, undelivered. This is deterministic consumer state rather than a
	// wall-clock window.
	cons, err := js.Consumer(ctx, streamName(testPrimaryRegion), consumerName(testPrimaryRegion))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, infoErr := cons.Info(ctx)
		return infoErr == nil && info.NumPending == 1 && info.Delivered.Consumer == 0
	}, 5*time.Second, 20*time.Millisecond, "message should sit pending and undelivered while at capacity")

	// And it must not have been dispatched while at capacity.
	select {
	case wr := <-ch:
		t.Fatalf("did not expect dispatch while at capacity, got %q", wr.RequestData.GetRequestId())
	default:
	}

	// Free the slot; the redelivered message should now be dispatched.
	c.releaseWork()
	select {
	case wr := <-ch:
		require.NotNil(t, wr)
		assert.Equal(t, "req-capacity", wr.RequestData.GetRequestId())
		assert.NoError(t, wr.Close())
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for redelivered work after capacity freed")
	}
}

// TestNewNatsConsumerSecondaryRegions verifies secondary-region consumers are
// constructed and reported as connected, and that work from a secondary region
// is dispatched alongside the primary.
func TestNewNatsConsumerSecondaryRegions(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	secondary := []string{"region-2"}
	c, err := NewNatsConsumer(ctx, js, provisionRegion(js), testFunctionVersionId, testPrimaryRegion, secondary, 8)
	require.NoError(t, err)

	connected := c.GetCurrentlyConnectedRegions()
	assert.Contains(t, connected, testPrimaryRegion)
	assert.Contains(t, connected, "region-2")
	assert.Len(t, connected, 2)

	publishWorkRequest(t, js, testPrimaryRegion, "req-primary")
	publishWorkRequest(t, js, "region-2", "req-secondary")

	ch, err := c.FetchWorkStream(ctx)
	require.NoError(t, err)

	seen := map[string]struct{}{}
	for i := 0; i < 2; i++ {
		select {
		case wr := <-ch:
			require.NotNil(t, wr)
			seen[wr.RequestData.GetRequestId()] = struct{}{}
			assert.NoError(t, wr.Close())
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for message %d (seen=%v)", i, seen)
		}
	}
	assert.Contains(t, seen, "req-primary")
	assert.Contains(t, seen, "req-secondary")
}

// TestNewNatsConsumerSecondaryRegionBestEffort verifies that a secondary region
// whose stream/consumer cannot be provisioned is skipped (best effort) while
// the primary still comes up. The provision func only knows about the primary
// region, so the secondary fails and must be dropped from connected regions.
func TestNewNatsConsumerSecondaryRegionBestEffort(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	provisionPrimaryOnly := func(ctx context.Context, region string) error {
		if region != testPrimaryRegion {
			return fmt.Errorf("provision not supported for region %s", region)
		}
		return provisionRegion(js)(ctx, region)
	}

	c, err := NewNatsConsumer(ctx, js, provisionPrimaryOnly, testFunctionVersionId, testPrimaryRegion, []string{"region-2"}, 4)
	require.NoError(t, err)

	connected := c.GetCurrentlyConnectedRegions()
	assert.Contains(t, connected, testPrimaryRegion)
	assert.NotContains(t, connected, "region-2")
	assert.Len(t, connected, 1)
}

// TestNewNatsConsumerPrimaryRegionFailure verifies that if the primary region
// stream cannot be provisioned, NewNatsConsumer returns an error.
func TestNewNatsConsumerPrimaryRegionFailure(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	failingProvision := func(ctx context.Context, region string) error {
		return fmt.Errorf("cannot provision %s", region)
	}

	_, err := NewNatsConsumer(ctx, js, failingProvision, testFunctionVersionId, testPrimaryRegion, nil, 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create consumer for primary region")
}

// TestFetchWorkStreamContextCancelStopsConsumers verifies that cancelling the
// context drives FetchWorkStream's output channel to close. This exercises the
// shutdown / drain path in singleConsumer and fetchMax.
func TestFetchWorkStreamContextCancelStopsConsumers(t *testing.T) {
	// Each NATS test stands up its own NewNatsSuperCluster on OS-assigned
	// ephemeral ports (nats-config/single-server.conf uses listen -1 / http -1)
	// and tears it down via t.Cleanup, so concurrent servers no longer collide
	// on fixed ports. There is no shared global state, so these run in parallel.
	t.Parallel()
	js := newJetStream(t)

	ctx, cancel := context.WithCancel(context.Background())

	c, err := NewNatsConsumer(ctx, js, provisionRegion(js), testFunctionVersionId, testPrimaryRegion, nil, 4)
	require.NoError(t, err)

	ch, err := c.FetchWorkStream(ctx)
	require.NoError(t, err)

	cancel()

	assert.Eventually(t, func() bool {
		select {
		case _, ok := <-ch:
			return !ok
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "expected work channel to close after context cancel")

	// once closed, the primary region is no longer reported as connected
	assert.Eventually(t, func() bool {
		_, ok := c.GetCurrentlyConnectedRegions()[testPrimaryRegion]
		return !ok
	}, 10*time.Second, 10*time.Millisecond)
}

// TestNewWorkRequestMalformedReturnsError verifies NewWorkRequest rejects a
// malformed proto payload by returning an error and a nil request.
func TestNewWorkRequestMalformedReturnsError(t *testing.T) {
	t.Parallel()

	msg := &fakeMsg{data: []byte("\xff\xff\xff\xff garbage")}
	wr, err := NewWorkRequest(msg, testPrimaryRegion, func() {})

	require.Error(t, err)
	assert.Nil(t, wr)
}

// TestNewWorkRequestValidNoRelease confirms the symmetric good path: a valid
// proto does NOT invoke release at construction time. The slot is only released
// later via Close.
func TestNewWorkRequestValidNoRelease(t *testing.T) {
	t.Parallel()

	var released atomic.Int32
	release := func() { released.Add(1) }

	data, err := proto.Marshal(&pb.WorkerInvokeFunctionRequest{RequestId: "ok"})
	require.NoError(t, err)

	msg := &fakeMsg{data: data}
	wr, err := NewWorkRequest(msg, testPrimaryRegion, release)
	require.NoError(t, err)
	require.NotNil(t, wr)
	assert.Equal(t, "ok", wr.RequestData.GetRequestId())
	assert.Equal(t, int32(0), released.Load())

	// Close releases exactly once, even when called multiple times.
	assert.NoError(t, wr.Close())
	assert.NoError(t, wr.Close())
	assert.Equal(t, int32(1), released.Load())
}

// fakeMsg is a minimal jetstream.Msg implementation for unit-testing
// NewWorkRequest without a live NATS server. Only Data() is meaningful; the
// other methods satisfy the interface and are not exercised by NewWorkRequest.
type fakeMsg struct {
	data    []byte
	subject string
	mu      sync.Mutex
	nakd    bool
}

func (m *fakeMsg) Data() []byte                    { return m.data }
func (m *fakeMsg) Headers() nats.Header            { return nil }
func (m *fakeMsg) Subject() string                 { return m.subject }
func (m *fakeMsg) Reply() string                   { return "" }
func (m *fakeMsg) Ack() error                      { return nil }
func (m *fakeMsg) DoubleAck(context.Context) error { return nil }
func (m *fakeMsg) Nak() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nakd = true
	return nil
}
func (m *fakeMsg) NakWithDelay(time.Duration) error          { return nil }
func (m *fakeMsg) InProgress() error                         { return nil }
func (m *fakeMsg) Term() error                               { return nil }
func (m *fakeMsg) TermWithReason(string) error               { return nil }
func (m *fakeMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
