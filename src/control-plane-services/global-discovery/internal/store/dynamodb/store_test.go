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

package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store/storetest"
)

func newTestStore(t *testing.T, db *fakeDB, opts ...func(*Options)) *Store {
	t.Helper()
	o := Options{Client: db, TableName: "gds", QueryConcurrency: 2}
	for _, fn := range opts {
		fn(&o)
	}
	st, err := New(o)
	require.NoError(t, err)
	return st
}

// TestConformanceFake runs the store suite against the in-memory fake.
// Every subtest gets its own Store on one shared fake, as replicas share
// one table; small query pages exercise pagination.
func TestConformanceFake(t *testing.T) {
	db := newFakeDB()
	storetest.Run(t, func(t *testing.T) store.Store { return newTestStore(t, db) })
}

func fastBackoff(t *testing.T) {
	base, maxB := baseBackoff, maxBackoff
	baseBackoff, maxBackoff = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { baseBackoff, maxBackoff = base, maxB })
}

func ck(cluster string) store.ClusterKey {
	return store.ClusterKey{ClusterSet: "cs", Cluster: cluster}
}

func ek(cluster, name string) store.ExportKey {
	return store.ExportKey{ClusterSet: "cs", Cluster: cluster, Namespace: "ns", Name: name}
}

func ak(cluster, id string, role store.FrontendRole) store.PoolAgentKey {
	return store.PoolAgentKey{ClusterSet: "cs", Cluster: cluster, PoolID: id, Role: role, AgentID: "pod-0"}
}

func pk(cluster, id string) store.PoolKey {
	return store.PoolKey{ClusterSet: "cs", Cluster: cluster, PoolID: id}
}

func TestNew(t *testing.T) {
	_, err := New(Options{TableName: "gds"})
	assert.ErrorContains(t, err, "Client is required")
	_, err = New(Options{Client: newFakeDB()})
	assert.ErrorContains(t, err, "TableName is required")

	st, err := New(Options{Client: newFakeDB(), TableName: "gds", QueryConcurrency: -3})
	require.NoError(t, err)
	assert.Equal(t, 1, st.concurrency)
	assert.Equal(t, DefaultExpiryAfter, st.expiryAfter)
	assert.NotNil(t, st.clock)

	st, err = New(Options{Client: newFakeDB(), TableName: "gds"})
	require.NoError(t, err)
	assert.Equal(t, DefaultQueryConcurrency, st.concurrency)
}

func TestClosedStore(t *testing.T) {
	st := newTestStore(t, newFakeDB())
	require.NoError(t, st.Ping(t.Context()))
	require.NoError(t, st.Close())
	require.NoError(t, st.Close(), "Close is idempotent")
	for name, err := range allCalls(t, st) {
		assert.ErrorIs(t, err, store.ErrUnavailable, name)
	}
}

// allCalls calls every Store method once and returns the errors by name.
func allCalls(t *testing.T, st *Store) map[string]error {
	ctx := t.Context()
	errs := map[string]error{}
	_, errs["PutCluster"] = st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	_, errs["RenewClusterLease"] = st.RenewClusterLease(ctx, ck("c1"), 1, t0)
	_, errs["GetCluster"] = st.GetCluster(ctx, ck("c1"))
	errs["DeleteCluster"] = st.DeleteCluster(ctx, ck("c1"))
	errs["PutExport"] = st.PutExport(ctx, store.ExportRecord{Key: ek("c1", "s"), Generation: 1})
	errs["DeleteExport"] = st.DeleteExport(ctx, ek("c1", "s"))
	errs["PutEndpointSet"] = st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek("c1", "s"), Generation: 1})
	errs["DeleteEndpointSet"] = st.DeleteEndpointSet(ctx, ek("c1", "s"))
	_, errs["PutPool"] = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p"), Name: "n", Generation: 1}, store.PutPoolOptions{Now: t0})
	_, errs["RenewPoolLease"] = st.RenewPoolLease(ctx, pk("c1", "p"), 1, t0, store.PoolHeartbeatPatch{})
	w := int32(1)
	_, errs["RenewPoolLeasePatch"] = st.RenewPoolLease(ctx, pk("c1", "p"), 1, t0, store.PoolHeartbeatPatch{WorkerCount: &w})
	errs["DeletePool"] = st.DeletePool(ctx, pk("c1", "p"))
	agent := store.PoolAgentRecord{Key: ak("c1", "p", store.FrontendRolePylon), PoolName: "n", Generation: 1}
	_, errs["PutPoolAgent"] = st.PutPoolAgent(ctx, agent, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	_, errs["RenewPoolAgentLease"] = st.RenewPoolAgentLease(ctx, agent.Key, 1, t0, store.PoolAgentHeartbeatPatch{})
	_, errs["RenewPoolAgentLeasePatch"] = st.RenewPoolAgentLease(ctx, agent.Key, 1, t0, store.PoolAgentHeartbeatPatch{Attributes: map[string]string{"a": "b"}})
	errs["DeletePoolAgent"] = st.DeletePoolAgent(ctx, agent.Key)
	_, errs["ListClusterSets"] = st.ListClusterSets(ctx)
	_, errs["LoadClusterSet"] = st.LoadClusterSet(ctx, "cs")
	errs["Ping"] = st.Ping(ctx)
	return errs
}

