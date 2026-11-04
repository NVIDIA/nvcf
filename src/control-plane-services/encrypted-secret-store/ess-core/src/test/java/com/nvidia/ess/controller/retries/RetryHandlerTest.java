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

import static com.nvidia.ess.constants.OpenTelemetryAttributes.MAX_RETRY_ATTEMPTS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MAX_RETRY_BACKOFF_TIME_MILLIS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.MIN_RETRY_BACKOFF_TIME_MILLIS_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.REQUEST_ID_KEY;
import static com.nvidia.ess.constants.OpenTelemetryAttributes.RETRY_NUM_KEY;
import static com.nvidia.ess.util.TestConstants.TEST_PROBLEM_SUMMARY;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyLong;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoMoreInteractions;

import com.nvidia.ess.exceptions.RetriesExhaustedInternalErrorException;
import com.nvidia.ess.exceptions.RetryableException;
import com.nvidia.ess.metrics.CustomMetricsRegistry;
import com.nvidia.ess.telemetry.TelemetryComponents;
import lombok.Builder;
import lombok.Getter;
import lombok.NonNull;
import lombok.experimental.SuperBuilder;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.ContextView;

@ExtendWith(MockitoExtension.class)
class RetryHandlerTest {

    private CustomMetricsRegistry customMetricsRegistry;

    @Mock
    private TelemetryComponents telemetryComponents;

    @InjectMocks
    private RetryHandler retryHandler;

    @BeforeEach
    void setUp() {
        customMetricsRegistry = mock(CustomMetricsRegistry.class);
        retryHandler.setCustomMetricsRegistry(customMetricsRegistry);
    }

    @SuperBuilder
    private static class FixedNumErrorsBeforeSuccessMonoProvider {
        private final int numAttemptsBeforeSuccess;

        private final boolean retryableOp;

        @Builder.Default
        @Getter
        private int numAttemptsSoFar = 0;

        protected Exception ex() {
            return retryableOp
                    ? new RetryableException(new RetriesExhaustedInternalErrorException(TEST_PROBLEM_SUMMARY, "test"))
                    : new RuntimeException("test");
        }

        final public Mono<Void> provide() {
            return Mono.defer(() -> {
                Assertions.assertTrue(numAttemptsSoFar <= numAttemptsBeforeSuccess);
                if (numAttemptsSoFar < numAttemptsBeforeSuccess) {
                    // Error on attempt ${k} where ${k} < ${numAttemptsBeforeSuccess}
                    var err = ex();
                    ++numAttemptsSoFar;
                    return Mono.error(err);
                }
                // Success after ${numAttemptsBeforeSuccess} attempts.
                ++numAttemptsSoFar;
                return Mono.empty();
            });
        }
    }

    @Builder
    @Getter
    private static final class ErrorAtSpecificAttempt {
        private final int raiseErrorAtThisAttempt;

        @NonNull
        private final Exception errorToRaiseAtSpecificAttempt;
    }

    @SuperBuilder
    private static final class SpecificErrorAtSpecificFailedAttemptMonoProvider
            extends FixedNumErrorsBeforeSuccessMonoProvider {

        private final ErrorAtSpecificAttempt errorAtSpecificAttempt;

        @Override
        protected Exception ex() {
            return getNumAttemptsSoFar() + 1 == errorAtSpecificAttempt.getRaiseErrorAtThisAttempt()
                    ? errorAtSpecificAttempt.getErrorToRaiseAtSpecificAttempt()
                    : super.ex();
        }
    }

    @Test
    void testRetryHandler_zeroRetries_opSuccessfulOnFirstTry_success() {
        var opProvider = FixedNumErrorsBeforeSuccessMonoProvider.builder()
                .numAttemptsBeforeSuccess(0)
                .retryableOp(true)
                .build();

        var opThatSucceedsOnFirstAttempt = opProvider.provide();
        var opRetriedZeroTimes = retryHandler.handleRetries("", 0, 1, 1, () -> opThatSucceedsOnFirstAttempt);

        StepVerifier.create(opRetriedZeroTimes)
                .expectComplete()
                .verify();

        // Expect exactly 1 attempt (the successful one).
        Assertions.assertEquals(1, opProvider.getNumAttemptsSoFar());

        verifyInitialSpanAttributes();
        verifyNoMoreInteractions(telemetryComponents);
    }

    @Test
    void testRetryHandler_opReturnsOnlyNonRetryableErrors_error() {

        var opProvider = FixedNumErrorsBeforeSuccessMonoProvider.builder()
                .numAttemptsBeforeSuccess(3)
                .retryableOp(false)
                .build();

        var opThatFailsThriceBeforeSucceeding = opProvider.provide();
        var opRetriedUpToFiveTimes = retryHandler.handleRetries("", 5, 1, 1, () -> opThatFailsThriceBeforeSucceeding);

        StepVerifier.create(opRetriedUpToFiveTimes)
                .expectError(RuntimeException.class)
                .verify();

        // Only 1 attempt. No actual retries.
        Assertions.assertEquals(1, opProvider.getNumAttemptsSoFar());

        verifyInitialSpanAttributes();
        verifyNoMoreInteractions(telemetryComponents);
    }

