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

package com.nvidia.apikeys.web;

import static com.nvidia.apikeys.TestData.INTROSPECTION_REQUEST_1;
import static com.nvidia.apikeys.TestData.KEY_REQUEST_1;
import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.CsvSource;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = {
                "spring.profiles.active=integrationtest",
                "apikeys.v1-key-management-enabled=false"
        })
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class V1KeyManagementDisabledIntegrationTest extends BaseIntegrationTest {

    @ParameterizedTest
    @CsvSource({
            "GET, /v1/keys",
            "POST, /v1/keys",
            "GET, /v1/keys/key-1",
            "DELETE, /v1/keys/key-1",
            "PUT, /v1/keys/key-1/authorizations",
            "GET, /v1/service-admin/users/user-1/keys",
            "GET, /v1/service-admin/users/user-1/keys/key-1",
            "PUT, /v1/service-admin/users/user-1/keys/key-1/status",
            "PUT, /v1/service-admin/users/user-1/suspend",
            "DELETE, /v1/service-admin/users/user-1/keys/key-1",
            "POST, /v1/admin/lookup",
            "GET, /v1/admin/users/user-1",
            "GET, /v1/admin/users/user-1/keys",
            "DELETE, /v1/admin/users/user-1/keys",
            "PUT, /v1/admin/users/user-1/status",
    })
    void v1KeyManagementReturnsGone(HttpMethod method, String path) {
        var headers = addHeaderForServiceAndUser(SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        var result = restTemplate.exchange(
                path, method, new HttpEntity<>(KEY_REQUEST_1, headers), String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.GONE);
        assertThat(result.getBody())
                .contains(V1KeyManagementInterceptor.MESG_V1_KEY_MANAGEMENT_DISABLED);
    }

    @Test
    void servicesStayAvailable() {
        var result = restTemplate.getForEntity("/v1/services", String.class);

        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    void introspectionStaysAvailable() {
        var result = restTemplate.exchange(
                "/v1/introspect", HttpMethod.POST, new HttpEntity<>(INTROSPECTION_REQUEST_1),
                String.class);

        assertThat(result.getStatusCode()).isNotEqualTo(HttpStatus.GONE);
        assertThat(result.getStatusCode().is5xxServerError()).isFalse();
    }
}