// TestBackendUnavailable checks that every method reports a throttled or
// unreachable backend as store.ErrUnavailable.
func TestBackendUnavailable(t *testing.T) {
	for _, cause := range []error{
		&ddbtypes.ProvisionedThroughputExceededException{Message: aws.String("slow down")},
		&ddbtypes.ResourceNotFoundException{Message: aws.String("no table")},
		errors.New("dial tcp: connection refused"),
	} {
		db := newFakeDB()
		db.before = func(string) error { return cause }
		st := newTestStore(t, db)
		for name, err := range allCalls(t, st) {
			assert.ErrorIs(t, err, store.ErrUnavailable, "%s with %v", name, cause)
		}
	}
}

// TestBackendUnavailableAfterFirstCall fails each operation kind only after
// the first call of that kind, which reaches the error paths behind the
// index and marker writes and the initial reads.
func TestBackendUnavailableAfterFirstCall(t *testing.T) {
	for _, failing := range []string{"GetItem", "PutItem", "UpdateItem", "Query", "BatchWriteItem", "TransactWriteItems", "DeleteItem"} {
		t.Run(failing, func(t *testing.T) {
			db := newFakeDB()
			st := newTestStore(t, db)
			ctx := t.Context()
			reg, err := st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
			require.NoError(t, err)
			_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p"), Name: "n", Generation: 1, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
			require.NoError(t, err)
			require.NoError(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek("c1", "s"), Generation: 1}))

			db.mu.Lock()
			db.before = func(op string) error {
				if op == failing {
					return &ddbtypes.InternalServerError{Message: aws.String("oops")}
				}
				return nil
			}
			db.mu.Unlock()
			errs := allCalls(t, st)
			_, errs["RenewClusterLeaseKnown"] = st.RenewClusterLease(ctx, ck("c1"), reg.Lease.Epoch, t0)
			failed := 0
			for name, err := range errs {
				if err != nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrLeaseEpochMismatch) && !errors.Is(err, store.ErrStaleGeneration{}) {
					assert.ErrorIs(t, err, store.ErrUnavailable, name)
					failed++
				}
			}
			assert.Positive(t, failed)
		})
	}
}

func TestCanceledDuringLoad(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	for i := range 5 {
		require.NoError(t, st.PutEndpointSet(t.Context(), store.EndpointSetRecord{Key: ek(fmt.Sprintf("c%d", i), "s"), Generation: 1}))
	}
	ctx, cancel := context.WithCancel(t.Context())
	queries := 0
	db.before = func(op string) error {
		if op == "Query" {
			queries++
			if queries == 2 {
				cancel()
			}
		}
		return nil
	}
	_, err := st.LoadClusterSet(ctx, "cs")
	assert.ErrorIs(t, err, context.Canceled)
}

func TestDeleteClusterRetriesUnprocessed(t *testing.T) {
	fastBackoff(t)
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	for i := range 30 {
		require.NoError(t, st.PutExport(ctx, store.ExportRecord{Key: ek("c1", fmt.Sprintf("svc-%02d", i)), Generation: 1}))
		require.NoError(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek("c1", fmt.Sprintf("svc-%02d", i)), Generation: 1}))
	}
	db.unprocessed = 3
	require.NoError(t, st.DeleteCluster(ctx, ck("c1")))
	assert.GreaterOrEqual(t, db.calls["BatchWriteItem"], 4, "62 deletes in 3 chunks plus retries")
	snap, err := st.LoadClusterSet(ctx, "cs")
	require.NoError(t, err)
	assert.Empty(t, snap.Exports)
	assert.Empty(t, snap.EndpointSets)

	require.NoError(t, st.PutExport(ctx, store.ExportRecord{Key: ek("c1", "svc"), Generation: 1}))
	db.unprocessed = 1000
	err = st.DeleteCluster(ctx, ck("c1"))
	assert.ErrorIs(t, err, store.ErrUnavailable)
	assert.ErrorContains(t, err, "still unprocessed")

	ctx2, cancel := context.WithCancel(ctx)
	db.before = func(op string) error {
		if op == "BatchWriteItem" {
			cancel()
		}
		return nil
	}
	assert.ErrorIs(t, st.DeleteCluster(ctx2, ck("c1")), context.Canceled)
}

