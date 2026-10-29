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

import static com.datastax.oss.driver.api.core.ConsistencyLevel.QUORUM;
import static com.datastax.oss.driver.api.core.retry.RetryDecision.RETHROW;
import static com.datastax.oss.driver.api.core.retry.RetryDecision.RETRY_NEXT;
import static com.datastax.oss.driver.api.core.servererrors.WriteType.UNLOGGED_BATCH;

import com.datastax.oss.driver.api.core.connection.ClosedConnectionException;
import com.datastax.oss.driver.api.core.servererrors.ReadFailureException;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class RangeReadRetryPolicyTest extends RetryPolicyTestBase {

    RangeReadRetryPolicyTest() {
        super(new RangeReadRetryPolicy(null, "testSession1"));
    }

    @Test
    void shouldRetryReadTimeoutWithDataButTooFewResponses() {
        // A range read has data from the coordinator's own replica, but a replica is down.
        assertOnReadTimeout(QUORUM, 2, 1, true, 0).hasDecision(RETRY_NEXT);
        assertOnReadTimeout(QUORUM, 2, 1, true, 1).hasDecision(RETHROW);
    }

    @Test
    void shouldNotRetryReadTimeoutWhenEnoughResponsesArrived() {
        assertOnReadTimeout(QUORUM, 2, 2, true, 0).hasDecision(RETHROW);
    }

    @Test
    void shouldKeepBaseBehaviourForReadTimeoutsWithoutData() {
        assertOnReadTimeout(QUORUM, 2, 2, false, 1).hasDecision(RETHROW);
        assertOnReadTimeout(QUORUM, 2, 1, false, 0).hasDecision(RETRY_NEXT);
    }

    @Test
    void shouldInheritTheOtherVerdictsFromNextHostRetryPolicy() {
        assertOnWriteTimeout(QUORUM, UNLOGGED_BATCH, 2, 1, 0).hasDecision(RETRY_NEXT);
        assertOnWriteTimeout(QUORUM, UNLOGGED_BATCH, 2, 1, 1).hasDecision(RETHROW);
        assertOnUnavailable(QUORUM, 2, 1, 0).hasDecision(RETRY_NEXT);
        assertOnRequestAborted(ClosedConnectionException.class, 0).hasDecision(RETRY_NEXT);
        assertOnErrorResponse(ReadFailureException.class, 0).hasDecision(RETHROW);
    }
}
