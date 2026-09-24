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
package io.nats.client.impl;

import io.micrometer.observation.annotation.Observed;
import io.micrometer.tracing.Tracer;
import io.nats.client.Message;
import io.nats.client.Options;
import io.nats.client.support.NatsRequestCompletableFuture.CancelAction;
import java.io.IOException;
import java.time.Duration;
import java.util.Map;
import java.util.concurrent.CompletableFuture;

public class TracedNatsConnection extends NatsConnection {

    private final Tracer tracer;

    private static final String OBSERVATION_PUBLISH = "nats-publish-internal";
    private static final String OBSERVATION_REQUEST_FUTURE = "nats-request-future-internal";
    private static final String TAG_SUBJECT = "subject";
    private static final String TAG_REPLY_TO = "replyTo";
    private static final String TAG_FUTURE_TIMEOUT = "futureTimeout";

    public TracedNatsConnection(Options options, Tracer tracer) {
        super(options);
        this.tracer = tracer;
    }

    public void connect(boolean reconnectOnConnect)
            throws InterruptedException, IOException {
        super.connect(reconnectOnConnect);
    }

    @Observed(name = OBSERVATION_PUBLISH)
    @Override
    void publishInternal(
            String subject, String replyTo, Headers headers,
            byte[] data, boolean validateSubRep, boolean flushImmediatelyAfterPublish) {
        addTagsToCurrentSpan(tracer, Map.of(
                TAG_SUBJECT, subject,
                TAG_REPLY_TO, String.valueOf(replyTo)
        ));
        super.publishInternal(subject, replyTo, headers, data, validateSubRep,
                              flushImmediatelyAfterPublish);
    }

    @Observed(name = OBSERVATION_REQUEST_FUTURE)
    @Override
    CompletableFuture<Message> requestFutureInternal(
            String subject, Headers headers, byte[] data,
            Duration futureTimeout, CancelAction cancelAction,
            boolean validateSubjectAndReplyTo, boolean flushImmediatelyAfterPublish) {
        addTagsToCurrentSpan(tracer, Map.of(
                TAG_SUBJECT, subject,
                TAG_FUTURE_TIMEOUT, String.valueOf(futureTimeout)
        ));
        return super.requestFutureInternal(subject, headers, data, futureTimeout, cancelAction,
                                           validateSubjectAndReplyTo, flushImmediatelyAfterPublish);
    }

    private static void addTagsToCurrentSpan(Tracer tracer, Map<String, Object> tags) {
        var span = tracer.currentSpan();
        if (span != null) {
            tags.forEach((key, value) -> span.tag(key, String.valueOf(value)));
        }
    }
}
