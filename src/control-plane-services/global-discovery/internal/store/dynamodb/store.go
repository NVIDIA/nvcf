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

// Package dynamodb is the store.Store of a multi-region GDS deployment: one
// DynamoDB table, replicated as a Global Table (version 2019.11.21), shared
// by every gds-server replica in every region.
//
// The table has a partition key pk and a sort key sk (both strings). A
// clusterset's metadata (clusters, exports, pools and pool name
// reservations) lives in partition CS#<clusterset>; the endpoint sets of
// each cluster live in their own partition EP#<clusterset>#<cluster>, so
// the heartbeat-heavy metadata partition stays small and cluster deletion
// and loading need prefix queries only. keys.go lists every item kind.
//
// Record bodies are serialized nvidia.gds.v1 messages (codec.go). The
// attributes that conditions and updates need are top level: gen,
// lease_epoch, lease_renewed_at, owner_id, updated_at and expires_at.
//
// Writes are conditional, so that every replica can serve every write:
//
//   - PutCluster: attribute_not_exists(pk) OR (lease_epoch < :new AND
//     gen = :seen), retried with the item returned on a failed condition.
//   - Heartbeats: lease_epoch = :epoch; the item returned on a failed
//     condition tells an unknown record (ErrNotFound) from another epoch
//     (ErrLeaseEpochMismatch).
//   - PutExport and PutEndpointSet: attribute_not_exists(pk) OR
//     gen < :gen; the item returned on a failed condition fills
//     ErrStaleGeneration.Current.
//   - PutPool and DeletePool: one TransactWriteItems over the pool and its
//     pool name reservation, guarded by the generation and body the store
//     read. A stale reservation, whose pool no longer exists (for example
//     after a TTL deletion) or now carries another name, is taken over.
//   - DeleteCluster: prefix queries, then BatchWriteItem deletes in chunks
//     of 25 with retries of unprocessed items. It is idempotent.
//
// LoadClusterSet is one paginated, eventually consistent query of the
// clusterset partition plus one per cluster endpoint partition, run in
// parallel. expires_at (Unix seconds) on cluster and pool items is a
// garbage-collection safety net for abandoned records; leases are never
// expired by the store.
package dynamodb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/NVIDIA/nvcf/src/control-plane-services/global-discovery/internal/store"
)

// API is the subset of the DynamoDB client the store uses. *dynamodb.Client
// implements it.
type API interface {
	GetItem(ctx context.Context, in *ddb.GetItemInput, optFns ...func(*ddb.Options)) (*ddb.GetItemOutput, error)
	PutItem(ctx context.Context, in *ddb.PutItemInput, optFns ...func(*ddb.Options)) (*ddb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *ddb.UpdateItemInput, optFns ...func(*ddb.Options)) (*ddb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *ddb.DeleteItemInput, optFns ...func(*ddb.Options)) (*ddb.DeleteItemOutput, error)
	Query(ctx context.Context, in *ddb.QueryInput, optFns ...func(*ddb.Options)) (*ddb.QueryOutput, error)
	BatchWriteItem(ctx context.Context, in *ddb.BatchWriteItemInput, optFns ...func(*ddb.Options)) (*ddb.BatchWriteItemOutput, error)
	TransactWriteItems(ctx context.Context, in *ddb.TransactWriteItemsInput, optFns ...func(*ddb.Options)) (*ddb.TransactWriteItemsOutput, error)
}

var _ API = (*ddb.Client)(nil)

const (
	// DefaultQueryConcurrency bounds the parallel endpoint partition
	// queries of one LoadClusterSet.
	DefaultQueryConcurrency = 8
	// DefaultExpiryAfter is how long after its last write a cluster or pool
	// item expires through DynamoDB TTL.
	DefaultExpiryAfter = 7 * 24 * time.Hour

	// maxAttempts bounds the optimistic retries of one write.
	maxAttempts = 32
	// maxBatchAttempts bounds the BatchWriteItem calls per chunk.
	maxBatchAttempts = 8
	// batchSize is the BatchWriteItem limit.
	batchSize = 25
	// maxItemBytes is the DynamoDB item size limit.
	maxItemBytes = 400 << 10
	// markCacheTTL is how long a replica trusts that an endpoint partition
	// marker it wrote still exists. DeleteCluster on another replica
	// removes markers.
	markCacheTTL = 5 * time.Minute
)

// Backoff between retries of unprocessed batch items and transaction
// conflicts. Variables so that tests can shorten them.
var (
	baseBackoff = 25 * time.Millisecond
	maxBackoff  = time.Second
)

// Options configures New.
type Options struct {
	// Client is the DynamoDB client. Required.
	Client API
	// TableName is the table. Required.
	TableName string
	// QueryConcurrency bounds the parallel endpoint partition queries of
	// LoadClusterSet. Zero means DefaultQueryConcurrency.
	QueryConcurrency int
	// Clock returns the current time for the store's own bookkeeping
	// (marker caching, expiry of writes without a server time). Record
	// timestamps always come from the caller. Nil means time.Now.
	Clock func() time.Time
	// ExpiryAfter sets expires_at on cluster and pool items to the write
	// time plus this duration. Zero means DefaultExpiryAfter; a negative
	// value disables expiry.
	ExpiryAfter time.Duration
}

