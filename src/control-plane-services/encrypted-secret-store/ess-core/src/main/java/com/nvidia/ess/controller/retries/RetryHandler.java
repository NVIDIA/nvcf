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
package com.nvidia.ess.controller.retries;

import static com.nvidia.ess.config.ObservedAspectConfiguration.TRACE_ONLY_NAME;
import static com.nvidia.ess.constants.Constants.UNKNOWN_NAMESPACE;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MAX_RETRY_ATTEMPTS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MAX_RETRY_BACKOFF_TIME_MILLIS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MIN_RETRY_BACKOFF_TIME_MILLIS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.REQUEST_ID_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.RETRY_NUM_KEY;
import static com.nvidia.ess.metrics.CustomMetricsRegistry.NAMESPACE_TAG;

import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import com.nvidia.ess.telemetry.TelemetryComponentsImpl;
import io.micrometer.observation.annotation.Observed;
import java.time.Duration;
import java.util.function.Supplier;
import lombok.Setter;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Qualifier;
import org.springframework.stereotype.Component;
import reactor.core.publisher.Mono;
import reactor.util.retry.Retry;

@Slf4j
@Component
public class RetryHandler {

    @Setter(onMethod_ = {@Autowired})
    @Qualifier(TelemetryComponentsImpl.BEAN_NAME)
    private TelemetryComponents telemetryComponents;

    @Setter(onMethod_ = { @Autowired })
    private CustomMetricsRegistry customMetricsRegistry;

    /**
     *
     * <p>Returns a {@code Mono} that performs up to {@code maxRetries + 1} attempts of the passed
     * {@code Mono} (see argument {@code Supplier<Mono<T>> retryableOperationF}) with retries (within the
     * limit) being attempted only if the most recent attempt failed with a {@link RetryableException}. Any
     * other type of failure shortcircuits the retry-loop.</p>
     * 
     * <p>Successive attempts are separated by an exponential backoff delay starting at
     * {@code minBackoffBetweenRetriesMillis} and capped at {@code maxBackoffBetweenRetriesMillis} milliseconds.</p>
     *
     * @param <T>
     * @param maxRetries
     * @param minBackoffBetweenRetriesMillis
     * @param maxBackoffBetweenRetriesMillis
     * @param retryableOperationF
     * @return
     */
    @Observed(name = TRACE_ONLY_NAME)
    public <T> Mono<T> handleRetries(String requestId,
            int maxRetries,
            long minBackoffBetweenRetriesMillis,
            long maxBackoffBetweenRetriesMillis,
            Supplier<Mono<T>> retryableOperationF) {

        return Mono.deferContextual(mainContext ->  {
            telemetryComponents.setSpanAttribute(mainContext, REQUEST_ID_KEY, requestId);
            telemetryComponents.setSpanAttribute(mainContext, MAX_RETRY_ATTEMPTS_KEY, maxRetries);
            telemetryComponents.setSpanAttribute(mainContext, MIN_RETRY_BACKOFF_TIME_MILLIS_KEY, minBackoffBetweenRetriesMillis);
            telemetryComponents.setSpanAttribute(mainContext, MAX_RETRY_BACKOFF_TIME_MILLIS_KEY, maxBackoffBetweenRetriesMillis);
            final var op = retryableOperationF.get();
            return op.retryWhen(Retry.backoff(maxRetries, Duration.ofMillis(minBackoffBetweenRetriesMillis))
                    .maxBackoff(Duration.ofMillis(maxBackoffBetweenRetriesMillis))
                    .filter(RetryableException.class::isInstance)
                    .withRetryContext(mainContext)
                    .doBeforeRetry(retrySignal ->
                        // Before a retry, reset any error-status back to OK.
                        telemetryComponents.setSpanStatusOk(mainContext)
                    )
                    .doAfterRetry(retrySignal -> {
                        var ex = (RetryableException) retrySignal.failure();
                        var numAttemptsAtThisPoint = retrySignal.totalRetries() + 1;
                        var contextView = retrySignal.retryContextView();
                        // Need to inject the request-id into this log-message.
                        log.warn("Encountered RetryableException while executing retryable-operation. Request ID: " +
                                requestId + ". Num attempts so far: " + numAttemptsAtThisPoint + ". Attempting a retry. " +
                                "Error: " + ex.getRetriesExhaustedFallbackError().getMessage());
                        telemetryComponents.recordException(mainContext, ex);
                        telemetryComponents.setSpanAttribute(mainContext, RETRY_NUM_KEY, numAttemptsAtThisPoint);
                        String namespace = contextView.getOrDefault(NAMESPACE_TAG, UNKNOWN_NAMESPACE);
                        customMetricsRegistry.recordRetryableError(namespace,
                                ex.getRetriesExhaustedFallbackError().getSummary().getProblemBrief());
                    })
                    .onRetryExhaustedThrow((retrySpec, retrySignal) -> {
                        var ex = (RetryableException) retrySignal.failure();
                        log.error("Encountered RetryableException while executing retryable-operation but all " +
                                maxRetries + " retries were exhausted. Request ID: " + requestId + ". Error: " +
                                ex.getRetriesExhaustedFallbackError().getMessage());
                        return ex.getRetriesExhaustedFallbackError();
                    })
            );
        });
    }
}
