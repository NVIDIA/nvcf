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
package com.nvidia.nvcf.service.scheduler;

import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_4;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nvidia.nvcf.persistence.function.FunctionsDeploymentRepository;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionDeploymentKey;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.service.nats.NatsResourceService;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.time.Duration;
import java.time.Instant;
import java.time.ZonedDateTime;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class CleanNatsStreamsTaskTest {

    @Mock
    private FunctionsDeploymentRepository functionsDeploymentRepository;

    @Mock
    private FunctionsRepository functionsRepository;

    @Mock
    private NatsResourceService natsResourceService;

    private CleanNatsStreamsTask cleanNatsStreamsTask;

    @BeforeEach
    void setUp() {
        cleanNatsStreamsTask = new CleanNatsStreamsTask(functionsDeploymentRepository,
                                                        functionsRepository,
                                                        natsResourceService);
    }

    @Test
    void testRun()
            throws InterruptedException, JetStreamApiException, IOException {
        // version 1 deployment missing, stream exists but is brand-new
        // version 2 deployment missing, stream exists
        // version 3 deployment exists, stream exists
        // version 4 deployment exists, stream exists, function is errored
        // we expect the stream for version 2 and 4 to be deleted

        // mock v1
        lenient().when(
                        functionsDeploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_1))
                .thenReturn(Optional.empty());
        var newStream = mock(StreamInfo.class);
        when(newStream.getCreateTime()).thenReturn(
                ZonedDateTime.now().minus(Duration.ofSeconds(1)));
        lenient().when(newStream.getConfiguration())
                .thenReturn(StreamConfiguration.builder()
                                    .name("rq_us-west-2_" + TEST_VERSION_ID_1)
                                    .build());

        // mock v2
        when(functionsDeploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_2))
                .thenReturn(Optional.empty());
        var oldExtraStream = mock(StreamInfo.class);
        when(oldExtraStream.getCreateTime()).thenReturn(
                ZonedDateTime.now().minus(Duration.ofHours(1)));
        when(oldExtraStream.getConfiguration())
                .thenReturn(StreamConfiguration.builder()
                                    .name("rq_us-west-2_" + TEST_VERSION_ID_2)
                                    .build());

        // mock v3
        when(functionsDeploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_3))
                .thenReturn(Optional.of(FunctionDeploymentEntity.builder()
                                                .key(FunctionDeploymentKey.builder()
                                                             .functionVersionId(TEST_VERSION_ID_3)
                                                             .build())
                                                .deploymentId(TEST_DEPLOYMENT_ID)
                                                .functionId(TEST_FUNCTION_ID)
                                                .ncaId(TEST_NCA_ID)

                                                .createdAt(Instant.now().minus(Duration.ofHours(1)))
                                                .lastUpdatedAt(Instant.now())
                                                .build()));
        when(functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_3))
                .thenReturn(Optional.of(FunctionEntity.builder()
                                                .functionId(TEST_FUNCTION_ID)
                                                .functionVersionId(TEST_VERSION_ID_3)
                                                .functionStatus(FunctionStatus.ACTIVE)
                                                .ncaId(TEST_NCA_ID)
                                                .functionName(TEST_FUNCTION_NAME)
                                                .inferenceUrl(TEST_INFERENCE_URL.toString())
                                                .apiBodyFormat(ApiBodyFormat.CUSTOM)
                                                .build()));
        var activeStream = mock(StreamInfo.class);
        when(activeStream.getCreateTime()).thenReturn(
                ZonedDateTime.now().minus(Duration.ofHours(1)));
        when(activeStream.getConfiguration())
                .thenReturn(StreamConfiguration.builder()
                                    .name("rq_us-west-2_" + TEST_VERSION_ID_3)
                                    .build());

        // mock v4
        when(functionsDeploymentRepository.getByKeyFunctionVersionId(TEST_VERSION_ID_4))
                .thenReturn(Optional.of(FunctionDeploymentEntity.builder()
                                                .key(FunctionDeploymentKey.builder()
                                                             .functionVersionId(TEST_VERSION_ID_4)
                                                             .build())
                                                .deploymentId(TEST_DEPLOYMENT_ID)
                                                .functionId(TEST_FUNCTION_ID)
                                                .ncaId(TEST_NCA_ID)

                                                .createdAt(Instant.now().minus(Duration.ofHours(1)))
                                                .lastUpdatedAt(Instant.now())
                                                .build()));
        when(functionsRepository.getByFunctionVersionId(TEST_VERSION_ID_4))
                .thenReturn(Optional.of(FunctionEntity.builder()
                                                .functionId(TEST_FUNCTION_ID)
                                                .functionVersionId(TEST_VERSION_ID_4)
                                                .functionStatus(FunctionStatus.ERROR)
                                                .ncaId(TEST_NCA_ID)
                                                .functionName(TEST_FUNCTION_NAME)
                                                .inferenceUrl(TEST_INFERENCE_URL.toString())
                                                .apiBodyFormat(ApiBodyFormat.CUSTOM)
                                                .build()));
        var erroredStream = mock(StreamInfo.class);
        when(erroredStream.getCreateTime()).thenReturn(
                ZonedDateTime.now().minus(Duration.ofHours(1)));
        when(erroredStream.getConfiguration())
                .thenReturn(StreamConfiguration.builder()
                                    .name("rq_us-west-2_" + TEST_VERSION_ID_4)
                                    .build());

        // attach mocks
        when(natsResourceService.getStreams()).thenReturn(
                List.of(newStream, oldExtraStream, activeStream, erroredStream));

        when(natsResourceService.deleteStream(anyString())).thenReturn(true);

        // run cleaner
        cleanNatsStreamsTask.run(Duration.ofSeconds(2));

        // verify results
        verify(natsResourceService).deleteStream("rq_us-west-2_" + TEST_VERSION_ID_2);
        verify(natsResourceService, never()).deleteStream("rq_us-west-2_" + TEST_VERSION_ID_1);
        verify(natsResourceService, never()).deleteStream("rq_us-west-2_" + TEST_VERSION_ID_3);
        verify(natsResourceService).deleteStream("rq_us-west-2_" + TEST_VERSION_ID_4);
        verify(newStream, never()).getConfiguration();
        verify(functionsDeploymentRepository, never()).getByKeyFunctionVersionId(
                TEST_VERSION_ID_1);
        verify(functionsRepository).getByFunctionVersionId(TEST_VERSION_ID_4);
    }
}