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
package com.nvidia.icms.outbound.fnds;

import static io.cloudevents.jackson.JsonFormat.CONTENT_TYPE;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.params.provider.Arguments.arguments;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyList;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.icms.configuration.bean.IcmsConfigurationProperties;
import com.nvidia.icms.outbound.fnds.model.FndsMessageDetailModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageModel;
import com.nvidia.icms.outbound.fnds.model.FndsMessageV2Model;
import com.nvidia.icms.outbound.fnds.model.FndsStages;
import com.nvidia.icms.service.telemetry.TelemetryEventClient;
import com.nvidia.icms.service.telemetry.model.GenericMetric;
import io.cloudevents.core.format.EventFormat;
import io.cloudevents.core.provider.EventFormatProvider;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import java.util.stream.Stream;
import org.jetbrains.annotations.NotNull;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.MockitoAnnotations;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.ResponseEntity;
import org.springframework.web.reactive.function.client.WebClientResponseException;

class FunctionDeploymentStagesClientTest {

    @Mock
    private IcmsConfigurationProperties icmsConfigurationProperties;

    @Mock
    private TelemetryEventClient telemetryEventClient;

    @Mock
    private FndsStubService fndsStubService;

    private EventFormat eventFormat;
    private FunctionDeploymentStagesClient functionDeploymentStagesClient;

    @BeforeEach
    void setUp() {
        MockitoAnnotations.openMocks(this);

        eventFormat = EventFormatProvider.getInstance().resolveFormat(CONTENT_TYPE);

        functionDeploymentStagesClient = new FunctionDeploymentStagesClient(
                fndsStubService,
                icmsConfigurationProperties,
                telemetryEventClient,
                eventFormat);
    }

    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void testSendFunctionDeploymentStage_DisabledMessages(boolean enableV1, boolean enableV2) {
        mockFndsFlags(enableV1, enableV2);
        when(icmsConfigurationProperties.isFndsMessagesEnabled()).thenReturn(false);

        FndsMessageV2Model message = createFndsMessage();
        Integer response = functionDeploymentStagesClient.sendFunctionDeploymentStage(message);

        assertNull(response);
        verifyNoInteractions(fndsStubService);
    }

    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void testPostMessage_Success(boolean enableV1, boolean enableV2) {
        mockFndsFlags(enableV1, enableV2);
        when(icmsConfigurationProperties.isFndsMessagesEnabled()).thenReturn(true);

        ResponseEntity<Void> accepted = ResponseEntity.status(202).build();
        when(fndsStubService.postDeploymentStageV1(anyString(), anyString(), any(FndsMessageModel.class)))
                .thenReturn(accepted);
        when(fndsStubService.postDeploymentStageV2(anyString(), anyString(), anyString(), any(FndsMessageV2Model.class)))
                .thenReturn(accepted);
        when(fndsStubService.postCloudEvents(any(byte[].class)))
                .thenReturn(accepted);

        FndsMessageV2Model message = createFndsMessage();
        Integer response = functionDeploymentStagesClient.sendFunctionDeploymentStage(message);

        assertEquals(202, response);
    }

    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void testPostMessage_Failure(boolean enableV1, boolean enableV2) {
        mockFndsFlags(enableV1, enableV2);
        when(icmsConfigurationProperties.isFndsMessagesEnabled()).thenReturn(true);

        ResponseEntity<Void> error = ResponseEntity.status(500).build();
        when(fndsStubService.postDeploymentStageV1(anyString(), anyString(), any(FndsMessageModel.class)))
                .thenReturn(error);
        when(fndsStubService.postDeploymentStageV2(anyString(), anyString(), anyString(), any(FndsMessageV2Model.class)))
                .thenReturn(error);
        when(fndsStubService.postCloudEvents(any(byte[].class)))
                .thenReturn(error);

        FndsMessageV2Model message = createFndsMessage();
        Integer response = functionDeploymentStagesClient.sendFunctionDeploymentStage(message);

        assertEquals(500, response);
        int expectedCalls = (enableV1 ? 1 : 0) + (enableV2 ? 1 : 0) + 1;
        verify(telemetryEventClient, times(expectedCalls)).triggerEvent(anyList());
    }


    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void testPostMessage_WebClientResponseException(boolean enableV1, boolean enableV2) {
        mockFndsFlags(enableV1, enableV2);
        when(icmsConfigurationProperties.isFndsMessagesEnabled()).thenReturn(true);

        var exception = new WebClientResponseException(
                "Service Unavailable", 503, "Service Unavailable",
                HttpHeaders.EMPTY, null, null);

        when(fndsStubService.postDeploymentStageV1(anyString(), anyString(), any(FndsMessageModel.class)))
                .thenThrow(exception);
        when(fndsStubService.postDeploymentStageV2(anyString(), anyString(), anyString(), any(FndsMessageV2Model.class)))
                .thenThrow(exception);
        when(fndsStubService.postCloudEvents(any(byte[].class)))
                .thenThrow(exception);

        FndsMessageV2Model message = createFndsMessage();
        Integer response = functionDeploymentStagesClient.sendFunctionDeploymentStage(message);

        assertEquals(503, response);
        int expectedCalls = (enableV1 ? 1 : 0) + (enableV2 ? 1 : 0) + 1;
        verify(telemetryEventClient, times(expectedCalls)).triggerEvent(anyList());
    }