// Store is a store.Store backed by one DynamoDB table. It is safe for
// concurrent use, and any number of Stores in any number of processes and
// regions may share the table.
type Store struct {
	client      API
	table       string
	concurrency int
	clock       func() time.Time
	expiryAfter time.Duration
	closed      atomic.Bool

	// clusterSets caches the clustersets whose index item this Store has
	// written. Index items are never deleted by the store.
	clusterSets sync.Map

	markMu sync.Mutex
	// marks maps an endpoint partition to the time its cached marker
	// expires.
	marks map[store.ClusterKey]time.Time
}

var _ store.Store = (*Store)(nil)

// New returns a Store. It does not contact DynamoDB; use Ping.
func New(opts Options) (*Store, error) {
	if opts.Client == nil {
		return nil, errors.New("dynamodb: Options.Client is required")
	}
	if opts.TableName == "" {
		return nil, errors.New("dynamodb: Options.TableName is required")
	}
	s := &Store{
		client:      opts.Client,
		table:       opts.TableName,
		concurrency: cmp.Or(opts.QueryConcurrency, DefaultQueryConcurrency),
		clock:       opts.Clock,
		expiryAfter: cmp.Or(opts.ExpiryAfter, DefaultExpiryAfter),
		marks:       map[store.ClusterKey]time.Time{},
	}
	if s.concurrency < 1 {
		s.concurrency = 1
	}
	if s.clock == nil {
		s.clock = time.Now
	}
	return s, nil
}

// begin fails when ctx is done or the store is closed.
func (s *Store) begin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() {
		return fmt.Errorf("dynamodb: %w: store is closed", store.ErrUnavailable)
	}
	return nil
}

// expiresAt returns expires_at for a write at now, 0 when expiry is
// disabled.
func (s *Store) expiresAt(now time.Time) int64 {
	if s.expiryAfter < 0 {
		return 0
	}
	if now.IsZero() {
		now = s.clock()
	}
	return now.Add(s.expiryAfter).Unix()
}

