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

package com.nvidia.boot.telemetry.client;

import io.cloudevents.core.builder.CloudEventBuilder;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.UUID;
import org.apache.commons.lang3.StringUtils;
import org.springframework.security.core.context.SecurityContextHolder;

/**
 * Provides a pre-configured CloudEventBuilder for creating CloudEvents.
 */
public class CloudEventBuilderProvider {

    private static final String DEFAULT_SUBJECT = "unknown-subject";
    private static final String CLOUDEVENT_DATA_CONTENT_TYPE = "application/json";

    /**
     * Returns a CloudEventBuilder with common attributes (id, time,
     * dataContentType, subject) pre-set.
     */
    public CloudEventBuilder getCloudEventBuilder() {
        return CloudEventBuilder.v1()
                .withId(UUID.randomUUID().toString())
                .withTime(Instant.now().atOffset(ZoneOffset.UTC))
                .withDataContentType(CLOUDEVENT_DATA_CONTENT_TYPE)
                .withSubject(getSubject());
    }

    private String getSubject() {
        var authentication = SecurityContextHolder.getContext().getAuthentication();
        var name = authentication != null ? authentication.getName() : null;
        return StringUtils.isNotEmpty(name) ? name : DEFAULT_SUBJECT;
    }
}
