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
package com.nvidia.nvcf.rest.function.invocation;

import static com.nvidia.nvcf.IntegrationTestConfiguration.MOCK_OAUTH2_TOKEN_SERVER;
import static com.nvidia.nvcf.service.azp.AuthorizedPartiesService.AUTHORIZED_WILDCARD_ACCOUNT;
import static com.nvidia.nvcf.util.TestConstants.MD_KEY_AUTHORIZATION;
import static com.nvidia.nvcf.util.TestConstants.TEST_ACCOUNT_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_AUTHORIZED_NCA_ID_6;
import static com.nvidia.nvcf.util.TestConstants.TEST_DEPLOYMENT_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID_3;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NAME_V1;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_PUBLIC_FUNCTION_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_1;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_3;

import com.nvidia.boot.mock.ngc.MockCasServer;
import com.nvidia.boot.mock.ngc.MockNgcContainerRegistryServer;
import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.IntegrationTestConfiguration.Initializer;
import com.nvidia.nvcf.LocalGrpcPort;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.configuration.nats.NatsConfiguration.NatsProperties;
import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.persistence.function.entity.RateLimitUdt;
import com.nvidia.nvcf.proto.ClientInvokeRequest;
import com.nvidia.nvcf.proto.ClientInvokeResponse;
import com.nvidia.nvcf.proto.InvocationGrpc;
import com.nvidia.nvcf.rest.account.TestAccountService;
import com.nvidia.nvcf.rest.azp.TestAuthorizedPartiesService;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartiesByFunctionDto;
import com.nvidia.nvcf.rest.azp.dto.AuthorizedPartyDto;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.function.FunctionLookupService;
import com.nvidia.nvcf.service.function.FunctionMapperService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import com.nvidia.nvcf.util.MockApiKeysServer;
import com.nvidia.nvcf.util.MockEssServer;
import io.grpc.ManagedChannelBuilder;
import io.grpc.Metadata;
import io.grpc.stub.MetadataUtils;
import io.nats.client.Connection;
import jakarta.annotation.Nullable;
import java.time.Duration;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.test.context.ContextConfiguration;
import tools.jackson.databind.json.JsonMapper;

@TestInstance(Lifecycle.PER_CLASS)
@Slf4j
@AutoConfigureTestRestTemplate
@SpringBootTest(classes = {NvcfTestApp.class,
        IntegrationTestConfiguration.class},
        webEnvironment = WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = Initializer.class)
public class BaseFunctionInvocationTest {

    @Autowired
    protected TestRestTemplate testRestTemplate;

    @Autowired
    private TestDeploymentService testService;

    @Autowired
    protected FunctionLookupService functionLookupService;

    @Autowired
    protected JsonMapper jsonMapper;

    @Autowired
    protected FunctionsRepository functionsRepository;

    @Autowired
    protected FunctionMapperService functionMapperService;

    @Autowired
    private AuthorizedPartiesService authorizedPartiesService;

    @Autowired
    private TestAuthorizedPartiesService testAuthPartiesService;

    @Autowired
    private TestAccountService testAccountService;

    @Autowired
    private TestCommonService testCommonService;

    @Autowired
    private TestQueueService testQueueService;

    @Autowired
    protected GrpcTokenService grpcTokenService;

    @Autowired
    protected NatsProperties natsProperties;

    @Autowired
    protected Connection natsConnection;

    @LocalGrpcPort
    protected int grpcServerPort;

    @Value("${nvcf.request.timeout}")
    protected Duration requestTimeout;

    @Value("${nvcf.api-keys.base-url}")
    private String apiKeysBaseUrl;

    @Value("${nvcf.request.timeout}")
    protected Duration defaultWaitDuration;

