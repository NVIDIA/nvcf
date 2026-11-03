// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! Quota management and rate limiting.
//!
//! Provides:
//! - Storage byte limits and object count limits per bucket
//! - Token-bucket rate limiting for request throttling

use std::collections::HashMap;
use std::time::Instant;

use parking_lot::Mutex;

// ---------------------------------------------------------------------------
// Quota limits
// ---------------------------------------------------------------------------

/// Per-bucket quota configuration.
#[derive(Debug, Clone)]
pub struct BucketQuota {
    /// Maximum total bytes in this bucket (0 = unlimited).
    pub max_bytes: u64,
    /// Maximum number of objects in this bucket (0 = unlimited).
    pub max_objects: u64,
}

impl Default for BucketQuota {
    fn default() -> Self {
        Self {
            max_bytes: 0,
            max_objects: 0,
        }
    }
}

/// Current usage counters for a bucket.
#[derive(Debug, Clone, Default)]
pub struct BucketUsage {
    pub total_bytes: u64,
    pub total_objects: u64,
}

// ---------------------------------------------------------------------------
// QuotaManager
// ---------------------------------------------------------------------------

/// Manages storage quotas and rate limiting.
pub struct QuotaManager {
    quotas: Mutex<HashMap<String, BucketQuota>>,
    usage: Mutex<HashMap<String, BucketUsage>>,
    rate_limiter: Mutex<TokenBucket>,
}

impl QuotaManager {
    /// Create a new QuotaManager with the given global rate limit.
    /// `requests_per_second`: maximum requests/sec (0 = unlimited).
    pub fn new(requests_per_second: u64) -> Self {
        Self {
            quotas: Mutex::new(HashMap::new()),
            usage: Mutex::new(HashMap::new()),
            rate_limiter: Mutex::new(TokenBucket::new(requests_per_second)),
        }
    }

    /// Create a QuotaManager with no limits.
    pub fn unlimited() -> Self {
        Self::new(0)
    }

    /// Set a quota for a bucket.
    pub fn set_quota(&self, bucket: &str, quota: BucketQuota) {
        self.quotas.lock().insert(bucket.to_string(), quota);
    }

    /// Remove a quota for a bucket.
    pub fn remove_quota(&self, bucket: &str) {
        self.quotas.lock().remove(bucket);
    }

    /// Update usage counters for a bucket.
    pub fn update_usage(&self, bucket: &str, bytes: u64, objects: u64) {
        let mut usage = self.usage.lock();
        let entry = usage.entry(bucket.to_string()).or_default();
        entry.total_bytes = bytes;
        entry.total_objects = objects;
    }

    /// Record an object addition (bytes added, +1 object).
    pub fn record_put(&self, bucket: &str, size: u64) {
        let mut usage = self.usage.lock();
        let entry = usage.entry(bucket.to_string()).or_default();
        entry.total_bytes += size;
        entry.total_objects += 1;
    }

    /// Record an object deletion (bytes removed, -1 object).
    pub fn record_delete(&self, bucket: &str, size: u64) {
        let mut usage = self.usage.lock();
        let entry = usage.entry(bucket.to_string()).or_default();
        entry.total_bytes = entry.total_bytes.saturating_sub(size);
        entry.total_objects = entry.total_objects.saturating_sub(1);
    }

    /// Check if a put of `size` bytes would exceed the bucket quota.
    /// Returns Ok(()) if within limits, Err with a message if exceeded.
    pub fn check_quota(&self, bucket: &str, size: u64) -> Result<(), String> {
        let quotas = self.quotas.lock();
        let quota = match quotas.get(bucket) {
            Some(q) => q.clone(),
            None => return Ok(()), // No quota set = unlimited.
        };
        drop(quotas);

        let usage = self.usage.lock();
        let current = usage.get(bucket).cloned().unwrap_or_default();
        drop(usage);

        if quota.max_bytes > 0 && current.total_bytes + size > quota.max_bytes {
            return Err(format!(
                "storage quota exceeded: {} + {} > {} bytes",
                current.total_bytes, size, quota.max_bytes
            ));
        }

        if quota.max_objects > 0 && current.total_objects + 1 > quota.max_objects {
            return Err(format!(
                "object count quota exceeded: {} + 1 > {}",
                current.total_objects, quota.max_objects
            ));
        }

        Ok(())
    }