func (s *Store) getItem(ctx context.Context, op string, k key) (item, bool, error) {
	out, err := s.client.GetItem(ctx, &ddb.GetItemInput{
		TableName:      &s.table,
		Key:            k.attributes(),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return item{}, false, mapError(op, err)
	}
	return parseOptionalItem(out.Item)
}

// parseOptionalItem parses an item that may be absent.
func parseOptionalItem(av map[string]ddbtypes.AttributeValue) (item, bool, error) {
	if len(av) == 0 {
		return item{}, false, nil
	}
	it, err := parseItem(av)
	if err != nil {
		return item{}, false, err
	}
	return it, true, nil
}

func (s *Store) attributes(it item) (map[string]ddbtypes.AttributeValue, error) {
	av, err := it.attributes()
	if err != nil {
		return nil, err
	}
	if n := itemSize(av); n > maxItemBytes {
		return nil, fmt.Errorf("dynamodb: item %s/%s is about %d bytes, above the %d byte limit: %w", it.PK, it.SK, n, maxItemBytes, store.ErrTooLarge)
	}
	return av, nil
}

// ensureClusterSet writes the clusterset index item once per Store and
// clusterset, before the clusterset's first record.
func (s *Store) ensureClusterSet(ctx context.Context, clusterSet string) error {
	if _, ok := s.clusterSets.Load(clusterSet); ok {
		return nil
	}
	k := clusterSetIndexKey(clusterSet)
	if _, err := s.client.PutItem(ctx, &ddb.PutItemInput{TableName: &s.table, Item: k.attributes()}); err != nil {
		return mapError("ensure clusterset index", err)
	}
	s.clusterSets.Store(clusterSet, struct{}{})
	return nil
}

// ensureEndpointMark writes the marker that makes the cluster's endpoint
// partition known to LoadClusterSet, for endpoint sets whose cluster has no
// other record.
func (s *Store) ensureEndpointMark(ctx context.Context, k store.ClusterKey) error {
	now := s.clock()
	s.markMu.Lock()
	until, ok := s.marks[k]
	s.markMu.Unlock()
	if ok && now.Before(until) {
		return nil
	}
	mk := endpointMarkKey(k.ClusterSet, k.Cluster)
	if _, err := s.client.PutItem(ctx, &ddb.PutItemInput{TableName: &s.table, Item: mk.attributes()}); err != nil {
		return mapError("ensure endpoint partition marker", err)
	}
	s.markMu.Lock()
	s.marks[k] = now.Add(markCacheTTL)
	s.markMu.Unlock()
	return nil
}

func (s *Store) forgetEndpointMark(k store.ClusterKey) {
	s.markMu.Lock()
	delete(s.marks, k)
	s.markMu.Unlock()
}

// Clusters.

// PutCluster implements store.ClusterStore.
func (s *Store) PutCluster(ctx context.Context, rec store.ClusterRecord, now time.Time) (store.ClusterRecord, error) {
	const op = "PutCluster"
	if err := s.begin(ctx); err != nil {
		return store.ClusterRecord{}, err
	}
	if err := s.ensureClusterSet(ctx, rec.Key.ClusterSet); err != nil {
		return store.ClusterRecord{}, err
	}
	stored, found, err := s.getItem(ctx, op, clusterKey(rec.Key))
	if err != nil {
		return store.ClusterRecord{}, err
	}
	for range maxAttempts {
		var prev store.ClusterRecord
		if found {
			if prev, err = decodeCluster(stored); err != nil {
				return store.ClusterRecord{}, err
			}
		}
		out := rec.Clone()
		out.Lease = store.Lease{
			Epoch:     store.NextEpoch(prev.Lease.Epoch, now),
			TTL:       rec.Lease.TTL,
			RenewedAt: now,
		}
		out.Generation = prev.Generation + 1
		out.RegisteredAt = now
		if found {
			out.RegisteredAt = prev.RegisteredAt
		}
		out.UpdatedAt = now

		it, err := encodeCluster(out, s.expiresAt(now))
		if err != nil {
			return store.ClusterRecord{}, err
		}
		av, err := s.attributes(it)
		if err != nil {
			return store.ClusterRecord{}, err
		}
		// The designed condition, lease_epoch < :new, plus the generation
		// that was read: RegisteredAt and the generation are derived from
		// the stored item, so a concurrent registration must make this one
		// start over.
		var e expr
		cond := fmt.Sprintf("%s OR (%s < %s AND %s)",
			notExists(&e, attrPK), e.name(attrLeaseEpoch), e.value("new_epoch", numU(out.Lease.Epoch)), genIs(&e, prev.Generation))
		_, err = s.client.PutItem(ctx, &ddb.PutItemInput{
			TableName:                           &s.table,
			Item:                                av,
			ConditionExpression:                 &cond,
			ExpressionAttributeNames:            e.names,
			ExpressionAttributeValues:           e.values,
			ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
		})
		if err == nil {
			return out, nil
		}
		old, ok := conditionFailed(err)
		if !ok {
			return store.ClusterRecord{}, mapError(op, err)
		}
		if stored, found, err = parseOptionalItem(old); err != nil {
			return store.ClusterRecord{}, err
		}
	}
	return store.ClusterRecord{}, fmt.Errorf("dynamodb: %s: %w", op, errContention)
}

// RenewClusterLease implements store.ClusterStore.
func (s *Store) RenewClusterLease(ctx context.Context, k store.ClusterKey, epoch uint64, now time.Time) (store.ClusterRecord, error) {
	const op = "RenewClusterLease"
	if err := s.begin(ctx); err != nil {
		return store.ClusterRecord{}, err
	}
	var e expr
	var u update
	u.setTime(&e, attrLeaseRenewedAt, now)
	u.setExpiry(&e, s.expiresAt(now))
	it, err := s.renew(ctx, op, clusterKey(k), &e, &u, epoch)
	if err != nil {
		return store.ClusterRecord{}, err
	}
	return decodeCluster(it)
}

// renew applies a heartbeat update under the condition lease_epoch =
// :epoch and returns the updated item. A failed condition with no item is
// ErrNotFound, any other ErrLeaseEpochMismatch.
func (s *Store) renew(ctx context.Context, op string, k key, e *expr, u *update, epoch uint64) (item, error) {
	cond := e.name(attrLeaseEpoch) + " = " + e.value("epoch", numU(epoch))
	expression := u.String()
	out, err := s.client.UpdateItem(ctx, &ddb.UpdateItemInput{
		TableName:                           &s.table,
		Key:                                 k.attributes(),
		UpdateExpression:                    &expression,
		ConditionExpression:                 &cond,
		ExpressionAttributeNames:            e.names,
		ExpressionAttributeValues:           e.values,
		ReturnValues:                        ddbtypes.ReturnValueAllNew,
		ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
	})
	if err != nil {
		old, ok := conditionFailed(err)
		switch {
		case !ok:
			return item{}, mapError(op, err)
		case len(old) == 0:
			return item{}, store.ErrNotFound
		default:
			return item{}, store.ErrLeaseEpochMismatch
		}
	}
	return parseItem(out.Attributes)
}

// GetCluster implements store.ClusterStore.
func (s *Store) GetCluster(ctx context.Context, k store.ClusterKey) (store.ClusterRecord, error) {
	if err := s.begin(ctx); err != nil {
		return store.ClusterRecord{}, err
	}
	it, found, err := s.getItem(ctx, "GetCluster", clusterKey(k))
	if err != nil {
		return store.ClusterRecord{}, err
	}
	if !found {
		return store.ClusterRecord{}, store.ErrNotFound
	}
	return decodeCluster(it)
}

// DeleteCluster implements store.ClusterStore. It deletes the registration,
// then every export, pool, pool name reservation and endpoint set of the
// cluster found by prefix queries. Records the cluster writes while the
// deletion runs may survive it; running it again removes them.
func (s *Store) DeleteCluster(ctx context.Context, k store.ClusterKey) error {
	const op = "DeleteCluster"
	if err := s.begin(ctx); err != nil {
		return err
	}
	keys := []key{clusterKey(k), endpointMarkKey(k.ClusterSet, k.Cluster)}
	collect := func(it item) error {
		keys = append(keys, key{pk: it.PK, sk: it.SK})
		return nil
	}
	pk := clusterSetPK(k.ClusterSet)
	for _, prefix := range []string{exportSKPrefix(k.Cluster), poolSKPrefix(k.Cluster), poolNameSKPrefix(k.Cluster)} {
		if err := s.query(ctx, op, pk, prefix, queryOptions{consistent: true, keysOnly: true}, collect); err != nil {
			return err
		}
	}
	if err := s.query(ctx, op, endpointPK(k.ClusterSet, k.Cluster), "", queryOptions{consistent: true, keysOnly: true}, collect); err != nil {
		return err
	}
	if err := s.deleteKeys(ctx, op, keys); err != nil {
		return err
	}
	s.forgetEndpointMark(k)
	return nil
}

// Exports and endpoint sets.

// PutExport implements store.ExportStore.
func (s *Store) PutExport(ctx context.Context, rec store.ExportRecord) error {
	const op = "PutExport"
	if err := s.begin(ctx); err != nil {
		return err
	}
	if err := s.ensureClusterSet(ctx, rec.Key.ClusterSet); err != nil {
		return err
	}
	it, err := encodeExport(rec)
	if err != nil {
		return err
	}
	return s.putIfNewer(ctx, op, it)
}

// putIfNewer writes it when no item exists or the stored generation is
// lower, and returns ErrStaleGeneration with the stored generation
// otherwise.
func (s *Store) putIfNewer(ctx context.Context, op string, it item) error {
	av, err := s.attributes(it)
	if err != nil {
		return err
	}
	var e expr
	cond := fmt.Sprintf("%s OR %s < %s", notExists(&e, attrPK), e.name(attrGen), e.value("gen", numU(it.gen())))
	_, err = s.client.PutItem(ctx, &ddb.PutItemInput{
		TableName:                           &s.table,
		Item:                                av,
		ConditionExpression:                 &cond,
		ExpressionAttributeNames:            e.names,
		ExpressionAttributeValues:           e.values,
		ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
	})
	if err == nil {
		return nil
	}
	old, ok := conditionFailed(err)
	if !ok {
		return mapError(op, err)
	}
	stored, _, err := parseOptionalItem(old)
	if err != nil {
		return err
	}
	return store.ErrStaleGeneration{Current: stored.gen()}
}

// DeleteExport implements store.ExportStore.
func (s *Store) DeleteExport(ctx context.Context, k store.ExportKey) error {
	if err := s.begin(ctx); err != nil {
		return err
	}
	return s.deleteKeys(ctx, "DeleteExport", []key{exportKey(k), endpointSetKey(k)})
}

// PutEndpointSet implements store.EndpointStore.
func (s *Store) PutEndpointSet(ctx context.Context, rec store.EndpointSetRecord) error {
	const op = "PutEndpointSet"
	if err := s.begin(ctx); err != nil {
		return err
	}
	if len(rec.Endpoints) > store.MaxEndpointsPerSet {
		return fmt.Errorf("dynamodb: %s: %d endpoints, at most %d: %w", op, len(rec.Endpoints), store.MaxEndpointsPerSet, store.ErrTooLarge)
	}
	it, err := encodeEndpointSet(rec)
	if err != nil {
		return err
	}
	if err := s.ensureClusterSet(ctx, rec.Key.ClusterSet); err != nil {
		return err
	}
	if err := s.ensureEndpointMark(ctx, rec.Key.ClusterKey()); err != nil {
		return err
	}
	return s.putIfNewer(ctx, op, it)
}

// DeleteEndpointSet implements store.EndpointStore.
func (s *Store) DeleteEndpointSet(ctx context.Context, k store.ExportKey) error {
	if err := s.begin(ctx); err != nil {
		return err
	}
	_, err := s.client.DeleteItem(ctx, &ddb.DeleteItemInput{TableName: &s.table, Key: endpointSetKey(k).attributes()})
	return mapError("DeleteEndpointSet", err)
}

// Pools.

// PutPool implements store.PoolStore. It reads the pool, derives the lease,
// RegisteredAt and the name to release from it, and writes the pool and
// its name reservation in one transaction. The pool condition requires the
// generation, body and lease (epoch, and the renewal time when the lease is
// kept) that were read, which implies the designed gen < :gen; a lost race
// starts over from the item DynamoDB returns.
func (s *Store) PutPool(ctx context.Context, rec store.PoolRecord, opts store.PutPoolOptions) (store.PoolRecord, error) {
	const op = "PutPool"
	if err := s.begin(ctx); err != nil {
		return store.PoolRecord{}, err
	}
	if err := s.ensureClusterSet(ctx, rec.Key.ClusterSet); err != nil {
		return store.PoolRecord{}, err
	}
	stored, found, err := s.getItem(ctx, op, poolKey(rec.Key))
	if err != nil {
		return store.PoolRecord{}, err
	}
	// takeover is a stale reservation of the name: its holder is gone or
	// carries another name.
	var takeover *staleHolder
	// releaseOld is false once the old name of a renamed pool turned out
	// to belong to another pool.
	releaseOld := true
	for attempt := range maxAttempts {
		var prev store.PoolRecord
		if found {
			if prev, err = decodePool(stored); err != nil {
				return store.PoolRecord{}, err
			}
			if rec.Generation <= prev.Generation {
				return store.PoolRecord{}, store.ErrStaleGeneration{Current: prev.Generation}
			}
		}
		out := rec.Clone()
		out.Spec.Location.Cluster = rec.Key.Cluster
		switch {
		case opts.MintLease:
			out.Lease = store.Lease{Epoch: store.NextEpoch(prev.Lease.Epoch, opts.Now), TTL: rec.Lease.TTL, RenewedAt: opts.Now}
		case found:
			out.Lease = prev.Lease
		default:
			out.Lease = store.Lease{}
		}
		out.RegisteredAt = opts.Now
		if found {
			out.RegisteredAt = prev.RegisteredAt
		}
		out.UpdatedAt = opts.Now

		it, err := encodePool(out, s.expiresAt(opts.Now))
		if err != nil {
			return store.PoolRecord{}, err
		}
		poolAV, err := s.attributes(it)
		if err != nil {
			return store.PoolRecord{}, err
		}

		items := make([]ddbtypes.TransactWriteItem, 0, 4)
		var pe expr
		var poolCond string
		if found {
			poolCond = genIs(&pe, prev.Generation) + " AND " + bodyIs(&pe, stored.Body) + " AND " + epochIs(&pe, stored.LeaseEpoch)
			if !opts.MintLease {
				// The kept lease must not undo a heartbeat that landed
				// after the read.
				poolCond += " AND " + renewedIs(&pe, stored.LeaseRenewedAt)
			}
		} else {
			poolCond = notExists(&pe, attrPK)
		}
		items = append(items, ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{
			TableName:                           &s.table,
			Item:                                poolAV,
			ConditionExpression:                 &poolCond,
			ExpressionAttributeNames:            pe.names,
			ExpressionAttributeValues:           pe.values,
			ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
		}})

		nk := poolNameKey(rec.Key.ClusterSet, rec.Key.Cluster, rec.Name)
		nameAV, err := item{PK: nk.pk, SK: nk.sk, PoolID: rec.Key.PoolID}.attributes()
		if err != nil {
			return store.PoolRecord{}, err
		}
		var ne expr
		var nameCond string
		if takeover == nil {
			nameCond = ownedBy(&ne, rec.Key.PoolID)
		} else {
			nameCond = ne.name(attrPoolID) + " = " + ne.value("stale_pool_id", str(takeover.poolID))
		}
		items = append(items, ddbtypes.TransactWriteItem{Put: &ddbtypes.Put{
			TableName:                           &s.table,
			Item:                                nameAV,
			ConditionExpression:                 &nameCond,
			ExpressionAttributeNames:            ne.names,
			ExpressionAttributeValues:           ne.values,
			ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
		}})

		staleCheck := -1
		if takeover != nil {
			// The holder must still be absent, or unchanged since it was
			// read under another name.
			staleCheck = len(items)
			var ce expr
			var cond string
			if takeover.body == nil {
				cond = notExists(&ce, attrPK)
			} else {
				cond = bodyIs(&ce, takeover.body)
			}
			stale := store.PoolKey{ClusterSet: rec.Key.ClusterSet, Cluster: rec.Key.Cluster, PoolID: takeover.poolID}
			items = append(items, ddbtypes.TransactWriteItem{ConditionCheck: &ddbtypes.ConditionCheck{
				TableName:                 &s.table,
				Key:                       poolKey(stale).attributes(),
				ConditionExpression:       &cond,
				ExpressionAttributeNames:  ce.names,
				ExpressionAttributeValues: ce.values,
			}})
		}
		release := -1
		if found && prev.Name != rec.Name && releaseOld {
			release = len(items)
			var re expr
			cond := ownedBy(&re, rec.Key.PoolID)
			items = append(items, ddbtypes.TransactWriteItem{Delete: &ddbtypes.Delete{
				TableName:                 &s.table,
				Key:                       poolNameKey(rec.Key.ClusterSet, rec.Key.Cluster, prev.Name).attributes(),
				ConditionExpression:       &cond,
				ExpressionAttributeNames:  re.names,
				ExpressionAttributeValues: re.values,
			}})
		}

		_, err = s.client.TransactWriteItems(ctx, &ddb.TransactWriteItemsInput{TransactItems: items})
		if err == nil {
			return out, nil
		}
		reasons, ok := transactionCanceled(err)
		if !ok {
			return store.PoolRecord{}, mapError(op, err)
		}
		switch {
		case reasonCode(reasons, 0) == reasonConditionalCheckFailed:
			// The pool changed since it was read.
			if stored, found, err = parseOptionalItem(reasons[0].Item); err != nil {
				return store.PoolRecord{}, err
			}
			takeover, releaseOld = nil, true
		case reasonCode(reasons, 1) == reasonConditionalCheckFailed:
			if takeover != nil {
				// The stale reservation changed hands; look again.
				takeover = nil
				continue
			}
			holder, _, err := parseOptionalItem(reasons[1].Item)
			if err != nil {
				return store.PoolRecord{}, err
			}
			if takeover, err = s.staleHolder(ctx, op, rec, holder.PoolID); err != nil {
				return store.PoolRecord{}, err
			}
		case staleCheck >= 0 && reasonCode(reasons, staleCheck) == reasonConditionalCheckFailed:
			// The holder changed since it was read; look again.
			takeover = nil
		case release >= 0 && reasonCode(reasons, release) == reasonConditionalCheckFailed:
			releaseOld = false
		default:
			// Transaction conflicts with concurrent writes of the same
			// items: back off and try again.
			if !onlyConflicts(reasons) {
				return store.PoolRecord{}, mapError(op, err)
			}
			if err := sleep(ctx, attempt); err != nil {
				return store.PoolRecord{}, err
			}
		}
	}
	return store.PoolRecord{}, fmt.Errorf("dynamodb: %s: %w", op, errContention)
}

