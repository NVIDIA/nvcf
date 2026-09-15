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

pub use retry::delay::{jitter, Exponential as ExponentialBackoff};

use crate::nats::Error;
use anyhow::anyhow;
use std::time::Duration;
use tokio::time::sleep;

/// Error type that can be either retryable or terminal
#[derive(thiserror::Error, Debug)]
pub enum ClassifiedError<E> {
    #[error(transparent)]
    Retryable(E),
    #[error(transparent)]
    Terminal(E),
}

pub async fn retry<CB, Fut, E>(
    callable: CB,
    retry_strategy: impl IntoIterator<Item = Duration>,
) -> Result<(), Error>
where
    // We call `callable()` repeatedly, each time returning a Future that yields Result<(), ClassifiedError<E>>.
    CB: Fn() -> Fut,
    Fut: std::future::Future<Output = Result<(), ClassifiedError<E>>>,
    Error: From<E>,
{
    let mut last_err: Option<Error> = None;

    // Try immediately, then loop over each delay in our retry strategy.
    for delay in std::iter::once(std::time::Duration::from_secs(0)).chain(retry_strategy) {
        match callable().await {
            Ok(_) => {
                return Ok(());
            }
            Err(classified_err) => {
                match classified_err {
                    ClassifiedError::Terminal(inner_err) => {
                        // Terminal error - stop retrying immediately
                        return Err(Error::from(inner_err));
                    }
                    ClassifiedError::Retryable(inner_err) => {
                        // Retryable error - store it and continue
                        last_err = Some(Error::from(inner_err));
                        sleep(delay).await;
                    }
                }
            }
        }
    }

    // If we never succeeded, return the last error, or a generic fallback.
    Err(last_err.unwrap_or_else(|| Error::Other(anyhow!("All retries failed"))))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Instant;

    #[tokio::test]
    async fn test_retry_with_happy_func() {
        // This async block succeeds immediately.
        async fn mock_fn() -> Result<(), ClassifiedError<anyhow::Error>> {
            Ok(())
        }
        let retry_strategy = ExponentialBackoff::from_millis_with_factor(10, 2.0).take(3);

        let start = Instant::now();
        match retry(mock_fn, retry_strategy).await {
            Ok(_) => {
                let elapsed = Instant::now().duration_since(start);
                // Confirm that the execution returns without any retries.
                assert!(
                    elapsed.as_millis() < 10,
                    "Expected < 10ms, but got {:?}",
                    elapsed
                );
            }
            Err(_) => {
                panic!("Expected Ok, but got Err");
            }
        }
    }

    #[tokio::test]
    async fn test_retry_with_retryable_func() {
        // This async block always fails with retryable errors.
        async fn mock_fn() -> Result<(), ClassifiedError<anyhow::Error>> {
            Err(ClassifiedError::Retryable(anyhow::anyhow!(
                "mock function failed"
            )))
        }
        // Define a small exponential backoff: 10ms, 20ms, 40ms.
        // In total, we attempt 4 times (initial + 3 retries), each failing.
        let retry_strategy = ExponentialBackoff::from_millis_with_factor(10, 2.0).take(3);

        let start = Instant::now();
        match retry(mock_fn, retry_strategy).await {
            Ok(_) => {
                panic!("Expected Err, but got Ok");
            }
            Err(e) => {
                // Confirm the final error string matches the mock function's error.
                let error_str = format!("{e}");
                assert_eq!(error_str, "Other error: mock function failed");
                // Check elapsed time is at least the sum of the delays.
                let elapsed = Instant::now().duration_since(start);
                assert!(
                    elapsed.as_millis() >= 70,
                    "Expected elapsed time >= 70ms, but got only {:?}",
                    elapsed
                );
            }
        }
    }

    #[tokio::test]
    async fn test_retry_with_terminal_func() {
        // This async block fails immediately with a terminal error.
        async fn mock_fn() -> Result<(), ClassifiedError<anyhow::Error>> {
            Err(ClassifiedError::Terminal(anyhow::anyhow!("terminal error")))
        }
        // Define a retry strategy, but it should not be used due to terminal error.
        let retry_strategy = ExponentialBackoff::from_millis_with_factor(100, 2.0).take(3);

        let start = Instant::now();
        match retry(mock_fn, retry_strategy).await {
            Ok(_) => {
                panic!("Expected Err, but got Ok");
            }
            Err(e) => {
                // Confirm the final error string matches the terminal error.
                let error_str = format!("{e}");
                assert_eq!(error_str, "Other error: terminal error");
                // Check that no significant time has elapsed (no retries).
                let elapsed = Instant::now().duration_since(start);
                assert!(
                    elapsed.as_millis() < 50,
                    "Expected elapsed time < 50ms (no retries), but got {:?}",
                    elapsed
                );
            }
        }
    }
}
