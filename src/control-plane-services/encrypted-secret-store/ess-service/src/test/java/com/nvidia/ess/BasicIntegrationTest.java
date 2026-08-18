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
package com.nvidia.ess;

import com.nvidia.ess.testing.CassandraContainerTest;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.actuate.autoconfigure.endpoint.web.WebEndpointProperties;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.SpringBootTest.WebEnvironment;
import org.springframework.boot.test.web.server.LocalManagementPort;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.util.StringUtils;

@Slf4j
@SpringBootTest(classes = EssServiceApplication.class, webEnvironment = WebEnvironment.RANDOM_PORT,
    properties = {
        "spring.profiles.active=integration-test",
    })
@ContextConfiguration
@CassandraContainerTest
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class BasicIntegrationTest {


    @Autowired
    WebEndpointProperties managementEndpointProperties;
    @LocalManagementPort
    private int managementPort;

    WebTestClient managementWebTestClient;

    @BeforeEach
    void setUpManagementClient() {
        managementWebTestClient = WebTestClient
            .bindToServer()
            .baseUrl("http://localhost:" + managementPort)
            .build();
    }

    @Test
    void infoEndpoint_shouldReturnOk() {
        managementWebTestClient.get()
            .uri(StringUtils.trimTrailingCharacter(managementEndpointProperties.getBasePath(), '/')
                + "/info")
            .exchange()
            .expectStatus().isOk()
            // Drain the body so Reactor-Netty releases the client-side ByteBuf (avoids a
            // GC-detected false positive from the Netty ByteBuf leak detector).
            .expectBody();
    }

    @Test
    void healthEndpoint_shouldReturnOk() {
        managementWebTestClient.get()
            .uri(StringUtils.trimTrailingCharacter(managementEndpointProperties.getBasePath(), '/')
                + "/health")
            .exchange()
            .expectStatus().isOk()
            // Drain the body so Reactor-Netty releases the client-side ByteBuf (avoids a
            // GC-detected false positive from the Netty ByteBuf leak detector).
            .expectBody();
    }

}