// staleHolder describes a name reservation that may be taken over.
type staleHolder struct {
	poolID string
	// body is the holder's body as read, nil when the holder is gone.
	body []byte
}

// staleHolder returns the reservation of rec.Name held by poolID when it
// is stale: the holding pool is gone (for example deleted by TTL) or now
// carries another name. A reservation held by a pool that carries the
// name is ErrPoolNameTaken.
func (s *Store) staleHolder(ctx context.Context, op string, rec store.PoolRecord, poolID string) (*staleHolder, error) {
	if poolID == "" {
		return nil, store.ErrPoolNameTaken
	}
	holderKey := store.PoolKey{ClusterSet: rec.Key.ClusterSet, Cluster: rec.Key.Cluster, PoolID: poolID}
	it, exists, err := s.getItem(ctx, op, poolKey(holderKey))
	if err != nil {
		return nil, err
	}
	if !exists {
		return &staleHolder{poolID: poolID}, nil
	}
	holder, err := decodePool(it)
	if err != nil || holder.Name == rec.Name || len(it.Body) == 0 {
		return nil, store.ErrPoolNameTaken
	}
	return &staleHolder{poolID: poolID, body: it.Body}, nil
}

// onlyConflicts reports whether a transaction was canceled by conflicts
// with other transactions only.
func onlyConflicts(reasons []ddbtypes.CancellationReason) bool {
	conflict := false
	for i := range reasons {
		switch reasonCode(reasons, i) {
		case reasonNone, "":
		case "TransactionConflict":
			conflict = true
		default:
			return false
		}
	}
	return conflict
}

