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
package com.nvidia.icms.service.telemetry;

import static com.nvidia.icms.util.TestUtil.DUMMY_CUSTOMER_ID;
import static com.nvidia.icms.util.TestUtil.DUMMY_ENV;
import static com.nvidia.icms.util.TestUtil.DUMMY_REGION;
import static com.nvidia.icms.util.TestUtil.DUMMY_REQUEST_ID;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.verify;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.telemetry.client.CloudEventBuilderProvider;
import com.nvidia.boot.telemetry.client.TelemetryClient;
import com.nvidia.icms.inbound.rest.model.SpotInstanceRequestState;
import com.nvidia.icms.service.telemetry.model.Events;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.cloudevents.core.builder.CloudEventBuilder;
import java.net.URI;
import java.time.OffsetDateTime;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class TelemetryEventClientTest {

    private static final String DUMMY_RESOURCE_NAME = "icms-service.telemetry";

    @Mock
    private TelemetryClient telemetryClient;

    @Mock
    private CloudEventBuilderProvider cloudEventBuilderProvider;

    @Mock
    private DatadogEventLogger datadogEventLogger;

    private TelemetryEventClient telemetryEventClient;

    @BeforeEach
    void init() {
        CloudEventBuilder builder = CloudEventBuilder.v1()
                .withId(UUID.randomUUID().toString())
                .withSource(URI.create("test"))
                .withType("test")
                .withTime(OffsetDateTime.now())
                .withDataContentType("application/json");
        doReturn(builder).when(cloudEventBuilderProvider).getCloudEventBuilder();

        telemetryEventClient =
                new TelemetryEventClient(telemetryClient, cloudEventBuilderProvider,
                        DUMMY_RESOURCE_NAME, true, DUMMY_ENV, DUMMY_REGION,
                        new JsonMapper(), datadogEventLogger);
    }

    @Test
    void triggerEvent_withValidParam_returnsSuccess() {
        doReturn(CompletableFuture.completedFuture(null))
                .when(telemetryClient).sendAsync(anyString(), anyList());

        telemetryEventClient.triggerEvent(
                List.of(new GenericMetric().withEventName(Events.CREATE_INSTANCES.toString())
                                .withCustomer(DUMMY_CUSTOMER_ID)
                                .withRequestId(DUMMY_REQUEST_ID)
                                .withInstanceCount(2)
                                .withRequestState(SpotInstanceRequestState.OPEN.toString())));

        verify(telemetryClient).sendAsync(anyString(), anyList());
    }

    @Test
    void triggerEvent_withEventTriggerFailed_returnsWithErrorLogging() {
        doReturn(CompletableFuture.failedFuture(new RuntimeException("dummy_exception")))
                .when(telemetryClient).sendAsync(anyString(), anyList());

        GenericMetric genericMetric =
                new GenericMetric().withEventName(Events.CREATE_INSTANCES.toString())
                        .withCustomer(DUMMY_CUSTOMER_ID)
                        .withRequestId(DUMMY_REQUEST_ID)
                        .withInstanceCount(2)
                        .withRequestState(SpotInstanceRequestState.OPEN.toString());
        telemetryEventClient.triggerEvent(
                List.of(genericMetric));

        verify(telemetryClient).sendAsync(anyString(), anyList());
    }
}
