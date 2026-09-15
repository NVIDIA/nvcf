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
package com.nvidia.nvcf.rest.queue;

import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.TestConstants.TEST_CLIENT_SUBJECT;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.configuration.nats.NatsConfiguration.FixedNatsPool;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.proto.WorkerInvokeFunctionRequest;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.invocation.TestInvokeService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.resultregion.ResultRegistrationService;
import io.nats.client.JetStreamApiException;
import io.nats.client.api.StreamConfiguration;
import io.nats.client.api.StreamInfo;
import java.io.IOException;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class TestQueueService {

    public static final UUID TEST_VERSION_ID_DIFF_ACCOUNT = UUID.randomUUID();

    @Autowired
    private TestDeploymentService testDeploymentService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private FunctionLookupService functionLookupService;

    @Autowired
    private FunctionsRepository functionsRepository;

    @Autowired
    private FixedNatsPool fixedNatsPool;
    @Autowired
    private ResultRegistrationService resultRegistrationService;
    @Autowired
    private TestInvokeService testInvokeService;

    public void initializeResourcesAndState() {
        // Create two versions of a function with DEPLOYING status in account TEST_NCA_ID.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME_2,
                                                       FunctionStatus.DEPLOYING);

        // Create a function with DEPLOYING status in different account TEST_NCA_ID_2.
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID_2,
                                                       TEST_VERSION_ID_DIFF_ACCOUNT,
                                                       TEST_NCA_ID_2, TEST_FUNCTION_NAME_2,
                                                       FunctionStatus.DEPLOYING);

        // Setup TEST_NCA_ID as an authorized party to invoke/list TEST_FUNCTION_ID_2 defined
        // in TEST_NCA_ID_2.
        var authorizedParties = Set.of(AuthorizedPartyDto.builder()
                                               .clientId(TEST_CLIENT_SUBJECT)
                                               .ncaId(TEST_NCA_ID).build());
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID_2, TEST_FUNCTION_ID_2,
                                                    Optional.empty(), authorizedParties);

        // Create entries in functions_deployment_v2 table for all the functions.
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                                     TEST_DEPLOYMENT_ID, TEST_NCA_ID);
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                TEST_DEPLOYMENT_ID, TEST_NCA_ID);
        testDeploymentService.createDeploymentEntity(TEST_FUNCTION_ID_2,
                                                     TEST_VERSION_ID_DIFF_ACCOUNT,
                                                     TEST_DEPLOYMENT_ID,
                                                     TEST_NCA_ID_2);

        // Create a function TEST_PUBLIC_FUNCTION_ID_1 in account TEST_NCA_ID.
        testDeploymentService.createTestFunctionEntity(TEST_PUBLIC_FUNCTION_ID_1,
                                                       TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                                       TEST_NCA_ID, TEST_PUBLIC_FUNCTION_NAME_V1,
                                                       FunctionStatus.DEPLOYING);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_NCA_ID, TEST_PUBLIC_FUNCTION_ID_1,
                                                    Optional.empty(), authorizedParties1);
        // Create deployment for public function
        testDeploymentService.createDeploymentEntity(
                TEST_PUBLIC_FUNCTION_ID_1,
                TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                TEST_DEPLOYMENT_ID,
                TEST_NCA_ID);
    }

    public void invokeFunctionsToBuildQueueDepth(int numInvocations, UUID functionId) {
        var functions = functionId != null ?
                functionLookupService.lookupUsingFunctionId(functionId) : null;
        if (Objects.nonNull(functions) && !functions.isEmpty()) {
            functions.forEach(
                    function -> {
                        var id = function.getFunctionId();
                        var versionId = function.getFunctionVersionId();
                        assertThat(id).isEqualTo(functionId);
                        assertThat(function.getFunctionStatus()).isEqualTo(
                                FunctionStatus.DEPLOYING);

                        // Make the function active so that it can be invoked to add messages to its
                        // queue.
                        function.setFunctionStatus(FunctionStatus.ACTIVE);
                        functionsRepository.insert(function);

                        // Invoke the function four times to add messages to the function-specific
                        // request queue.
                        for (int i = 0; i < numInvocations; i++) {
                            addInvocationToQueue(id, versionId);
                        }
                    }
            );
        }
    }

    public void clearQueues() {
        clearNATSQueues();
    }

    @SneakyThrows
    private void clearNATSQueues() {
        var jetStreamManagement = fixedNatsPool.borrowJetStreamManagement();
        var jetStream = fixedNatsPool.borrowJetStream();
        jetStreamManagement.getStreams()
                .stream()
                .map(StreamInfo::getConfiguration)
                .map(StreamConfiguration::getName)
                .map(streamName -> {
                    try {
                        return jetStream.getStreamContext(streamName);
                    } catch (IOException | JetStreamApiException e) {
                        throw new RuntimeException(e);
                    }
                })
                .forEach(s -> {
                    try {
                        s.purge();
                    } catch (IOException | JetStreamApiException e) {
                        throw new RuntimeException(e);
                    }
                });
    }

    public UUID addInvocationToQueue(UUID functionId, UUID versionId) {
        UUID reqId = UUID.randomUUID();
        resultRegistrationService.registerRequest(functionId, versionId, reqId);
        testInvokeService.enqueueFunctionInvocationRequest(
                versionId, WorkerInvokeFunctionRequest.newBuilder()
                        .setRequestId(reqId.toString())
                        .setNcaId(TEST_NCA_ID)
                        .build());
        return reqId;
    }

    public void setupForPositionInQueue(UUID functionId, UUID functionVersionId) {
        // Create a function with DEPLOYING status.
        initializeResourcesAndState();

        var function =
                functionLookupService
                        .lookupUsingFunctionIdAndVersionIdOrThrow(functionId,
                                                                  functionVersionId);
        assertThat(function).isNotNull();

        // Make the function active so that it can be invoked to add messages to its queue.
        function.setFunctionStatus(FunctionStatus.ACTIVE);
        functionsRepository.insert(function);
    }
}
