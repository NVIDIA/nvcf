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
import static com.datastax.oss.driver.api.core.servererrors.WriteType.BATCH_LOG;
import static com.datastax.oss.driver.api.core.servererrors.WriteType.UNLOGGED_BATCH;
import static org.mockito.Mockito.mock;

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.core.connection.ClosedConnectionException;
import com.datastax.oss.driver.api.core.connection.HeartbeatException;
import com.datastax.oss.driver.api.core.metadata.Node;
import com.datastax.oss.driver.api.core.servererrors.CoordinatorException;
import com.datastax.oss.driver.api.core.servererrors.OverloadedException;
import com.datastax.oss.driver.api.core.servererrors.ReadFailureException;
import com.datastax.oss.driver.api.core.servererrors.ServerError;
import com.datastax.oss.driver.api.core.servererrors.TruncateException;
import com.datastax.oss.driver.api.core.servererrors.UnavailableException;
import com.datastax.oss.driver.api.core.servererrors.WriteFailureException;
import com.datastax.oss.driver.api.core.servererrors.WriteType;
import com.datastax.oss.driver.api.core.session.Request;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import org.junit.jupiter.api.Assertions;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class NextHostRetryPolicyTest extends RetryPolicyTestBase {

    NextHostRetryPolicyTest() {
        super(new NextHostRetryPolicy(null, "testSession1"));
    }

    @Test
    void shouldProcessReadTimeouts() {
        assertOnReadTimeout(QUORUM, 2, 2, false, 1).hasDecision(RETHROW);
        assertOnReadTimeout(QUORUM, 2, 1, false, 0).hasDecision(RETRY_NEXT);
        assertOnReadTimeout(QUORUM, 2, 1, true, 0).hasDecision(RETHROW);
    }

    @Test
    void shouldProcessWriteTimeouts() {
        assertOnWriteTimeout(QUORUM, BATCH_LOG, 2, 2, 1).hasDecision(RETHROW);
        assertOnWriteTimeout(QUORUM, BATCH_LOG, 1, 1, 0).hasDecision(RETRY_NEXT);
        assertOnWriteTimeout(QUORUM, UNLOGGED_BATCH, 1, 1, 0).hasDecision(RETRY_NEXT);
    }

    @Test
    void shouldProcessUnavailable() {
        assertOnUnavailable(QUORUM, 2, 1, 1).hasDecision(RETHROW);
        assertOnUnavailable(QUORUM, 2, 1, 0).hasDecision(RETRY_NEXT);
    }

    @Test
    void shouldProcessAbortedRequest() {
        assertOnRequestAborted(ClosedConnectionException.class, 0).hasDecision(RETRY_NEXT);
        assertOnRequestAborted(ClosedConnectionException.class, 1).hasDecision(RETRY_NEXT);
        assertOnRequestAborted(HeartbeatException.class, 0).hasDecision(RETRY_NEXT);
        assertOnRequestAborted(HeartbeatException.class, 1).hasDecision(RETRY_NEXT);
        assertOnRequestAborted(Throwable.class, 0).hasDecision(RETHROW);
    }

    @Test
    void shouldProcessErrorResponse() {
        assertOnErrorResponse(ReadFailureException.class, 0).hasDecision(RETHROW);
        assertOnErrorResponse(ReadFailureException.class, 1).hasDecision(RETHROW);
        assertOnErrorResponse(WriteFailureException.class, 0).hasDecision(RETHROW);
        assertOnErrorResponse(WriteFailureException.class, 1).hasDecision(RETHROW);

        assertOnErrorResponse(OverloadedException.class, 0).hasDecision(RETRY_NEXT);
        assertOnErrorResponse(OverloadedException.class, 1).hasDecision(RETRY_NEXT);
        assertOnErrorResponse(ServerError.class, 0).hasDecision(RETRY_NEXT);
        assertOnErrorResponse(ServerError.class, 1).hasDecision(RETRY_NEXT);
        assertOnErrorResponse(TruncateException.class, 0).hasDecision(RETRY_NEXT);
        assertOnErrorResponse(TruncateException.class, 1).hasDecision(RETRY_NEXT);
    }

    @Test
    void shouldHandleUnsupportedOperation() throws Exception {
        var requestMock = mock(Request.class);
        var mockNode = mock(Node.class);
        var unavailableEx = new UnavailableException(mockNode, QUORUM, 1, 1);
        var policy = new NextHostRetryPolicy(null, "testSession1");

        // Using reflection to shutup IntelliJ from complaining about using deprecated methods
        // in NextHostRetryPolicy.
        assertDeprecatedThrows(policy, NextHostRetryPolicy.class.getMethod("onReadTimeout",
                Request.class, ConsistencyLevel.class, int.class, int.class, boolean.class, int.class),
                requestMock, QUORUM, 1, 1, false, 1);
        assertDeprecatedThrows(policy, NextHostRetryPolicy.class.getMethod("onWriteTimeout",
                Request.class, ConsistencyLevel.class, WriteType.class, int.class, int.class, int.class),
                requestMock, QUORUM, BATCH_LOG, 1, 1, 1);
        assertDeprecatedThrows(policy, NextHostRetryPolicy.class.getMethod("onUnavailable",
                Request.class, ConsistencyLevel.class, int.class, int.class, int.class),
                requestMock, QUORUM, 1, 1, 1);
        assertDeprecatedThrows(policy, NextHostRetryPolicy.class.getMethod("onRequestAborted",
                Request.class, Throwable.class, int.class),
                requestMock, unavailableEx, 1);
        assertDeprecatedThrows(policy, NextHostRetryPolicy.class.getMethod("onErrorResponse",
                Request.class, CoordinatorException.class, int.class),
                requestMock, unavailableEx, 1);
    }

    private void assertDeprecatedThrows(NextHostRetryPolicy policy, Method method, Object... args) {
        var thrown = Assertions.assertThrows(InvocationTargetException.class,
                () -> method.invoke(policy, args));
        Assertions.assertInstanceOf(UnsupportedOperationException.class, thrown.getCause());
    }

    @Test
    void testClose() {
        var policy = new NextHostRetryPolicy(null, "testSession1");
        Assertions.assertDoesNotThrow(policy::close);
    }
}