// RenewPoolLease implements store.PoolStore. A heartbeat whose patch
// changes nothing updates only lease_renewed_at; otherwise the store reads
// the pool, applies the patch and writes the body under the condition that
// the epoch and the body it read are unchanged.
func (s *Store) RenewPoolLease(ctx context.Context, k store.PoolKey, epoch uint64, now time.Time, patch store.PoolHeartbeatPatch) (store.PoolRecord, error) {
	const op = "RenewPoolLease"
	if err := s.begin(ctx); err != nil {
		return store.PoolRecord{}, err
	}
	pk := poolKey(k)
	renewOnly := func() (store.PoolRecord, error) {
		var e expr
		var u update
		u.setTime(&e, attrLeaseRenewedAt, now)
		u.setExpiry(&e, s.expiresAt(now))
		it, err := s.renew(ctx, op, pk, &e, &u, epoch)
		if err != nil {
			return store.PoolRecord{}, err
		}
		return decodePool(it)
	}
	if patch.WorkerCount == nil && patch.ReadyWorkerCount == nil && patch.DesiredState == "" {
		return renewOnly()
	}

	stored, found, err := s.getItem(ctx, op, pk)
	if err != nil {
		return store.PoolRecord{}, err
	}
	for range maxAttempts {
		if !found {
			return store.PoolRecord{}, store.ErrNotFound
		}
		cur, err := decodePool(stored)
		if err != nil {
			return store.PoolRecord{}, err
		}
		if cur.Lease.Epoch == 0 || cur.Lease.Epoch != epoch {
			return store.PoolRecord{}, store.ErrLeaseEpochMismatch
		}
		if !patch.Apply(&cur.Spec) {
			return renewOnly()
		}
		cur.Lease.RenewedAt = now
		cur.UpdatedAt = now
		body, err := encodePoolBody(cur)
		if err != nil {
			return store.PoolRecord{}, err
		}
		var e expr
		var u update
		u.setValue(&e, attrBody, bin(body))
		u.setTime(&e, attrLeaseRenewedAt, now)
		u.setTime(&e, attrUpdatedAt, now)
		u.setExpiry(&e, s.expiresAt(now))
		cond := epochIs(&e, epoch) + " AND " + bodyIs(&e, stored.Body)
		expression := u.String()
		out, err := s.client.UpdateItem(ctx, &ddb.UpdateItemInput{
			TableName:                           &s.table,
			Key:                                 pk.attributes(),
			UpdateExpression:                    &expression,
			ConditionExpression:                 &cond,
			ExpressionAttributeNames:            e.names,
			ExpressionAttributeValues:           e.values,
			ReturnValues:                        ddbtypes.ReturnValueAllNew,
			ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
		})
		if err == nil {
			it, err := parseItem(out.Attributes)
			if err != nil {
				return store.PoolRecord{}, err
			}
			return decodePool(it)
		}
		old, ok := conditionFailed(err)
		if !ok {
			return store.PoolRecord{}, mapError(op, err)
		}
		if stored, found, err = parseOptionalItem(old); err != nil {
			return store.PoolRecord{}, err
		}
	}
	return store.PoolRecord{}, fmt.Errorf("dynamodb: %s: %w", op, errContention)
}

