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

import com.datastax.oss.driver.api.core.ConsistencyLevel;
import com.datastax.oss.driver.api.core.connection.ClosedConnectionException;
import com.datastax.oss.driver.api.core.connection.HeartbeatException;
import com.datastax.oss.driver.api.core.context.DriverContext;
import com.datastax.oss.driver.api.core.retry.RetryDecision;
import com.datastax.oss.driver.api.core.retry.RetryPolicy;
import com.datastax.oss.driver.api.core.retry.RetryVerdict;
import com.datastax.oss.driver.api.core.servererrors.CoordinatorException;
import com.datastax.oss.driver.api.core.servererrors.ReadFailureException;
import com.datastax.oss.driver.api.core.servererrors.WriteFailureException;
import com.datastax.oss.driver.api.core.servererrors.WriteType;
import com.datastax.oss.driver.api.core.session.Request;
import lombok.extern.slf4j.Slf4j;

/**
 * Retry policy that retries on the next host for transient failures (connection/heartbeat,
 * read timeout without data, write timeout, unavailable). Rethrows for ReadFailure, WriteFailure,
 * and after one retry attempt.
 */
@Slf4j
public class NextHostRetryPolicy implements RetryPolicy {

    static final String VERDICT_ON_READ_TIMEOUT = "[{}] Verdict on read "
            + "timeout (consistency: {}, required responses: {}, received responses: {}, "
            + "data retrieved: {}, retries: {}): {}";
    static final String VERDICT_ON_WRITE_TIMEOUT = "[{}] Verdict on write "
            + "timeout (consistency: {}, write type: {}, required acknowledgments: {},"
            + " received acknowledgments: {}, retries: {}): {}";
    static final String VERDICT_ON_UNAVAILABLE = "[{}] Verdict on unavailable exception "
            + "(consistency: {}, required replica: {}, alive replica: {}, "
            + "retries: {}): {}";
    static final String VERDICT_ON_ABORTED = "[{}] Verdict on aborted request (type: {}, "
            + "message: '{}', retries: {}): {}";
    static final String VERDICT_ON_ERROR = "[{}] Verdict on node error (type: {}, "
            + "message: '{}', retries: {}): {}";

    private final String logPrefix;

    public NextHostRetryPolicy(DriverContext context, String profileName) {
        var sessionName = context != null ? context.getSessionName() : "No Context";
        this.logPrefix = sessionName + "|" + profileName;
    }

    @Override
    public RetryVerdict onReadTimeoutVerdict(
            Request request,
            ConsistencyLevel cl,
            int blockFor,
            int received,
            boolean dataPresent,
            int retryCount) {
        var verdict = retryCount == 0 && !dataPresent ?
                                                RetryVerdict.RETRY_NEXT : RetryVerdict.RETHROW;
        log.info(VERDICT_ON_READ_TIMEOUT, logPrefix, cl, blockFor, received, dataPresent,
                 retryCount, verdict);
        return verdict;
    }

    @Override
    public RetryVerdict onWriteTimeoutVerdict(
            Request request,
            ConsistencyLevel cl,
            WriteType writeType,
            int blockFor,
            int received,
            int retryCount) {
        var verdict = retryCount == 0 ? RetryVerdict.RETRY_NEXT : RetryVerdict.RETHROW;
        log.info(VERDICT_ON_WRITE_TIMEOUT, logPrefix, cl, writeType, blockFor, received,
                 retryCount, verdict);
        return verdict;
    }

    @Override
    public RetryVerdict onUnavailableVerdict(
            Request request,
            ConsistencyLevel cl,
            int required,
            int alive,
            int retryCount) {
        var verdict = retryCount == 0 ? RetryVerdict.RETRY_NEXT : RetryVerdict.RETHROW;
        log.info(VERDICT_ON_UNAVAILABLE, logPrefix, cl, required, alive, retryCount, verdict);
        return verdict;
    }

    @Override
    public RetryVerdict onRequestAbortedVerdict(
            Request request,
            Throwable error,
            int retryCount) {
        var verdict = error instanceof ClosedConnectionException
                        || error instanceof HeartbeatException
                ? RetryVerdict.RETRY_NEXT
                : RetryVerdict.RETHROW;
        log.info(VERDICT_ON_ABORTED, logPrefix, error.getClass().getSimpleName(),
                 error.getMessage(), retryCount,
                verdict);
        return verdict;
    }

    @Override
    public RetryVerdict onErrorResponseVerdict(
            Request request,
            CoordinatorException error,
            int retryCount) {
        var verdict =
                error instanceof WriteFailureException || error instanceof ReadFailureException
                        ? RetryVerdict.RETHROW
                        : RetryVerdict.RETRY_NEXT;
        log.info(VERDICT_ON_ERROR, logPrefix, error.getClass().getSimpleName(),
                 error.getMessage(), retryCount,
                verdict);
        return verdict;
    }

    @Override
    @Deprecated(forRemoval = true)
    public RetryDecision onReadTimeout(
            Request request,
            ConsistencyLevel cl,
            int blockFor,
            int received,
            boolean dataPresent,
            int retryCount) {
        throw new UnsupportedOperationException("onReadTimeout");
    }

    @Override
    @Deprecated(forRemoval = true)
    public RetryDecision onWriteTimeout(
            Request request,
            ConsistencyLevel cl,
            WriteType writeType,
            int blockFor,
            int received,
            int retryCount) {
        throw new UnsupportedOperationException("onWriteTimeout");
    }

    @Override
    @Deprecated(forRemoval = true)
    public RetryDecision onUnavailable(
            Request request,
            ConsistencyLevel cl,
            int required,
            int alive,
            int retryCount) {
        throw new UnsupportedOperationException("onUnavailable");
    }

    @Override
    @Deprecated(forRemoval = true)
    public RetryDecision onRequestAborted(Request request, Throwable error, int retryCount) {
        throw new UnsupportedOperationException("onRequestAborted");
    }

    @Override
    @Deprecated(forRemoval = true)
    public RetryDecision onErrorResponse(
            Request request,
            CoordinatorException error,
            int retryCount) {
        throw new UnsupportedOperationException("onErrorResponse");
    }

    @Override
    public void close() {
        // Nothing to specifically clean up.
    }
}