func TestPutPoolTakesOverStaleReservation(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	// A reservation whose pool is gone, as after a TTL deletion of the pool.
	_, err := st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "ghost"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	db.mu.Lock()
	delete(db.items, itemID(poolKey(pk("c1", "ghost")).attributes()))
	db.mu.Unlock()

	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	holder := db.items[itemID(poolNameKey("cs", "c1", "pool-a").attributes())]
	assert.Equal(t, str("p1"), holder[attrPoolID])

	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p2"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	assert.ErrorIs(t, err, store.ErrPoolNameTaken, "a live holder keeps the name")
}

// TestPutPoolTakesOverRenamedHolder covers a reservation left behind by a
// pool that now carries another name, as when a pool expired and was
// registered again under its id with a new name.
func TestPutPoolTakesOverRenamedHolder(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	_, err := st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	db.mu.Lock()
	delete(db.items, itemID(poolKey(pk("c1", "p1")).attributes()))
	db.mu.Unlock()
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-b", Generation: 2}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)

	// The holder is renamed again between the read and the transaction.
	poolID := itemID(poolKey(pk("c1", "p1")).attributes())
	transactions := 0
	db.before = func(op string) error {
		if op == "TransactWriteItems" {
			transactions++
			if transactions == 2 {
				other, _ := parseItem(db.items[poolID])
				rec, _ := decodePool(other)
				rec.Name = "pool-c"
				body, _ := encodePoolBody(rec)
				db.items[poolID][attrBody] = bin(body)
			}
		}
		return nil
	}
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p2"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err, "the reservation of a pool that carries another name is stale")
	assert.Equal(t, 4, transactions, "a failed takeover is retried from a fresh read")
	holder := db.items[itemID(poolNameKey("cs", "c1", "pool-a").attributes())]
	assert.Equal(t, str("p2"), holder[attrPoolID])
}

func TestPutPoolTakeoverRaces(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	nameKey := poolNameKey("cs", "c1", "pool-a").attributes()
	setHolder := func(id string) {
		db.items[itemID(nameKey)] = map[string]ddbtypes.AttributeValue{attrPK: nameKey[attrPK], attrSK: nameKey[attrSK], attrPoolID: str(id)}
	}
	db.mu.Lock()
	setHolder("ghost")
	db.mu.Unlock()

	// The first takeover attempt loses: the reservation moves to another
	// missing pool. The second finds the holder's pool back. The third
	// fails for good.
	transactions := 0
	db.before = func(op string) error {
		if op != "TransactWriteItems" {
			return nil
		}
		transactions++
		switch transactions {
		case 2:
			setHolder("ghost-2")
		case 4:
			k := poolKey(pk("c1", "ghost-2"))
			db.items[itemID(k.attributes())] = k.attributes()
		}
		return nil
	}
	_, err := st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	assert.ErrorIs(t, err, store.ErrPoolNameTaken)
	assert.GreaterOrEqual(t, transactions, 4)

	db.before = nil
	db.mu.Lock()
	db.items[itemID(nameKey)] = map[string]ddbtypes.AttributeValue{attrPK: nameKey[attrPK], attrSK: nameKey[attrSK]}
	db.mu.Unlock()
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	assert.ErrorIs(t, err, store.ErrPoolNameTaken, "a reservation without a holder is never taken over")
}

// TestPoolNameHeldElsewhere covers a reservation of a pool's own old name
// that another pool holds: a rename and a delete leave it alone.
func TestPoolNameHeldElsewhere(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	_, err := st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	nameKey := poolNameKey("cs", "c1", "pool-a").attributes()
	db.mu.Lock()
	db.items[itemID(nameKey)][attrPoolID] = str("p9")
	db.mu.Unlock()

	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "pool-b", Generation: 2}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	assert.Equal(t, str("p9"), db.items[itemID(nameKey)][attrPoolID])

	nameKey = poolNameKey("cs", "c1", "pool-b").attributes()
	db.mu.Lock()
	db.items[itemID(nameKey)][attrPoolID] = str("p9")
	db.mu.Unlock()
	require.NoError(t, st.DeletePool(ctx, pk("c1", "p1")))
	assert.Equal(t, str("p9"), db.items[itemID(nameKey)][attrPoolID])
	snap, err := st.LoadClusterSet(ctx, "cs")
	require.NoError(t, err)
	assert.Empty(t, snap.Pools)
}