    /**
     * Test to verify that telemetry events are correctly constructed and sent using the TelemetryEventClient.
     */
    @ParameterizedTest
    @MethodSource("getAllComboFor2Booleans")
    void ai_testSendStageTelemetryEvent(boolean enableV1, boolean enableV2) {
        mockFndsFlags(enableV1, enableV2);

        FndsMessageV2Model message = createFndsMessage();

        functionDeploymentStagesClient.sendStageTelemetryEvent(message, 200, "No error", true);

        ArgumentCaptor<List<GenericMetric>> captor = ArgumentCaptor.forClass(List.class);
        verify(telemetryEventClient).triggerEvent(captor.capture());

        List<GenericMetric> metrics = captor.getValue();
        assertEquals(1, metrics.size());
        GenericMetric metric = metrics.get(0);
        assertEquals(message.getFunctionId(), metric.getFunctionId());
        assertEquals(message.getFunctionVersionId(), metric.getFunctionVersionId());
        assertEquals(message.getInstanceId(), metric.getInstanceId());
        assertEquals(200, metric.getHttpCode());
        assertEquals("No error", metric.getError());
        assertEquals(message.getDeploymentId(), metric.getDeploymentId());
    }

    private void mockFndsFlags(boolean enableV1, boolean enableV2) {
        when(icmsConfigurationProperties.isFndsMessagesV1Enabled()).thenReturn(enableV1);
        when(icmsConfigurationProperties.isFndsMessagesV2Enabled()).thenReturn(enableV2);
        when(icmsConfigurationProperties.isFndsMessagesV3Enabled()).thenReturn(true);
    }

    private static @NotNull Stream<Arguments> getAllComboFor2Booleans() {
        return Stream.of(
                arguments(true, true),
                arguments(true, false),
                arguments(false, true),
                arguments(false, false)
        );
    }

    private FndsMessageV2Model createFndsMessage() {
        return FndsMessageV2Model.builder()
                .ncaId(UUID.randomUUID().toString())
                .functionId(UUID.randomUUID().toString())
                .functionVersionId("version-1")
                .deploymentId(UUID.randomUUID())
                .gpuSpecificationId(UUID.randomUUID())
                .instanceId("instance-1")
                .event(FndsStages.STAGE_READY.toString())
                .eventType("SIS")
                .timestamp(Instant.now().toString())
                .details(new FndsMessageDetailModel())
                .build();
    }
}
