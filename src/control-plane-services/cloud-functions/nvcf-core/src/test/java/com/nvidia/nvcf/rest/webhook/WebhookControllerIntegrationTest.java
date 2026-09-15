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
package com.nvidia.nvcf.rest.webhook;

import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_NCA_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_VERSION_ID_2;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpStatus.BAD_REQUEST;
import static org.springframework.http.HttpStatus.FORBIDDEN;
import static org.springframework.http.HttpStatus.OK;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.rest.function.deployment.TestDeploymentService;
import com.nvidia.nvcf.rest.queue.TestQueueService;
import com.nvidia.nvcf.rest.webhook.dto.WebhookRequest;
import com.nvidia.nvcf.rest.webhook.dto.WebhookResponse;
import com.nvidia.nvcf.service.common.TestCommonService;
import com.nvidia.nvcf.service.token.GrpcTokenService;
import java.net.URI;
import java.util.List;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@ExtendWith(MockitoExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class WebhookControllerIntegrationTest {

    @Autowired
    private TestRestTemplate testRestTemplate;
    @Autowired
    private GrpcTokenService grpcTokenService;
    @Autowired
    private TestDeploymentService testDeploymentService;
    @Autowired
    private TestCommonService testCommonService;
    @Autowired
    private TestQueueService testQueueService;

    @BeforeEach
    void beforeEach() {
        testDeploymentService.createTestFunctionEntity(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                       TEST_NCA_ID, TEST_FUNCTION_NAME,
                                                       FunctionStatus.DEPLOYING);
    }

    @AfterEach
    void afterEach() {
        testCommonService.reset();
        testQueueService.clearQueues();
    }


    @Test
    void authenticateWebhookPlugin_Success() {
        var token = grpcTokenService.issueToken(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                GrpcTokenService.NvcfIssuedToken.TokenType.WORKER);
        var request = WebhookRequest.builder()
                .account("Worker")
                .pluginName("webhook")
                .payload(token)
                .build();

        var response = testRestTemplate.postForEntity(
                URI.create("/v2/nvcf/webhook/nats-auth"),
                request,
                WebhookResponse.class
        );

        assertThat(response.getStatusCode()).isEqualTo(OK);
        var permission = WebhookResponse.WebhookPermission.builder()
                .allow(List.of("rq.*.%s.>".formatted(TEST_VERSION_ID_2),
                               "stateful_session.lookup.*.%s.>".formatted(TEST_VERSION_ID_2),
                               "stateful_session.reconnect.>",
                               "llsrq.*.%s.>".formatted(TEST_VERSION_ID_2),
                               "nvcf.cancel.%s".formatted(TEST_VERSION_ID_2),
                               "$JS.API.STREAM.INFO.*",
                               "rq_polling.*",
                               "$JS.API.STREAM.PURGE.*",
                               "$JS.API.CONSUMER.INFO.>",
                               "$JS.API.CONSUMER.MSG.NEXT.*.*",
                               "$JS.ACK.>",
                               "_INBOX.>"))
                .build();
        var expectedResponse = WebhookResponse.builder()
                .userId("worker-" + TEST_VERSION_ID_2)
                .account("Worker")
                .permissions(WebhookResponse.WebhookPermissions.builder()
                                     .publish(permission)
                                     .subscribe(permission)
                                     .response(WebhookResponse.WebhookResponsePermission.builder()
                                                       .maxMsgs(1)
                                                       .build())
                                     .build())
                .ttl(null)
                .build();

        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody()).isEqualTo(expectedResponse);
    }

    @Test
    void authenticateWebhookPlugin_ForbiddenAccount() {
        var token = grpcTokenService.issueToken(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                GrpcTokenService.NvcfIssuedToken.TokenType.WORKER);
        var request = WebhookRequest.builder()
                .account("InvalidAccount")
                .pluginName("webhook")
                .payload(token)
                .build();

        var response = testRestTemplate.postForEntity(
                URI.create("/v2/nvcf/webhook/nats-auth"),
                request,
                String.class
        );
        assertThat(response.getStatusCode()).isEqualTo(FORBIDDEN);
    }

    @Test
    void authenticateWebhookPlugin_InvalidPluginName() {
        var token = grpcTokenService.issueToken(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                GrpcTokenService.NvcfIssuedToken.TokenType.WORKER);
        var request = WebhookRequest.builder()
                .account("Worker")
                .pluginName("invalid-plugin")
                .payload(token)
                .build();

        var response = testRestTemplate.postForEntity(
                URI.create("/v2/nvcf/webhook/nats-auth"),
                request,
                String.class
        );
        assertThat(response.getStatusCode()).isEqualTo(FORBIDDEN);
    }

    @Test
    void authenticateWebhookPlugin_InvalidToken() {
        var request = WebhookRequest.builder()
                .account("Worker")
                .pluginName("webhook")
                .payload("invalid-token")
                .build();
        var response = testRestTemplate.postForEntity(
                URI.create("/v2/nvcf/webhook/nats-auth"),
                request,
                String.class
        );
        assertThat(response.getStatusCode()).isEqualTo(FORBIDDEN);
    }

    @Test
    void authenticateWebhookPlugin_ValidationFailure() {
        var token = grpcTokenService.issueToken(TEST_FUNCTION_ID, TEST_VERSION_ID_2,
                                                GrpcTokenService.NvcfIssuedToken.TokenType.WORKER);
        var emptyAccountRequest = WebhookRequest.builder()
                .account("")
                .pluginName("webhook")
                .payload(token)
                .build();

        var response = testRestTemplate.postForEntity(
                URI.create("/v2/nvcf/webhook/nats-auth"),
                emptyAccountRequest,
                String.class
        );
        assertThat(response.getStatusCode()).isEqualTo(BAD_REQUEST);
    }
} 