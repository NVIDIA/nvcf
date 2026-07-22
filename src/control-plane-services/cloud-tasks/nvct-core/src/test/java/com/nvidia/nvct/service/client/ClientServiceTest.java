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
package com.nvidia.nvct.service.client;

import static com.github.tomakehurst.wiremock.client.WireMock.getRequestedFor;
import static com.github.tomakehurst.wiremock.client.WireMock.urlMatching;
import static com.nvidia.nvct.util.TestConstants.TEST_CLIENT_ID;
import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatExceptionOfType;

import com.nvidia.boot.exceptions.NotFoundException;
import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import com.nvidia.nvct.grpc.TestTaskService;
import com.nvidia.nvct.util.MockNvcfServer;
import java.net.URL;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.ContextConfiguration;

@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@Slf4j
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class ClientServiceTest {

    @Autowired
    private ClientService clientService;

    @Autowired
    private TestTaskService testTaskService;


    @Value("${nvct.nvcf.base-url}")
    private URL nvcfBaseUrl;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
        MockNvcfServer.start(nvcfBaseUrl);
    }

    @AfterAll
    void cleanup() {
        MockNvcfServer.stop();
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @AfterEach
    void afterEach() {
        clientService.invalidateCache();
        testTaskService.clearAll();
    }

    @Test
    void shouldGetClient() {
        var dto = clientService.getClient(TEST_CLIENT_ID);
        assertThat(dto).isNotNull();
        assertThat(dto.clientId()).isEqualTo(TEST_CLIENT_ID);
        assertThat(dto.ncaId()).isNotBlank();
        assertThat(dto.name()).isNotBlank();
    }

    @Test
    void shouldFailToGetClient() {
        assertThatExceptionOfType(NotFoundException.class).isThrownBy(
                () -> clientService.getClient("non-existent-client"));
    }

    @Test
    void shouldReturnCachedClientOnSecondCallWithinTtl() {
        MockNvcfServer.getMockNvcfServer().resetRequests();

        clientService.getClient(TEST_CLIENT_ID);
        clientService.getClient(TEST_CLIENT_ID);

        MockNvcfServer.getMockNvcfServer().verify(1,
                getRequestedFor(urlMatching("/v2/nvcf/clients/" + TEST_CLIENT_ID)));
    }

    @Test
    void shouldRefetchClientAfterTtlExpires() throws InterruptedException {
        MockNvcfServer.getMockNvcfServer().resetRequests();

        clientService.getClient(TEST_CLIENT_ID);
        Thread.sleep(3000);
        clientService.getClient(TEST_CLIENT_ID);

        MockNvcfServer.getMockNvcfServer().verify(2,
                getRequestedFor(urlMatching("/v2/nvcf/clients/" + TEST_CLIENT_ID)));
    }

}
