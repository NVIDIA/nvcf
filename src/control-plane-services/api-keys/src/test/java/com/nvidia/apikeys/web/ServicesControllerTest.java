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

import static com.nvidia.apikeys.TestData.SERVICE_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpMethod.GET;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.config.IntegrationTestConfiguration.TestCleanerExtension;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

@Slf4j
@ExtendWith(TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
public class ServicesControllerTest extends BaseIntegrationTest {

    @Test
    void shouldAllowListingServices() {
        var result = restTemplate.exchange(
                "/v1/services", GET, HttpEntity.EMPTY, String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @Test
    void shouldAllowReadingService() {
        String serviceOneUrl = "/v1/services/" + SERVICE_ID_1;

        ResponseEntity<String> result =
                restTemplate.exchange(serviceOneUrl, GET, HttpEntity.EMPTY, String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertJsonBodyEquals(
                """
                        {"service_id":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "service_name":"test-service",\
                        "audience_service_ids":["nvidia-cloud-functions-ncp-service-id-aketm"],\
                        "max_api_keys_per_user":8,\
                        "max_api_key_ttl_days":365,\
                        "max_authz_size_chars":2048,\
                        "min_authz_update_interval_seconds":3}""", result.getBody());
    }

    @Test
    void getService_ShouldThrowIfDoesNotExist() {
        ResponseEntity<String> response = restTemplate
                .exchange("/v1/services/non-existing", GET, HttpEntity.EMPTY, String.class);

        assertThat(response.getStatusCode()).isEqualTo(HttpStatus.NOT_FOUND);
        assertJsonBodyEquals(
                "{"
                + "\"type\":\"urn:nv-boot:problem-details:not-found\","
                + "\"title\":\"Not Found\",\"status\":404,"
                + "\"detail\":\"Service 'non-existing' not found\","
                + "\"instance\":\"/v1/services/non-existing\""
                + "}", response.getBody());
    }
}
