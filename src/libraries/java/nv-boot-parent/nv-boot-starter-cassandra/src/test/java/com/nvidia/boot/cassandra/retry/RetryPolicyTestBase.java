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

package com.nvidia.boot.cassandra.retry;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.core.retry.RetryDecision;
import com.datastax.oss.driver.api.core.retry.RetryPolicy;
import com.datastax.oss.driver.api.core.retry.RetryVerdict;
import com.datastax.oss.driver.api.core.servererrors.CoordinatorException;
import com.datastax.oss.driver.api.core.servererrors.WriteType;
import com.datastax.oss.driver.api.core.session.Request;
import org.assertj.core.api.AbstractAssert;
import org.mockito.Mock;

public abstract class RetryPolicyTestBase {
    private final RetryPolicy policy;

    @Mock
    private Request request;

    RetryPolicyTestBase(RetryPolicy policy) {
        this.policy = policy;
    }

    RetryVerdictAssert assertOnReadTimeout(
            ConsistencyLevel cl,
            int blockFor,
            int received,
            boolean dataPresent,
            int retryCount) {
        return new RetryVerdictAssert(
                policy.onReadTimeoutVerdict(request, cl, blockFor, received, dataPresent, retryCount));
    }

    RetryVerdictAssert assertOnWriteTimeout(
            ConsistencyLevel cl,
            WriteType writeType,
            int blockFor,
            int received,
            int retryCount) {
        return new RetryVerdictAssert(
                policy.onWriteTimeoutVerdict(request, cl, writeType, blockFor, received, retryCount));
    }

    RetryVerdictAssert assertOnUnavailable(
            ConsistencyLevel cl,
            int required,
            int alive,
            int retryCount) {
        return new RetryVerdictAssert(
                policy.onUnavailableVerdict(request, cl, required, alive, retryCount));
    }

    RetryVerdictAssert assertOnRequestAborted(
            Class<? extends Throwable> errorClass,
            int retryCount) {
        return new RetryVerdictAssert(
                policy.onRequestAbortedVerdict(request, mock(errorClass), retryCount));
    }

    RetryVerdictAssert assertOnErrorResponse(
            Class<? extends CoordinatorException> errorClass,
            int retryCount) {
        return new RetryVerdictAssert(
                policy.onErrorResponseVerdict(request, mock(errorClass), retryCount));
    }

    static class RetryVerdictAssert extends AbstractAssert<RetryVerdictAssert, RetryVerdict> {
        RetryVerdictAssert(RetryVerdict actual) {
            super(actual, RetryVerdictAssert.class);
        }

        public RetryVerdictAssert hasDecision(RetryDecision decision) {
            assertThat(actual.getRetryDecision()).isEqualTo(decision);
            return this;
        }
    }
}