    /// Check rate limit. Returns Ok(()) if allowed, Err if throttled.
    pub fn check_rate(&self) -> Result<(), String> {
        let mut limiter = self.rate_limiter.lock();
        if limiter.try_acquire() {
            Ok(())
        } else {
            Err("rate limit exceeded".to_string())
        }
    }

    /// Get current usage for a bucket.
    pub fn get_usage(&self, bucket: &str) -> BucketUsage {
        self.usage.lock().get(bucket).cloned().unwrap_or_default()
    }
}

// ---------------------------------------------------------------------------
// Token bucket
// ---------------------------------------------------------------------------

/// Simple token-bucket rate limiter.
struct TokenBucket {
    capacity: u64,
    tokens: f64,
    last_refill: Instant,
    rate: f64, // tokens per second
}

impl TokenBucket {
    fn new(rate: u64) -> Self {
        Self {
            capacity: rate.max(1),
            tokens: rate as f64,
            last_refill: Instant::now(),
            rate: rate as f64,
        }
    }

    fn try_acquire(&mut self) -> bool {
        // Rate of 0 = unlimited.
        if self.rate == 0.0 {
            return true;
        }

        self.refill();

        if self.tokens >= 1.0 {
            self.tokens -= 1.0;
            true
        } else {
            false
        }
    }

    fn refill(&mut self) {
        let now = Instant::now();
        let elapsed = now.duration_since(self.last_refill).as_secs_f64();
        self.tokens = (self.tokens + elapsed * self.rate).min(self.capacity as f64);
        self.last_refill = now;
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_quota_unlimited() {
        let qm = QuotaManager::unlimited();
        assert!(qm.check_quota("any-bucket", 1_000_000_000).is_ok());
        assert!(qm.check_rate().is_ok());
    }

    #[test]
    fn test_quota_bytes_limit() {
        let qm = QuotaManager::unlimited();
        qm.set_quota(
            "b1",
            BucketQuota {
                max_bytes: 1000,
                max_objects: 0,
            },
        );
        qm.update_usage("b1", 900, 5);

        // 900 + 50 = 950 <= 1000: ok
        assert!(qm.check_quota("b1", 50).is_ok());

        // 900 + 200 = 1100 > 1000: exceeded
        assert!(qm.check_quota("b1", 200).is_err());
    }

    #[test]
    fn test_quota_object_count_limit() {
        let qm = QuotaManager::unlimited();
        qm.set_quota(
            "b2",
            BucketQuota {
                max_bytes: 0,
                max_objects: 10,
            },
        );
        qm.update_usage("b2", 500, 10);

        // 10 + 1 > 10: exceeded
        assert!(qm.check_quota("b2", 0).is_err());
    }

    #[test]
    fn test_record_put_delete() {
        let qm = QuotaManager::unlimited();
        qm.record_put("b3", 100);
        qm.record_put("b3", 200);
        let u = qm.get_usage("b3");
        assert_eq!(u.total_bytes, 300);
        assert_eq!(u.total_objects, 2);

        qm.record_delete("b3", 100);
        let u = qm.get_usage("b3");
        assert_eq!(u.total_bytes, 200);
        assert_eq!(u.total_objects, 1);
    }

    #[test]
    fn test_rate_limiter_allows_within_capacity() {
        let qm = QuotaManager::new(100);
        // Should allow at least one request immediately.
        assert!(qm.check_rate().is_ok());
    }

    #[test]
    fn test_remove_quota() {
        let qm = QuotaManager::unlimited();
        qm.set_quota(
            "b4",
            BucketQuota {
                max_bytes: 100,
                max_objects: 0,
            },
        );
        qm.update_usage("b4", 100, 1);
        assert!(qm.check_quota("b4", 1).is_err());

        qm.remove_quota("b4");
        // No quota = unlimited.
        assert!(qm.check_quota("b4", 1).is_ok());
    }
}