    @Value("${nvcf.ess.base-url}")
    private String essBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.hostname}")
    private String casBaseUrl;

    @Value("${nvcf.registries.recognized.helm.ngc.oauth2.base-url}")
    private String authnBaseUrl;

    @Value("${nvcf.registries.recognized.container.ngc.hostname}")
    private String ngcContainerRegistryUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());

        MockEssServer.start(essBaseUrl);
        MockApiKeysServer.start(apiKeysBaseUrl);
        MockCasServer.start(authnBaseUrl, casBaseUrl);
        MockNgcContainerRegistryServer.start(ngcContainerRegistryUrl);

        testAccountService.createDefaultAccountsClientsAndRegistries();
        testAccountService.createAccountWithNoOAuth2Clients(TEST_NCA_ID_3, TEST_ACCOUNT_NAME_3);
    }

    @AfterAll
    void cleanup() {
        testAccountService.cleanupAccountsClientsAndRegistries();
        MockEssServer.stop();
        MockApiKeysServer.stop();
        MockCasServer.stop();
        MockNgcContainerRegistryServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void reset() {
        testCommonService.reset();
        MockApiKeysServer.resetToDefault();
        testAccountService.deleteAccount(TEST_PUBLIC_FUNCTION_NCA_ID);
        testQueueService.clearQueues();
    }

    @BeforeEach
    void setup() {
        // Create functions in different accounts with DEPLOYING status.
        testService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                             TEST_NCA_ID, TEST_FUNCTION_NAME,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                             TEST_NCA_ID_2, TEST_FUNCTION_NAME_2,
                                             FunctionStatus.DEPLOYING);
        testService.createTestFunctionEntity(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                             TEST_NCA_ID_3, TEST_FUNCTION_NAME_3,
                                             FunctionStatus.DEPLOYING);

        // Create entries in functions_deployment_v2 table for the functions.
        testService.createDeploymentEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID);
        testService.createDeploymentEntity(TEST_FUNCTION_ID_2, TEST_VERSION_ID_2,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID_2);
        testService.createDeploymentEntity(TEST_FUNCTION_ID_3, TEST_VERSION_ID_3,
                                           TEST_DEPLOYMENT_ID, TEST_NCA_ID_3);

        // Create authorized parties.
        var authorizedParties = List.of(
                AuthorizedPartyDto.builder()
                        // .clientId(TEST_AUTHORIZED_CLIENT_ID_1)
                        .ncaId(TEST_AUTHORIZED_NCA_ID_1)
                        .build(),
                AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_2).build(),
                AuthorizedPartyDto.builder().ncaId(TEST_AUTHORIZED_NCA_ID_6).build()

        );
        associateAuthParties(TEST_FUNCTION_ID, TEST_NCA_ID, authorizedParties);

        // Create an account for public functions.
        testAccountService.createAccountAndAssociateClients(TEST_PUBLIC_FUNCTION_NCA_ID, null);

        // Create a function TEST_PUBLIC_FUNCTION_ID_1 in account TEST_PUBLIC_FUNCTION_NCA_ID.
        testService.createTestFunctionEntity(TEST_PUBLIC_FUNCTION_ID_1,
                                             TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                             TEST_PUBLIC_FUNCTION_NCA_ID,
                                             TEST_PUBLIC_FUNCTION_NAME_V1,
                                             FunctionStatus.DEPLOYING);

        // Associate AUTHORIZED_WILDCARD_ACCOUNT as a function level authorized party
        // to make all the versions public.
        var authorizedParties1 = Set.of(
                AuthorizedPartyDto.builder().ncaId(AUTHORIZED_WILDCARD_ACCOUNT).build()
        );
        testAuthPartiesService.associateAuthParties(TEST_PUBLIC_FUNCTION_NCA_ID,
                                                    TEST_PUBLIC_FUNCTION_ID_1,
                                                    Optional.empty(),
                                                    authorizedParties1);
        // Create deployment for public function
        testService.createDeploymentEntity(TEST_PUBLIC_FUNCTION_ID_1,
                                           TEST_PUBLIC_FUNCTION_VERSION_ID_1,
                                           TEST_DEPLOYMENT_ID,
                                           TEST_PUBLIC_FUNCTION_NCA_ID);
    }

    private AuthorizedPartiesByFunctionDto associateAuthParties(
            UUID id,
            String ncaId,
            List<AuthorizedPartyDto> authorizedParties) {
        return authorizedPartiesService.createAuthorizedParties(id, Optional.empty(),
                                                                ncaId, authorizedParties);
    }

    protected void setFunctionModels(UUID functionVersionId, List<FunctionModelDto> models) {
        var function = functionLookupService.lookupUsingVersionIdOrThrow(functionVersionId);
        function.setModelSpecs(functionMapperService.toModelSpecs(models));
        functionsRepository.save(function);
    }

    protected FunctionEntity setFunctionType(UUID functionId,
                                             UUID functionVersionId,
                                             FunctionType functionType) {
        var functionEntity = functionLookupService
                .lookupUsingFunctionIdAndVersionId(functionId, functionVersionId);
        if (functionEntity.isEmpty()) {
            return null;
        }
        functionEntity.get().setFunctionType(functionType);
        return functionsRepository.save(functionEntity.get());
    }

    protected FunctionModelDto saveFunctionModel(UUID functionVersionId,
                                                  String modelName,
                                                  List<String> uris,
                                                  String tokenRateLimit) {
        return saveFunctionModel(functionVersionId, modelName, uris, tokenRateLimit, null, null);
    }

    protected FunctionModelDto saveFunctionModel(UUID functionVersionId,
                                                  String modelName,
                                                  List<String> uris,
                                                  String tokenRateLimit,
                                                  @Nullable String tokenizer,
                                                  @Nullable String routingMethod) {
        var llmConfig = FunctionModelDto.LlmConfigDto.builder()
                .uris(uris)
                .tokenRateLimit(tokenRateLimit)
                .tokenizer(tokenizer)
                .routingMethod(routingMethod)
                .build();
        var dto = FunctionModelDto.builder()
                .name(modelName)
                .llmConfig(llmConfig)
                .build();
        var function = functionLookupService.lookupUsingVersionIdOrThrow(functionVersionId);
        var models = functionMapperService.toFunctionModels(function.getModelSpecs());
        models = new java.util.ArrayList<>(models);
        models.removeIf(model -> model.getName().equals(modelName));
        models.add(dto);
        function.setModelSpecs(functionMapperService.toModelSpecs(models));
        functionsRepository.save(function);
        return dto;
    }

    protected FunctionEntity setFunctionRateLimit(UUID functionId, UUID functionVersionId, RateLimitUdt rateLimitUdt) {
        var functionEntity = functionLookupService.lookupUsingFunctionIdAndVersionId(functionId, functionVersionId);
        if (functionEntity.isEmpty()) {
            return null;
        }
        var entity = functionEntity.get();
        
        entity.setRateLimit(rateLimitUdt);
        
        return functionsRepository.save(entity);
    }

    protected FunctionEntity setFunctionActive(UUID functionId, UUID functionVersionId) {
        return setFunctionStatus(functionId, functionVersionId, FunctionStatus.ACTIVE);
    }

    protected FunctionEntity setFunctionStatus(
            UUID functionId, UUID functionVersionId, FunctionStatus status) {
        var functionEntity = functionLookupService
                .lookupUsingFunctionIdAndVersionId(functionId, functionVersionId);
        if (functionEntity.isEmpty()) {
            return null;
        }
        functionEntity.get().setFunctionStatus(status);
        return functionsRepository.insert(functionEntity.get());
    }

    protected ClientInvokeResponse functionAdminAuth(String adminAuth, String functionId,
                                                     @Nullable String versionId) {
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("invocation:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var reactorInvocationStub = InvocationGrpc.newBlockingStub(channel);
        var requestBuilder = ClientInvokeRequest.newBuilder()
                .setFunctionId(functionId);
        if (versionId != null) {
            requestBuilder.setFunctionVersionId(versionId);
        }
        var proxyInvokeRequest = requestBuilder
                .setClientAuthorizationToken(adminAuth)
                .setTargetNcaId(TEST_NCA_ID)
                .build();
        var clientInvokeResponse = reactorInvocationStub.authClientInvocation(proxyInvokeRequest);
        channel.shutdownNow();
        return clientInvokeResponse;
    }

    protected ClientInvokeResponse functionAuth(String clientAuth, String functionId,
                                                @Nullable String versionId) {
        var md = new Metadata();
        var jwt = MOCK_OAUTH2_TOKEN_SERVER.getJwt("invocation:check_invocation");
        md.put(MD_KEY_AUTHORIZATION, "Bearer " + jwt);
        var channel = ManagedChannelBuilder
                .forAddress("localhost", grpcServerPort)
                .usePlaintext()
                .intercept(MetadataUtils.newAttachHeadersInterceptor(md))
                .build();
        var reactorInvocationStub = InvocationGrpc.newBlockingStub(channel);
        var requestBuilder = ClientInvokeRequest.newBuilder().setFunctionId(functionId);
        if (versionId != null) {
            requestBuilder.setFunctionVersionId(versionId);
        }
        var proxyInvokeRequest = requestBuilder
                .setClientAuthorizationToken(clientAuth)
                .build();
        var clientInvokeResponse = reactorInvocationStub.authClientInvocation(proxyInvokeRequest);
        channel.shutdownNow();
        return clientInvokeResponse;
    }
}