func TestPoolWritesRetryLostRaces(t *testing.T) {
	fastBackoff(t)
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	key := pk("c1", "p1")
	reg, err := st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 1, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	poolID := itemID(poolKey(key).attributes())

	// A transaction conflict is retried.
	conflicts := 1
	db.before = func(op string) error {
		if op == "TransactWriteItems" && conflicts > 0 {
			conflicts--
			return &ddbtypes.TransactionCanceledException{CancellationReasons: []ddbtypes.CancellationReason{
				{Code: aws.String("TransactionConflict")}, {Code: aws.String(reasonNone)},
			}}
		}
		return nil
	}
	_, err = st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 2}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)

	// Another cancellation reason is not.
	db.before = func(op string) error {
		if op == "TransactWriteItems" {
			return &ddbtypes.TransactionCanceledException{CancellationReasons: []ddbtypes.CancellationReason{
				{Code: aws.String("ThrottlingError")}, {Code: aws.String(reasonNone)},
			}}
		}
		return nil
	}
	_, err = st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 3}, store.PutPoolOptions{Now: t0})
	assert.ErrorIs(t, err, store.ErrUnavailable)
	assert.ErrorIs(t, st.DeletePool(ctx, key), store.ErrUnavailable)

	// A heartbeat patch that races with another writer of the body starts
	// over from the new body.
	updates := 0
	db.before = func(op string) error {
		if op == "UpdateItem" {
			updates++
			if updates == 1 {
				other, _ := parseItem(db.items[poolID])
				rec, _ := decodePool(other)
				rec.Spec.Labels = map[string]string{"changed": "concurrently"}
				body, _ := encodePoolBody(rec)
				db.items[poolID][attrBody] = bin(body)
			}
		}
		return nil
	}
	ready := int32(9)
	got, err := st.RenewPoolLease(ctx, key, reg.Lease.Epoch, t0.Add(time.Second), store.PoolHeartbeatPatch{ReadyWorkerCount: &ready})
	require.NoError(t, err)
	assert.Equal(t, 2, updates)
	assert.Equal(t, int32(9), got.Spec.ReadyWorkerCount)
	assert.Equal(t, map[string]string{"changed": "concurrently"}, got.Spec.Labels)

	// A pool deleted between the read and the write is not found.
	db.before = func(op string) error {
		if op == "UpdateItem" {
			delete(db.items, poolID)
		}
		return nil
	}
	ready = 10
	_, err = st.RenewPoolLease(ctx, key, reg.Lease.Epoch, t0, store.PoolHeartbeatPatch{ReadyWorkerCount: &ready})
	assert.ErrorIs(t, err, store.ErrNotFound)

	// A delete that loses to a concurrent write starts over; one that
	// loses to a concurrent delete is done.
	db.before = nil
	_, err = st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	transactions := 0
	db.before = func(op string) error {
		if op == "TransactWriteItems" {
			transactions++
			if transactions == 1 {
				db.items[poolID][attrGen] = numU(99)
			}
		}
		return nil
	}
	require.NoError(t, st.DeletePool(ctx, key))
	assert.Equal(t, 2, transactions)
	_, err = st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	db.before = func(op string) error {
		if op == "TransactWriteItems" {
			delete(db.items, poolID)
		}
		return nil
	}
	require.NoError(t, st.DeletePool(ctx, key))
}

// TestPutPoolKeepsConcurrentRenewal checks that an update which keeps the
// lease does not undo a heartbeat that lands between its read and write.
func TestPutPoolKeepsConcurrentRenewal(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	key := pk("c1", "p1")
	reg, err := st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 1, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	first := true
	db.before = func(op string) error {
		if op == "TransactWriteItems" && first {
			first = false
			db.items[itemID(poolKey(key).attributes())][attrLeaseRenewedAt] = numI(t0.Add(time.Minute).UnixNano())
		}
		return nil
	}
	got, err := st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 2}, store.PutPoolOptions{Now: t0.Add(2 * time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, reg.Lease.Epoch, got.Lease.Epoch)
	assert.True(t, got.Lease.RenewedAt.Equal(t0.Add(time.Minute)), "got %v", got.Lease.RenewedAt)
	snap, err := st.LoadClusterSet(ctx, "cs")
	require.NoError(t, err)
	require.Len(t, snap.Pools, 1)
	assert.True(t, snap.Pools[0].Lease.RenewedAt.Equal(t0.Add(time.Minute)))
}

