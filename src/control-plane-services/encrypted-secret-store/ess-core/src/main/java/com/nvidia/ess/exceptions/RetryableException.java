/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package com.nvidia.ess.exceptions;

import static com.nvidia.ess.constants.Constants.MSG_INTERNAL_ERROR;

import com.nvidia.boot.exceptions.BootResponseException;
import lombok.Getter;
import lombok.NonNull;
import org.springframework.http.HttpStatus;

/**
 * A wrapper for exceptions caused by issues that don't block retries in an outer-loop
 * (either because they're transient issues [e.g. connection-issues] or because they
 * aren't raised due to conflicts with preexisting state that won't change without
 * human intervention).
 *
 * For when an outer-retry-loop runs out of retries, the underlying root cause is saved for
 * logging and reporting purposes. The error-message itself shouldn't be included in the
 * error-response (which should be a 500) when retries run out, but should be logged. The
 * error-response message itself should be generic ("an internal error occurred").
 */
public class RetryableException extends BootResponseException {

  @Getter
  private final RetriesExhaustedException retriesExhaustedFallbackError;

  // TODO: Utils to remove the internal detail from the returned error-response and log it instead.
  // This applies to `RetryableException` and `AnomalyException`.
  //
  // We need something like this:
  //
  // @Slf4j
  // public class ErrorUtils {
  //
  // /* ... */
  //
  //   /*
  //    * Replace all invocations of `Mono.error()` with this, and then
  //    * remove the `cause` argument from `RetryableException(...)` as well
  //    * as `AnomalyException(...)`.
  //    */
  //   Mono<T> loggedErrorMono(Exception ex, String logTheCause) {
  //     return Mono.error(ex).doOnError(e -> {
  //       // Logs only when this error-raising `Mono<>` is subscribed.
  //       log.error("Encountered exception: {}, with cause'{}'", e, logTheCause);
  //     });
  //   }
  //
  // /* ... */
  //
  // }
  public RetryableException(@NonNull RetriesExhaustedException retriesExhaustedFallbackError) {
    // Error-code: 500.
    super(HttpStatus.INTERNAL_SERVER_ERROR,
        // Message in error-response body.
        MSG_INTERNAL_ERROR,
        // Underlying message detailing error, with root-cause.
        retriesExhaustedFallbackError,
        RetryableException.class);

    this.retriesExhaustedFallbackError = retriesExhaustedFallbackError;
  }
}
