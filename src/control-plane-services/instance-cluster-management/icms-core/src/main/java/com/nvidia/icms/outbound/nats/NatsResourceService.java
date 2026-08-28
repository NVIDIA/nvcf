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
package com.nvidia.icms.outbound.nats;

import com.nvidia.icms.configuration.nats.NatsConfiguration.FixedNatsPool;
import io.micrometer.core.annotation.Timed;
import io.nats.client.JetStreamApiException;
import io.nats.client.JetStreamManagement;
import io.nats.client.api.StreamConfiguration;
import java.io.IOException;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;

@Slf4j
@Service
@RequiredArgsConstructor
public class NatsResourceService {

    private final FixedNatsPool fixedNatsPool;

    @Timed(value = "icms.nats.create.stream")
    public void createStream(StreamConfiguration streamConfig)
            throws IOException, JetStreamApiException, InterruptedException {
        JetStreamManagement jetStreamManagement = fixedNatsPool.borrowJetStreamManagement();
        try {
            var streamInfo = jetStreamManagement.getStreamInfo(streamConfig.getName());
            validateStreamConfiguration(streamConfig, streamInfo.getConfiguration());
            return;
        } catch (JetStreamApiException e) {
            // non-404 related error gets passed back up
            if (e.getErrorCode() != 404) {
                throw e;
            }
            // if stream doesn't exist, keep going and try to create
        }
        try {
            // if the stream was created by another server during this gap
            // but the config is the same, this call will succeed
            jetStreamManagement.addStream(streamConfig);
        } catch (JetStreamApiException e) {
            if (e.getApiErrorCode() == 10058) {
                var streamInfo = jetStreamManagement.getStreamInfo(streamConfig.getName());
                validateStreamConfiguration(streamConfig, streamInfo.getConfiguration());
                return;
            }
            throw e;
        }
    }

    private static void validateStreamConfiguration(
            StreamConfiguration expected, StreamConfiguration actual) {
        if (!expected.getSubjects().equals(actual.getSubjects())
                || expected.getStorageType() != actual.getStorageType()
                || expected.getRetentionPolicy() != actual.getRetentionPolicy()
                || expected.getMaxMsgs() != actual.getMaxMsgs()
                || !expected.getMaxAge().equals(actual.getMaxAge())) {
            throw new IllegalStateException(
                    "NATS stream " + expected.getName() + " has an incompatible configuration");
        }
    }
}