// TestPutPoolRaceWithConcurrentWriter makes the pool change between the
// read and the transaction; the put starts over and sees the newer
// generation.
func TestPutPoolRaceWithConcurrentWriter(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	key := pk("c1", "p1")
	_, err := st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	poolID := itemID(poolKey(key).attributes())
	first := true
	db.before = func(op string) error {
		if op == "TransactWriteItems" && first {
			first = false
			db.items[poolID][attrGen] = numU(5)
		}
		return nil
	}
	_, err = st.PutPool(ctx, store.PoolRecord{Key: key, Name: "pool-a", Generation: 3}, store.PutPoolOptions{Now: t0})
	current, ok := store.StaleGeneration(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, uint64(5), current)
}

func TestContention(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	_, err := st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	require.NoError(t, err)
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "n", Generation: 1, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)

	// Another writer changes every item before each write.
	gen := uint64(100)
	db.before = func(op string) error {
		switch op {
		case "PutItem", "UpdateItem", "TransactWriteItems":
			gen++
			for _, it := range db.items {
				if _, ok := it[attrGen]; ok {
					it[attrGen] = numU(gen)
					it[attrBody] = bin([]byte(fmt.Sprintf("\x0a\x01%c", 'a'+gen%26)))
				}
			}
		}
		return nil
	}
	_, err = st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	assert.ErrorIs(t, err, store.ErrUnavailable)
	assert.ErrorContains(t, err, "too many concurrent writes")
}

func TestEndpointMarkCache(t *testing.T) {
	db := newFakeDB()
	now := t0
	st := newTestStore(t, db, func(o *Options) { o.Clock = func() time.Time { return now } })
	ctx := t.Context()
	markWrites := 0
	db.before = func(op string) error {
		if op == "PutItem" {
			markWrites++
		}
		return nil
	}
	put := func(gen uint64) {
		t.Helper()
		require.NoError(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek("c1", "s"), Generation: gen}))
	}
	put(1) // index, marker, set
	put(2) // set
	assert.Equal(t, 4, markWrites)
	now = now.Add(markCacheTTL + time.Second)
	put(3) // marker, set
	assert.Equal(t, 6, markWrites)
	require.NoError(t, st.DeleteCluster(ctx, ck("c1")))
	put(4) // marker, set
	assert.Equal(t, 8, markWrites)
	snap, err := st.LoadClusterSet(ctx, "cs")
	require.NoError(t, err)
	require.Len(t, snap.EndpointSets, 1, "an orphan endpoint set is found through its marker")
}

func TestLoadClusterSetErrors(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	require.NoError(t, st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek("c1", "s"), Generation: 1}))
	setID := itemID(endpointSetKey(ek("c1", "s")).attributes())
	db.items[setID][attrBody] = bin([]byte{0xff})
	_, err := st.LoadClusterSet(ctx, "cs")
	assert.ErrorContains(t, err, "unmarshal body")

	db.items[setID][attrGen] = str("not a number")
	_, err = st.LoadClusterSet(ctx, "cs")
	assert.ErrorContains(t, err, "unmarshal item")
	delete(db.items, setID)

	for _, write := range []func(){
		func() {
			_, _ = st.PutPoolAgent(ctx, store.PoolAgentRecord{Key: ak("c2", "p", store.FrontendRoleOpenAI), Generation: 1},
				store.PutPoolAgentOptions{Now: t0, MintLease: true})
		},
		func() { _, _ = st.PutCluster(ctx, store.ClusterRecord{Key: ck("c2")}, t0) },
		func() { _ = st.PutExport(ctx, store.ExportRecord{Key: ek("c2", "s"), Generation: 1}) },
		func() {
			_, _ = st.PutPool(ctx, store.PoolRecord{Key: pk("c2", "p"), Name: "n", Generation: 1}, store.PutPoolOptions{Now: t0})
		},
	} {
		write()
		for id, it := range db.items {
			if strings.HasPrefix(id, "CS#cs\x00") && it[attrBody] != nil {
				it[attrBody] = bin([]byte{0xff})
			}
		}
		_, err = st.LoadClusterSet(ctx, "cs")
		assert.ErrorContains(t, err, "unmarshal body")
		for id := range db.items {
			if strings.HasPrefix(id, "CS#cs\x00") {
				delete(db.items, id)
			}
		}
	}
}

