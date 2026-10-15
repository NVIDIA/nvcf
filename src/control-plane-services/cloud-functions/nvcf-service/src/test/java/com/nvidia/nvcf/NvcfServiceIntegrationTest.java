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
package com.nvidia.nvcf;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.nvcf.util.MockNvcfServer;
import java.net.URI;
import lombok.SneakyThrows;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.client.RestTemplate;

/**
 * OSS executable ({@link App} + {@code nvcf-core}) smoke tests. Parallels the managed
 * {@code nvcf-service} repo integration tests, except this module has no
 * {@code NvBootConfiguration} / {@code AuditProperties} wiring.
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class NvcfServiceIntegrationTest {

    @Autowired
    private TestRestTemplate testRestTemplate;

    @LocalManagementPort
    private int managementPort;

    @SneakyThrows
    @BeforeAll
    void beforeAll() {
        MockNvcfServer.start(URI.create("http://localhost:8080").toURL());
    }

    @AfterAll
    void afterAll() {
        MockNvcfServer.stop();
    }

    @SneakyThrows
    @Test
    void healthEndpointReturnsOk() {
        var response = testRestTemplate.exchange(
                RequestEntity.get(URI.create("/health")).build(),
                String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @SneakyThrows
    @Test
    void actuatorHealthReturnsOk() {
        var endpoint = URI.create("http://localhost:" + managementPort + "/actuator/health");
        var response = new RestTemplate().exchange(RequestEntity.get(endpoint).build(), String.class);
        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(response.getBody()).isNotNull();
        assertThat(response.getBody()).containsIgnoringCase("status");
    }
}
