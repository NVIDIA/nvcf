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

use aws_sdk_s3::config::{AsyncSleep, ConfigBag};
use aws_sdk_s3::primitives::DateTime;
use aws_smithy_async::future::timeout::Timeout;
use aws_smithy_runtime::expiring_cache::ExpiringCache;
use aws_smithy_runtime_api::box_error::BoxError;
use aws_smithy_runtime_api::client::identity::{
    Identity, IdentityCachePartition, IdentityFuture, ResolveCachedIdentity, ResolveIdentity,
};
use aws_smithy_runtime_api::client::runtime_components::RuntimeComponents;
use std::collections::HashMap;
use std::fmt;
use std::sync::RwLock;
use std::time::Duration;
use tracing::Instrument;

/// copied from aws-smithy-runtime:1.7.3:src/client/identity/cache/lazy.rs with the jitter function hardcoded to 0.0
#[derive(Debug)]
pub struct NonJitterTTLIdentityCache {
    partitions: CachePartitions,
    load_timeout: Duration,
    buffer_time: Duration,
    buffer_time_jitter_fraction: fn() -> f64,
    default_expiration: Duration,
}

impl NonJitterTTLIdentityCache {
    pub fn new(buffer_time: Duration) -> Self {
        Self {
            partitions: CachePartitions::new(buffer_time),
            load_timeout: Duration::from_secs(5),
            buffer_time,
            buffer_time_jitter_fraction: || 0.0,
            default_expiration: Duration::from_secs(15 * 60),
        }
    }
}

impl ResolveCachedIdentity for NonJitterTTLIdentityCache {
    fn resolve_cached_identity<'a>(
        &'a self,
        resolver: aws_smithy_runtime_api::client::identity::SharedIdentityResolver,
        runtime_components: &'a RuntimeComponents,
        config_bag: &'a ConfigBag,
    ) -> IdentityFuture<'a> {
        let (time_source, sleep_impl) = (
            runtime_components.time_source().expect("validated"),
            runtime_components.sleep_impl().expect("validated"),
        );

        let now = time_source.now();
        let timeout_future = sleep_impl.sleep(self.load_timeout);
        let load_timeout = self.load_timeout;
        let partition = resolver.cache_partition();
        let cache = self.partitions.partition(partition);
        let default_expiration = self.default_expiration;

        IdentityFuture::new(async move {
            // Attempt to get cached identity, or clear the cache if they're expired
            if let Some(identity) = cache.yield_or_clear_if_expired(now).await {
                tracing::debug!(
                    buffer_time=?self.buffer_time,
                    cached_expiration=?identity.expiration(),
                    now=?now,
                    "loaded identity from cache"
                );
                Ok(identity)
            } else {
                // If we didn't get identity from the cache, then we need to try and load.
                // There may be other threads also loading simultaneously, but this is OK
                // since the futures are not eagerly executed, and the cache will only run one
                // of them.
                let start_time = time_source.now();
                let result = cache
                    .get_or_load(|| {
                        let span = tracing::info_span!("lazy_load_identity");
                        async move {
                            let fut = Timeout::new(
                                resolver.resolve_identity(runtime_components, config_bag),
                                timeout_future,
                            );
                            let identity = match fut.await {
                                Ok(result) => result?,
                                Err(_err) => match resolver.fallback_on_interrupt() {
                                    Some(identity) => identity,
                                    None => {
                                        return Err(BoxError::from(TimedOutError(load_timeout)))
                                    }
                                },
                            };
                            // If the identity don't have an expiration time, then create a default one
                            let expiration =
                                identity.expiration().unwrap_or(now + default_expiration);

                            let jitter = self
                                .buffer_time
                                .mul_f64((self.buffer_time_jitter_fraction)());

                            // Logging for cache miss should be emitted here as opposed to after the call to
                            // `cache.get_or_load` above. In the case of multiple threads concurrently executing
                            // `cache.get_or_load`, logging inside `cache.get_or_load` ensures that it is emitted
                            // only once for the first thread that succeeds in populating a cache value.
                            let printable = DateTime::from(expiration);
                            tracing::debug!(
                                new_expiration=%printable,
                                valid_for=?expiration.duration_since(time_source.now()).unwrap_or_default(),
                                partition=?partition,
                                "identity cache miss occurred; added new identity (took {:?})",
                                time_source.now().duration_since(start_time).unwrap_or_default()
                            );

                            Ok((identity, expiration + jitter))
                        }
                            // Only instrument the the actual load future so that no span
                            // is opened if the cache decides not to execute it.
                            .instrument(span)
                    })
                    .await;
                tracing::debug!("loaded identity");
                result
            }
        })
    }
}

#[derive(Debug)]
struct CachePartitions {
    partitions: RwLock<HashMap<IdentityCachePartition, ExpiringCache<Identity, BoxError>>>,
    buffer_time: Duration,
}

impl CachePartitions {
    fn new(buffer_time: Duration) -> Self {
        Self {
            partitions: RwLock::new(HashMap::new()),
            buffer_time,
        }
    }

    fn partition(&self, key: IdentityCachePartition) -> ExpiringCache<Identity, BoxError> {
        let mut partition = self.partitions.read().unwrap().get(&key).cloned();
        // Add the partition to the cache if it doesn't already exist.
        // Partitions will never be removed.
        if partition.is_none() {
            let mut partitions = self.partitions.write().unwrap();
            // Another thread could have inserted the partition before we acquired the lock,
            // so double check before inserting it.
            partitions
                .entry(key)
                .or_insert_with(|| ExpiringCache::new(self.buffer_time));
            drop(partitions);

            partition = self.partitions.read().unwrap().get(&key).cloned();
        }
        partition.expect("inserted above if not present")
    }
}

#[derive(Debug)]
struct TimedOutError(Duration);

impl std::error::Error for TimedOutError {}

impl fmt::Display for TimedOutError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "identity resolver timed out after {:?}", self.0)
    }
}