// DeletePool implements store.PoolStore. It deletes the pool and its name
// reservation in one transaction.
func (s *Store) DeletePool(ctx context.Context, k store.PoolKey) error {
	const op = "DeletePool"
	if err := s.begin(ctx); err != nil {
		return err
	}
	pk := poolKey(k)
	stored, found, err := s.getItem(ctx, op, pk)
	if err != nil {
		return err
	}
	releaseName := true
	for attempt := range maxAttempts {
		if !found {
			return nil
		}
		cur, err := decodePool(stored)
		if err != nil {
			return err
		}
		var pe expr
		poolCond := genIs(&pe, cur.Generation) + " AND " + bodyIs(&pe, stored.Body)
		items := []ddbtypes.TransactWriteItem{{Delete: &ddbtypes.Delete{
			TableName:                           &s.table,
			Key:                                 pk.attributes(),
			ConditionExpression:                 &poolCond,
			ExpressionAttributeNames:            pe.names,
			ExpressionAttributeValues:           pe.values,
			ReturnValuesOnConditionCheckFailure: ddbtypes.ReturnValuesOnConditionCheckFailureAllOld,
		}}}
		if releaseName {
			var ne expr
			nameCond := ownedBy(&ne, k.PoolID)
			items = append(items, ddbtypes.TransactWriteItem{Delete: &ddbtypes.Delete{
				TableName:                 &s.table,
				Key:                       poolNameKey(k.ClusterSet, k.Cluster, cur.Name).attributes(),
				ConditionExpression:       &nameCond,
				ExpressionAttributeNames:  ne.names,
				ExpressionAttributeValues: ne.values,
			}})
		}
		_, err = s.client.TransactWriteItems(ctx, &ddb.TransactWriteItemsInput{TransactItems: items})
		if err == nil {
			return nil
		}
		reasons, ok := transactionCanceled(err)
		if !ok {
			return mapError(op, err)
		}
		switch {
		case reasonCode(reasons, 0) == reasonConditionalCheckFailed:
			if stored, found, err = parseOptionalItem(reasons[0].Item); err != nil {
				return err
			}
			releaseName = true
		case reasonCode(reasons, 1) == reasonConditionalCheckFailed:
			// The name belongs to another pool; delete only this pool.
			releaseName = false
		default:
			if !onlyConflicts(reasons) {
				return mapError(op, err)
			}
			if err := sleep(ctx, attempt); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("dynamodb: %s: %w", op, errContention)
}

// Snapshots.

// ListClusterSets implements store.Store.
func (s *Store) ListClusterSets(ctx context.Context) ([]string, error) {
	const op = "ListClusterSets"
	if err := s.begin(ctx); err != nil {
		return nil, err
	}
	names := []string{}
	err := s.query(ctx, op, clusterSetIndexPK, clusterSetPrefix, queryOptions{keysOnly: true}, func(it item) error {
		if name, ok := parseClusterSetIndexSK(it.SK); ok {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// LoadClusterSet implements store.Store. It queries the clusterset
// partition, then the endpoint partition of every cluster that has a
// registration, an export, a pool or an endpoint partition marker, at most
// Options.QueryConcurrency at a time. Reads are eventually consistent.
func (s *Store) LoadClusterSet(ctx context.Context, clusterSet string) (store.ClusterSetSnapshot, error) {
	const op = "LoadClusterSet"
	if err := s.begin(ctx); err != nil {
		return store.ClusterSetSnapshot{}, err
	}
	snap := store.ClusterSetSnapshot{ClusterSet: clusterSet}
	clusters := map[string]struct{}{}
	err := s.query(ctx, op, clusterSetPK(clusterSet), "", queryOptions{}, func(it item) error {
		switch {
		case strings.HasPrefix(it.SK, clusterPrefix):
			rec, err := decodeCluster(it)
			if err != nil {
				return err
			}
			snap.Clusters = append(snap.Clusters, rec)
			clusters[rec.Key.Cluster] = struct{}{}
		case strings.HasPrefix(it.SK, exportPrefix):
			rec, err := decodeExport(it)
			if err != nil {
				return err
			}
			snap.Exports = append(snap.Exports, rec)
			clusters[rec.Key.Cluster] = struct{}{}
		case strings.HasPrefix(it.SK, poolPrefix):
			rec, err := decodePool(it)
			if err != nil {
				return err
			}
			snap.Pools = append(snap.Pools, rec)
			clusters[rec.Key.Cluster] = struct{}{}
		case strings.HasPrefix(it.SK, endpointMarkPfx):
			if cluster, ok := parseEndpointMarkSK(it.SK); ok {
				clusters[cluster] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return store.ClusterSetSnapshot{}, err
	}

	sets, err := s.loadEndpointSets(ctx, clusterSet, clusters)
	if err != nil {
		return store.ClusterSetSnapshot{}, err
	}
	snap.EndpointSets = sets
	snap.Sort()
	return snap, nil
}

// loadEndpointSets queries the endpoint partition of each cluster with at
// most s.concurrency queries in flight.
func (s *Store) loadEndpointSets(ctx context.Context, clusterSet string, clusters map[string]struct{}) ([]store.EndpointSetRecord, error) {
	names := make([]string, 0, len(clusters))
	for c := range clusters {
		names = append(names, c)
	}
	slices.Sort(names)

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	results := make([][]store.EndpointSetRecord, len(names))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(s.concurrency, len(names)) {
		wg.Go(func() {
			for i := range next {
				err := s.query(ctx, "LoadClusterSet", endpointPK(clusterSet, names[i]), "", queryOptions{}, func(it item) error {
					rec, err := decodeEndpointSet(it)
					if err != nil {
						return err
					}
					results[i] = append(results[i], rec)
					return nil
				})
				if err != nil {
					cancel(err)
				}
			}
		})
	}
feed:
	for i := range names {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	var out []store.EndpointSetRecord
	for _, r := range results {
		out = append(out, r...)
	}
	return out, nil
}

// Ping implements store.Store. It reads a key that never exists, which
// needs only dynamodb:GetItem and fails when the table is missing or the
// credentials are not accepted.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.begin(ctx); err != nil {
		return err
	}
	_, err := s.client.GetItem(ctx, &ddb.GetItemInput{
		TableName: &s.table,
		Key:       key{pk: clusterSetIndexPK, sk: pingSK}.attributes(),
	})
	return mapError("Ping", err)
}

// Close implements store.Store. Later calls return store.ErrUnavailable.
// The client is owned by the caller and stays usable.
func (s *Store) Close() error {
	s.closed.Store(true)
	return nil
}

// Queries and batches.

type queryOptions struct {
	consistent bool
	keysOnly   bool
}

// query runs a paginated query of partition pk, restricted to sort keys
// beginning with skPrefix when it is not empty, and calls fn for each item.
func (s *Store) query(ctx context.Context, op, pk, skPrefix string, opts queryOptions, fn func(item) error) error {
	var e expr
	cond := e.name(attrPK) + " = " + e.value("pk", str(pk))
	if skPrefix != "" {
		cond += " AND begins_with(" + e.name(attrSK) + ", " + e.value("sk_prefix", str(skPrefix)) + ")"
	}
	in := &ddb.QueryInput{
		TableName:                 &s.table,
		KeyConditionExpression:    &cond,
		ExpressionAttributeNames:  e.names,
		ExpressionAttributeValues: e.values,
		ConsistentRead:            aws.Bool(opts.consistent),
	}
	if opts.keysOnly {
		in.ProjectionExpression = aws.String(e.name(attrPK) + ", " + e.name(attrSK))
	}
	pages := ddb.NewQueryPaginator(s.client, in)
	for pages.HasMorePages() {
		out, err := pages.NextPage(ctx)
		if err != nil {
			return mapError(op, err)
		}
		for _, av := range out.Items {
			it, err := parseItem(av)
			if err != nil {
				return err
			}
			if err := fn(it); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteKeys deletes items with BatchWriteItem in chunks of 25, retrying
// unprocessed items with backoff. Deleting a missing item succeeds.
func (s *Store) deleteKeys(ctx context.Context, op string, keys []key) error {
	for chunk := range slices.Chunk(keys, batchSize) {
		reqs := make([]ddbtypes.WriteRequest, len(chunk))
		for i, k := range chunk {
			reqs[i] = ddbtypes.WriteRequest{DeleteRequest: &ddbtypes.DeleteRequest{Key: k.attributes()}}
		}
		for attempt := 0; len(reqs) > 0; attempt++ {
			if attempt == maxBatchAttempts {
				return fmt.Errorf("dynamodb: %s: %w: %d deletes still unprocessed", op, store.ErrUnavailable, len(reqs))
			}
			if attempt > 0 {
				if err := sleep(ctx, attempt-1); err != nil {
					return err
				}
			}
			out, err := s.client.BatchWriteItem(ctx, &ddb.BatchWriteItemInput{
				RequestItems: map[string][]ddbtypes.WriteRequest{s.table: reqs},
			})
			if err != nil {
				return mapError(op, err)
			}
			reqs = out.UnprocessedItems[s.table]
		}
	}
	return nil
}

// sleep waits an exponential, jittered backoff for the attempt.
func sleep(ctx context.Context, attempt int) error {
	d := min(baseBackoff<<min(attempt, 10), maxBackoff)
	d = d/2 + rand.N(d/2+1)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
