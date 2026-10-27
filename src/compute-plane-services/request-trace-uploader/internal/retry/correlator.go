// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
)

// Correlator decides the scope of unknown faults from what happens to other
// items. When enough distinct items fail with one reason inside the window,
// the backend is failing. When one item keeps failing while another item
// succeeds inside the window, the item is at fault. Backend scope wins when
// both hold, because a hold loses nothing and a quarantine might.
//
// It remembers only the window, in memory. The item's attempt count comes
// from the caller's journal, so it survives a restart.
type Correlator struct {
	window          time.Duration
	backendItems    int
	segmentFailures int

	mu sync.Mutex
	// failures holds the last failure time per reason and item.
	failures map[backend.Reason]map[string]time.Time
	// successes holds the last success time per item.
	successes map[string]time.Time
}

// NewCorrelator builds a correlator. The counts must be at least 1, as
// Config.Validate checks.
func NewCorrelator(window time.Duration, backendItems, segmentFailures int) *Correlator {
	return &Correlator{
		window:          window,
		backendItems:    backendItems,
		segmentFailures: segmentFailures,
		failures:        map[backend.Reason]map[string]time.Time{},
		successes:       map[string]time.Time{},
	}
}

// Failure records an unknown-scope failure of key at now and returns the scope
// it resolves to. attempts counts the item's failed attempts so far,
// including this one.
func (c *Correlator) Failure(now time.Time, key string, reason backend.Reason, attempts int) backend.Scope {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)

	items, ok := c.failures[reason]
	if !ok {
		items = map[string]time.Time{}
		c.failures[reason] = items
	}
	items[key] = now
	if len(items) >= c.backendItems {
		return backend.ScopeBackend
	}
	if attempts >= c.segmentFailures && c.otherSucceeded(key) {
		return backend.ScopeSegment
	}
	return backend.ScopeUnknown
}

// Success records that key succeeded at now.
func (c *Correlator) Success(now time.Time, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	c.successes[key] = now
}

func (c *Correlator) otherSucceeded(key string) bool {
	for item := range c.successes {
		if item != key {
			return true
		}
	}
	return false
}

func (c *Correlator) prune(now time.Time) {
	cutoff := now.Add(-c.window)
	for reason, items := range c.failures {
		for item, at := range items {
			if at.Before(cutoff) {
				delete(items, item)
			}
		}
		if len(items) == 0 {
			delete(c.failures, reason)
		}
	}
	for item, at := range c.successes {
		if at.Before(cutoff) {
			delete(c.successes, item)
		}
	}
}

func (c *Correlator) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.successes)
	for _, items := range c.failures {
		n += len(items)
	}
	return n
}