func TestCorruptItemsFailReadModifyWrite(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	reg, err := st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	require.NoError(t, err)
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "n", Generation: 1, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	for _, it := range db.items {
		if it[attrBody] != nil {
			it[attrBody] = bin([]byte{0xff})
		}
	}
	_, err = st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	assert.Error(t, err)
	_, err = st.GetCluster(ctx, ck("c1"))
	assert.Error(t, err)
	_, err = st.RenewClusterLease(ctx, ck("c1"), reg.Lease.Epoch, t0)
	assert.Error(t, err)
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p1"), Name: "n", Generation: 2}, store.PutPoolOptions{Now: t0})
	assert.Error(t, err)
	w := int32(3)
	_, err = st.RenewPoolLease(ctx, pk("c1", "p1"), 1, t0, store.PoolHeartbeatPatch{WorkerCount: &w})
	assert.Error(t, err)
	assert.Error(t, st.DeletePool(ctx, pk("c1", "p1")))

	agent := store.PoolAgentRecord{Key: ak("c1", "p1", store.FrontendRolePylon), Generation: 1, Lease: store.Lease{TTL: time.Minute}}
	reg, err = st.PutCluster(ctx, store.ClusterRecord{Key: ck("c9")}, t0)
	require.NoError(t, err)
	_, err = st.PutPoolAgent(ctx, agent, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	db.items[itemID(poolAgentKey(agent.Key).attributes())][attrBody] = bin([]byte{0xff})
	agent.Generation = 2
	_, err = st.PutPoolAgent(ctx, agent, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	assert.Error(t, err)
	_, err = st.RenewPoolAgentLease(ctx, agent.Key, reg.Lease.Epoch, t0, store.PoolAgentHeartbeatPatch{Attributes: map[string]string{"a": "b"}})
	assert.Error(t, err)
	_, err = st.RenewPoolAgentLease(ctx, agent.Key, reg.Lease.Epoch, t0, store.PoolAgentHeartbeatPatch{})
	assert.Error(t, err)
}

func TestInvalidRecords(t *testing.T) {
	st := newTestStore(t, newFakeDB())
	ctx := t.Context()
	err := st.PutExport(ctx, store.ExportRecord{Key: ek("c1", "s"), Spec: store.ExportSpec{Type: "LoadBalancer"}})
	assert.ErrorContains(t, err, "unknown service type")
	err = st.PutEndpointSet(ctx, store.EndpointSetRecord{Key: ek("c1", "s"), Endpoints: []store.Endpoint{{AddressType: "MAC"}}})
	assert.ErrorContains(t, err, "unknown address type")
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p"), Spec: store.PoolSpec{DesiredState: "GONE"}}, store.PutPoolOptions{Now: t0})
	assert.ErrorContains(t, err, "unknown desired state")

	big := strings.Repeat("x", maxItemBytes)
	err = st.PutExport(ctx, store.ExportRecord{Key: ek("c1", "s"), Spec: store.ExportSpec{Annotations: map[string]string{"big": big}}})
	assert.ErrorIs(t, err, store.ErrTooLarge)
	_, err = st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1"), Labels: map[string]string{"big": big}}, t0)
	assert.ErrorIs(t, err, store.ErrTooLarge)
	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p"), Spec: store.PoolSpec{Labels: map[string]string{"big": big}}}, store.PutPoolOptions{Now: t0})
	assert.ErrorIs(t, err, store.ErrTooLarge)
	_, err = st.PutPoolAgent(ctx, store.PoolAgentRecord{Key: ak("c1", "p", store.FrontendRolePylon), Attributes: map[string]string{"big": big}},
		store.PutPoolAgentOptions{Now: t0})
	assert.ErrorIs(t, err, store.ErrTooLarge)
	_, err = st.PutPoolAgent(ctx, store.PoolAgentRecord{Key: ak("c1", "p", "ROUTER")}, store.PutPoolAgentOptions{Now: t0})
	assert.ErrorContains(t, err, "unknown frontend role")

	_, err = st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p"), Name: "n", Generation: 1, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	_, err = st.RenewPoolLease(ctx, pk("c1", "p"), 0, t0, store.PoolHeartbeatPatch{DesiredState: "GONE"})
	assert.ErrorIs(t, err, store.ErrLeaseEpochMismatch)
	_, err = st.RenewPoolLease(ctx, pk("c1", "p"), uint64(t0.UnixMilli()), t0, store.PoolHeartbeatPatch{DesiredState: "GONE"})
	assert.ErrorContains(t, err, "unknown desired state")
}

func TestExpiry(t *testing.T) {
	db := newFakeDB()
	now := t0
	st := newTestStore(t, db, func(o *Options) {
		o.ExpiryAfter = time.Hour
		o.Clock = func() time.Time { return now }
	})
	ctx := t.Context()
	reg, err := st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	require.NoError(t, err)
	clusterID := itemID(clusterKey(ck("c1")).attributes())
	assert.Equal(t, numI(t0.Add(time.Hour).Unix()), db.items[clusterID][attrExpiresAt])

	// A write without a server time expires from the store's clock.
	now = t0.Add(10 * time.Minute)
	_, err = st.RenewClusterLease(ctx, ck("c1"), reg.Lease.Epoch, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, numI(now.Add(time.Hour).Unix()), db.items[clusterID][attrExpiresAt])
	assert.NotContains(t, db.items[clusterID], attrLeaseRenewedAt, "a zero renewal time is stored as absent")

	off := newTestStore(t, db, func(o *Options) { o.ExpiryAfter = -1 })
	_, err = off.PutCluster(ctx, store.ClusterRecord{Key: ck("c2")}, t0)
	require.NoError(t, err)
	assert.NotContains(t, db.items[itemID(clusterKey(ck("c2")).attributes())], attrExpiresAt)
	_, err = off.PutPool(ctx, store.PoolRecord{Key: pk("c2", "p"), Name: "n", Generation: 1}, store.PutPoolOptions{Now: t0})
	require.NoError(t, err)
	assert.NotContains(t, db.items[itemID(poolKey(pk("c2", "p")).attributes())], attrExpiresAt)

	// Agent registrations expire like pools, and a heartbeat extends them.
	agent := store.PoolAgentRecord{Key: ak("c1", "p", store.FrontendRolePylon), Generation: 1, Lease: store.Lease{TTL: time.Minute}}
	areg, err := st.PutPoolAgent(ctx, agent, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	agentID := itemID(poolAgentKey(agent.Key).attributes())
	assert.Equal(t, numI(t0.Add(time.Hour).Unix()), db.items[agentID][attrExpiresAt])
	_, err = st.RenewPoolAgentLease(ctx, agent.Key, areg.Lease.Epoch, t0.Add(time.Minute), store.PoolAgentHeartbeatPatch{})
	require.NoError(t, err)
	assert.Equal(t, numI(t0.Add(time.Minute+time.Hour).Unix()), db.items[agentID][attrExpiresAt])
}

// TestPoolAgentWritesRetryLostRaces makes an agent registration change
// between the store's read and its write.
func TestPoolAgentWritesRetryLostRaces(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	key := ak("c1", "p1", store.FrontendRolePylon)
	agentID := itemID(poolAgentKey(key).attributes())
	rec := store.PoolAgentRecord{Key: key, PoolName: "pool-a", Generation: 1, Lease: store.Lease{TTL: time.Minute}}
	reg, err := st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	require.NoError(t, err)

	// A concurrent registration with a higher generation wins.
	first := true
	db.before = func(op string) error {
		if op == "PutItem" && first {
			first = false
			db.items[agentID][attrGen] = numU(5)
		}
		return nil
	}
	rec.Generation = 3
	_, err = st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: t0.Add(time.Second), MintLease: true})
	current, ok := store.StaleGeneration(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, uint64(5), current)

	// A put that keeps the lease does not undo a heartbeat that lands
	// between its read and its write.
	first = true
	renewed := t0.Add(time.Minute)
	db.before = func(op string) error {
		if op == "PutItem" && first {
			first = false
			db.items[agentID][attrLeaseRenewedAt] = numI(renewed.UnixNano())
		}
		return nil
	}
	rec.Generation = 6
	got, err := st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: t0.Add(2 * time.Minute)})
	require.NoError(t, err)
	assert.Equal(t, reg.Lease.Epoch, got.Lease.Epoch)
	assert.True(t, got.Lease.RenewedAt.Equal(renewed), "got %v", got.Lease.RenewedAt)

	// A registration deleted between the read and the write is created
	// again.
	first = true
	db.before = func(op string) error {
		if op == "PutItem" && first {
			first = false
			delete(db.items, agentID)
		}
		return nil
	}
	rec.Generation = 7
	got, err = st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: t0.Add(3 * time.Minute), MintLease: true})
	require.NoError(t, err)
	assert.True(t, got.RegisteredAt.Equal(t0.Add(3*time.Minute)), "a new registration")
	epoch := got.Lease.Epoch

	// An attribute heartbeat that races with another writer of the body
	// starts over from the new body.
	updates := 0
	db.before = func(op string) error {
		if op == "UpdateItem" {
			updates++
			if updates == 1 {
				db.items[agentID][attrBody] = bin([]byte{0x42, 0x00})
			}
		}
		return nil
	}
	got, err = st.RenewPoolAgentLease(ctx, key, epoch, t0.Add(4*time.Minute), store.PoolAgentHeartbeatPatch{Attributes: map[string]string{"a": "b"}})
	require.NoError(t, err)
	assert.Equal(t, 2, updates)
	assert.Equal(t, map[string]string{"a": "b"}, got.Attributes)

	// The same attributes renew without a body write.
	db.before = nil
	got, err = st.RenewPoolAgentLease(ctx, key, epoch, t0.Add(5*time.Minute), store.PoolAgentHeartbeatPatch{Attributes: map[string]string{"a": "b"}})
	require.NoError(t, err)
	assert.True(t, got.UpdatedAt.Equal(t0.Add(4*time.Minute)))

	// A registration deleted between the read and the write is not found.
	db.before = func(op string) error {
		if op == "UpdateItem" {
			delete(db.items, agentID)
		}
		return nil
	}
	_, err = st.RenewPoolAgentLease(ctx, key, epoch, t0, store.PoolAgentHeartbeatPatch{Attributes: map[string]string{"c": "d"}})
	assert.ErrorIs(t, err, store.ErrNotFound)

	// Another writer that changes the registration before every write
	// exhausts the attempts.
	db.before = nil
	_, err = st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	gen := uint64(100)
	db.before = func(op string) error {
		if op == "PutItem" || op == "UpdateItem" {
			gen++
			db.items[agentID][attrGen] = numU(gen)
			db.items[agentID][attrLeaseEpoch] = numU(gen)
			db.items[agentID][attrBody] = bin([]byte(fmt.Sprintf("\x42\x01%c", 'a'+gen%26)))
		}
		return nil
	}
	rec.Generation = 1 << 40
	_, err = st.PutPoolAgent(ctx, rec, store.PutPoolAgentOptions{Now: t0, MintLease: true})
	assert.ErrorContains(t, err, "too many concurrent writes")
}

