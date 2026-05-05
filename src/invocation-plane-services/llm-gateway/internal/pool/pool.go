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

// Package pool provides types and functionality related to object pooling.
package pool

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/NVIDIA/nvcf/llm-api-gateway/internal/must"
)

var (
	// ErrCorruptPool is returned by Pool.Get when the de-pooled object's type
	// is unconvertible to the pool's intended object type.
	ErrCorruptPool = errors.New("corrupt pool")
	// ErrPoolClosed is returned when a pool is attempted to be used after
	// being closed.
	ErrPoolClosed = errors.New("pool closed")
)

type (
	// A Constructor creates a new object of type T.
	Constructor[T any] func() T
	// A Releaser resets an object of type T for reuse.
	Releaser[T any] func(T)
)

// Pool is a strongly-typed object pool.
type Pool[T any] struct {
	pool    sync.Pool
	release Releaser[T]
}

// New creates a new Pool compatible with objects of type T with the given
// Constructor for T.
func New[T any](ctor Constructor[T]) *Pool[T] {
	return &Pool[T]{
		pool: sync.Pool{
			New: func() any {
				return ctor()
			},
		},
	}
}

// NewWithReleaser creates a new Pool compatible with objects of type T with
// the given Constructor and Releaser for T.
func NewWithReleaser[T any](ctor Constructor[T], release Releaser[T]) *Pool[T] {
	pool := New(ctor)
	pool.release = release
	return pool
}

// Get de-pools or creates an object of type T.
func (p *Pool[T]) Get() T {
	x, ok := p.pool.Get().(T)
	if !ok {
		panic(fmt.Errorf("%v: pool contains non-%T", ErrCorruptPool, x))
	}

	return x
}

// Put places the given object back into the pool. If a Releaser is configured,
// it will be called prior to pooling the object.
func (p *Pool[T]) Put(x T) {
	if p.release != nil {
		p.release(x)
	}

	p.pool.Put(x)
}

// A FixedPool is a fixed-size resource pool. It will only create the number of
// objects configured, and de-pooling objects can block (depending on method).
//
// Note that, unlike [Pool], any [Releaser] will be run as part of calling
// [FixedPool.Close], not as each object is re-pooled with [FixedPool.Put].
type FixedPool[T any] struct {
	ctor     Constructor[T]
	releaser Releaser[T]
	pool     chan T
	created  int
	mu       sync.RWMutex
}

// NewFixedPool creates a new [FixedPool] that uses the given [Constructor] to
// create up to size objects.
func NewFixedPool[T any](ctor Constructor[T], size int) *FixedPool[T] {
	return NewFixedPoolWithReleaser(ctor, func(T) {}, size)
}

// NewFixedPoolWithReleaser creates a new [FixedPool] that uses the given
// [Constructor] to create up to size objects, and the given [Releaser] to
// release them as part of closing.
func NewFixedPoolWithReleaser[T any](
	ctor Constructor[T],
	releaser Releaser[T],
	size int,
) *FixedPool[T] {
	if size <= 0 {
		panic("internal/pool.FixedPool: size must be positive")
	}

	return &FixedPool[T]{
		ctor:     ctor,
		releaser: releaser,
		pool:     make(chan T, size),
		created:  0,
	}
}

// Len returns the current length of the pool.
func (p *FixedPool[T]) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pool)
}

// Cap returns the capacity of the pool.
func (p *FixedPool[T]) Cap() int {
	return cap(p.pool)
}

// Used returns the number of used allocations.
func (p *FixedPool[T]) Used() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.created
}

// Get waits for an object to be available in the pool, then de-pools and
// returns it.
func (p *FixedPool[T]) Get() T {
	return must.Get(p.GetWithContext(context.Background()))
}

// GetWithContext waits up to the lifetime of ctx for an object to be available
// in the pool, then de-pools and returns it.
func (p *FixedPool[T]) GetWithContext(ctx context.Context) (T, error) {
	// IMPORTANT: This method has some nuance. Some important details and
	//            invariants to observe:
	//
	//   - p.created, the "count of objects created by the pool", only ever
	//     increases.
	//   - In the case where we have already created the maximum number of
	//     objects that the pool can hold, the mutex does not need to be held
	//     past the condition because there is no external pool interaction
	//     that would affect branching within the (terminal) `select`.
	//   - In the case where we have not yet created the maximum number of
	//     objects the pool can hold, the mutex spans that check as well as the
	//     `select`s that determine whether to create a new object or wait for
	//     one to be available in the pool.

	p.mu.Lock()
	if p.created >= cap(p.pool) {
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		default:
			select {
			case <-ctx.Done():
				var zero T
				return zero, ctx.Err()
			case x, ok := <-p.pool:
				if !ok {
					var zero T
					return zero, ErrPoolClosed
				}
				return x, nil
			}
		}
	}

	select {
	case <-ctx.Done():
		p.mu.Unlock()
		var zero T
		return zero, ctx.Err()
	default:
		select {
		case x, ok := <-p.pool:
			p.mu.Unlock()
			if !ok {
				var zero T
				return zero, ErrPoolClosed
			}
			return x, nil
		default:
			p.created++
			p.mu.Unlock()
			return p.ctor(), nil
		}
	}
}

// TryGet attempts to de-pool an object immediately, returning the potential
// object and whether de-pooling was successful.
func (p *FixedPool[T]) TryGet() (T, bool) {
	// IMPORTANT: This method has some nuance. Some important details and
	//            invariants to observe:
	//
	//   - p.created, the "count of objects created by the pool", only ever
	//     increases.
	//   - In the case where we have already created the maximum number of
	//     objects that the pool can hold, the mutex does not need to be held
	//     past the condition because there is no external pool interaction
	//     that would affect branching within the (terminal) `select`.
	//   - In the case where we have not yet created the maximum number of
	//     objects the pool can hold, the mutex spans that check as well as the
	//     `select`s that determine whether to create a new object or wait for
	//     one to be available in the pool.

	p.mu.Lock()
	if p.created >= cap(p.pool) {
		p.mu.Unlock()
		select {
		case x, ok := <-p.pool:
			if !ok {
				var zero T
				return zero, false
			}
			return x, true
		default:
			var zero T
			return zero, false
		}
	}

	select {
	case x, ok := <-p.pool:
		p.mu.Unlock()
		if !ok {
			var zero T
			return zero, false
		}
		return x, true
	default:
		p.created++
		p.mu.Unlock()
		return p.ctor(), true
	}
}

// Put puts x back into the pool. If the pool is already at capacity, the
// object will be dropped. No releaser is run on the given object until
// [FixedPool.Close] is called.
//
// Calls to Put which pass a T not constructed by the pool itself are malformed
// and will lead to the extra objects being dropped and potentially garbage
// collected.
func (p *FixedPool[T]) Put(x T) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.pool == nil {
		return
	}

	select {
	case p.pool <- x:
	default:
	}
}

// Close closes the pool, releasing all of its objects.
func (p *FixedPool[T]) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.pool != nil {
		close(p.pool)
		if p.releaser != nil {
			for x := range p.pool {
				p.releaser(x)
			}
		}
		p.pool = nil
	}

	return nil
}