    @Test
    void testRetryHandler_opReturnsOnlyRetryableErrors_moreFailedAttemptsThanLimit_error() {

        var opProvider = FixedNumErrorsBeforeSuccessMonoProvider.builder()
                .numAttemptsBeforeSuccess(6)
                .retryableOp(true)
                .build();

        var opThatFailsSixTimesBeforeSucceeding = opProvider.provide();
        var opRetriedUpToFiveTimes = retryHandler.handleRetries("req-1", 5, 1, 1, () -> opThatFailsSixTimesBeforeSucceeding);

        doNothing().when(customMetricsRegistry).recordRetryableError(any(), any());

        StepVerifier.create(opRetriedUpToFiveTimes)
                .expectError(RetriesExhaustedInternalErrorException.class)
                .verify();

        // Check for 1 attempt and 5 retries.
        Assertions.assertEquals(6, opProvider.getNumAttemptsSoFar());

        verify(telemetryComponents, times(5))
                .recordException(any(ContextView.class), any(Throwable.class));
        verify(telemetryComponents, times(5))
                .setSpanStatusOk(any(ContextView.class));
        for (var i = 0; i < 5; ++i) {
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(RETRY_NUM_KEY), eq(i + 1L));
        }
    }

    @Test
    void testRetryHandler_opReturnsOnlyRetryableErrors_failedAttemptsSameAsOrFewerThanLimit_success() {

        var opProvider = FixedNumErrorsBeforeSuccessMonoProvider.builder()
                .numAttemptsBeforeSuccess(10)
                .retryableOp(true)
                .build();

        var opThatFailsTenTimesBeforeSucceeding = opProvider.provide();
        var opRetriedUpToTenTimes = retryHandler.handleRetries("req-1", 10, 1, 1, () -> opThatFailsTenTimesBeforeSucceeding);

        doNothing().when(customMetricsRegistry).recordRetryableError(any(), any());

        StepVerifier.create(opRetriedUpToTenTimes)
                .expectComplete()
                .verify();

        // Check for 10 failed attempts followed by 1 success.
        Assertions.assertEquals(11, opProvider.getNumAttemptsSoFar());

        verify(telemetryComponents, times(10))
                .recordException(any(ContextView.class), any(Throwable.class));
        // All 10 retries within limit; doBeforeRetry resets to OK before each retry attempt
        verify(telemetryComponents, times(10))
                .setSpanStatusOk(any(ContextView.class));
        for (var i = 0; i < 10; ++i) {
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(RETRY_NUM_KEY), eq(i + 1L));
        }
    }

    @Test
    void testRetryHandler_fourRetries_nonRetryableErrorThirdTime_dontAttemptThirdRetry_error() {

        var opProvider = SpecificErrorAtSpecificFailedAttemptMonoProvider.builder()
                // Op fails 4 times before a successful attempt.
                .numAttemptsBeforeSuccess(4)
                // Failures: 1, 2 and 4 (if it were executed) are RetryableException failures.
                .retryableOp(true)
                // Failure 3 is a RuntimeException failure.
                .errorAtSpecificAttempt(
                        ErrorAtSpecificAttempt.builder()
                                .raiseErrorAtThisAttempt(3)
                                .errorToRaiseAtSpecificAttempt(new RuntimeException("test"))
                                .build())
                .build();

        var opThatFailsFourTimesBeforeSucceeding = opProvider.provide();
        var opRetriedUpToFourTimes = retryHandler.handleRetries("req-1", 4, 1, 1, () -> opThatFailsFourTimesBeforeSucceeding);

        doNothing().when(customMetricsRegistry).recordRetryableError(any(), any());

        StepVerifier.create(opRetriedUpToFourTimes)
                .expectError(RuntimeException.class)
                .verify();

        // Expect 1 failure and 2 failed retries (the last one yielding a non-retryable
        // error,
        // thus stopping the retry-loop).
        Assertions.assertEquals(3, opProvider.getNumAttemptsSoFar());

        verify(telemetryComponents, times(2))
                .recordException(any(ContextView.class), any(Throwable.class));
        // doBeforeRetry fires before attempts 2 and 3 (both within limit)
        verify(telemetryComponents, times(2))
                .setSpanStatusOk(any(ContextView.class));
        for (var i = 0; i < 2; ++i) {
            verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(RETRY_NUM_KEY), eq(i + 1L));
        }
    }

    private void verifyInitialSpanAttributes() {
        verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(REQUEST_ID_KEY), anyString());
        verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MAX_RETRY_ATTEMPTS_KEY), anyLong());
        verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MIN_RETRY_BACKOFF_TIME_MILLIS_KEY), anyLong());
        verify(telemetryComponents).setSpanAttribute(any(ContextView.class), eq(MAX_RETRY_BACKOFF_TIME_MILLIS_KEY), anyLong());
    }
}