func TestHeartbeatPatchWithoutChange(t *testing.T) {
	st := newTestStore(t, newFakeDB())
	ctx := t.Context()
	reg, err := st.PutPool(ctx, store.PoolRecord{Key: pk("c1", "p"), Name: "n", Generation: 1, Spec: store.PoolSpec{WorkerCount: 4}, Lease: store.Lease{TTL: time.Minute}}, store.PutPoolOptions{Now: t0, MintLease: true})
	require.NoError(t, err)
	w := int32(4)
	got, err := st.RenewPoolLease(ctx, pk("c1", "p"), reg.Lease.Epoch, t0.Add(time.Minute), store.PoolHeartbeatPatch{WorkerCount: &w})
	require.NoError(t, err)
	assert.True(t, got.UpdatedAt.Equal(t0), "a patch that changes nothing keeps UpdatedAt")
	assert.True(t, got.Lease.RenewedAt.Equal(t0.Add(time.Minute)))
	_, err = st.RenewPoolLease(ctx, pk("c1", "nope"), reg.Lease.Epoch, t0, store.PoolHeartbeatPatch{WorkerCount: &w})
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestListClusterSetsIgnoresOtherKeys(t *testing.T) {
	db := newFakeDB()
	st := newTestStore(t, db)
	ctx := t.Context()
	_, err := st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
	require.NoError(t, err)
	extra := key{pk: clusterSetIndexPK, sk: "CS#a#b"}.attributes()
	db.items[itemID(extra)] = extra
	names, err := st.ListClusterSets(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"cs"}, names)
}

// TestFakeRejectsUnusedPlaceholders keeps the fake honest about the
// DynamoDB rule the store must follow.
func TestFakeRejectsUnusedPlaceholders(t *testing.T) {
	db := newFakeDB()
	k := clusterKey(ck("c1")).attributes()
	_, err := db.PutItem(t.Context(), &ddb.PutItemInput{
		TableName:                aws.String("gds"),
		Item:                     k,
		ConditionExpression:      aws.String("attribute_not_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{"#pk": "pk", "#gen": "gen"},
	})
	assert.ErrorContains(t, err, "unused in expressions")
	_, err = db.UpdateItem(t.Context(), &ddb.UpdateItemInput{
		TableName:                 aws.String("gds"),
		Key:                       k,
		UpdateExpression:          aws.String("SET #gen = :gen"),
		ExpressionAttributeNames:  map[string]string{"#gen": "gen"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":gen": numU(1), ":x": numU(2)},
	})
	assert.ErrorContains(t, err, "unused in expressions")
}

func TestConcurrentStoresOnOneTable(t *testing.T) {
	db := newFakeDB()
	a, b := newTestStore(t, db), newTestStore(t, db)
	ctx := t.Context()
	var wg sync.WaitGroup
	for i := range 20 {
		st := a
		if i%2 == 1 {
			st = b
		}
		wg.Go(func() {
			_, err := st.PutCluster(ctx, store.ClusterRecord{Key: ck("c1")}, t0)
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	got, err := a.GetCluster(ctx, ck("c1"))
	require.NoError(t, err)
	assert.Equal(t, uint64(20), got.Generation)
	assert.Equal(t, uint64(t0.UnixMilli())+19, got.Lease.Epoch)
}
