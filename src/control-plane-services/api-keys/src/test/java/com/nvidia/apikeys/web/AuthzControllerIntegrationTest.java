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
import static com.nvidia.apikeys.TestData.TEST_TIME;
import static com.nvidia.apikeys.TestData.USER_KEY_OWNER_ID_1;
import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpStatus.BAD_REQUEST;
import static org.springframework.http.HttpStatus.OK;
import static org.springframework.http.MediaType.APPLICATION_JSON_VALUE;

import com.nvidia.apikeys.App;
import com.nvidia.apikeys.TestData;
import com.nvidia.apikeys.config.IntegrationTestConfiguration;
import com.nvidia.apikeys.dto.keys.KeyDto;
import com.nvidia.apikeys.utils.TestClock;
import java.time.ZoneId;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.http.HttpEntity;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.test.context.ContextConfiguration;

@ExtendWith(IntegrationTestConfiguration.TestCleanerExtension.class)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {App.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=integrationtest")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class AuthzControllerIntegrationTest extends BaseIntegrationTest {

    @Test
    void runAuthz_shouldReturnValidResponse()
            throws Exception {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        // create key
        HttpHeaders serviceUserHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        String keyRequest1 =
                "{"
                + " \"expires_at\":\"" + TestData.KEY_EXPIRES_AT_1_STRING + "\","
                + " \"description\":\"" + TestData.KEY_DESCRIPTION_1 + "\","
                + " \"audience_service_ids\":[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + " \"authorizations\":" + "{\"policies\":["
                + "    {"
                + "       \"aud\": \"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "       \"policy\": \"test-policy-one\""
                + "    },"
                + "    {"
                + "       \"aud\": \"some-other-service-id-of-no-interest-to-us-\","
                + "       \"policy\": \"test-policy\""
                + "    },"
                + "    {"
                + "       \"aud\": \"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "       \"policy\": \"test-policy-dos\""
                + "    }"
                + "  ]}"
                + "}";
        var result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST, new HttpEntity<>(keyRequest1, serviceUserHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);

        KeyDto keyDto = jsonMapper.readValue(result.getBody(), KeyDto.class);

        // Arrange
        String namespace = "nvcf";
        String policyName = "apikey.allow";
        String requestBody = "{" +
                             "    \"input\": {" +
                             "        \"apiKey\": \"" + keyDto.getValue() + "\"" +
                             "    }" +
                             "}";

        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        HttpEntity<String> entity = new HttpEntity<>(requestBody, headers);

        // Act
        ResponseEntity<String> response = restTemplate.exchange(
                "/v1/namespaces/{namespace}/evaluations/{policy-name}",
                HttpMethod.POST, entity, String.class, namespace, policyName);

        // Assert
        assertThat(response.getStatusCode()).isEqualTo(OK);
        assertJsonBodyEquals(
                """
                        {"namespace":"nvcf","result":{"allowed":true,\
                        "ncaId":"test-nca-id",\
                        "ownerId":"yreDV0J-umh_ZWUVuJ2aBgtFCfvZeRjMw9hF6dvurUs",\
                        "policy":{"aud":"nvidia-cloud-functions-ncp-service-id-aketm",\
                        "policy":"test-policy-one"}\
                        },"rule_name":"apikey.allow"}""", response.getBody());
        assertThat(response.getHeaders().getCacheControl()).contains("max-age=60");

        TestClock.resetToDefaults();
    }

    @Test
    void runAuthz_shouldReturnNegativeResponseIfKeyExpired()
            throws Exception {
        TestClock.setBaseClock(TestClock.fixed(TEST_TIME, ZoneId.systemDefault()));

        // create key
        HttpHeaders serviceUserHeader = addHeaderForServiceAndUser(
                SERVICE_ID_1, USER_KEY_OWNER_ID_1, null);

        String keyRequest1 =
                "{"
                + " \"expires_at\":\"" + TestData.KEY_EXPIRES_AT_1_STRING + "\","
                + " \"description\":\"" + TestData.KEY_DESCRIPTION_1 + "\","
                + " \"audience_service_ids\":[\"nvidia-cloud-functions-ncp-service-id-aketm\"],"
                + " \"authorizations\":" + "{\"policies\":["
                + "    {"
                + "       \"aud\": \"nvidia-cloud-functions-ncp-service-id-aketm\","
                + "       \"policy\": \"test-policy-one\""
                + "    }"
                + "  ]}"
                + "}";
        var result = restTemplate.exchange(
                "/v1/keys", HttpMethod.POST, new HttpEntity<>(keyRequest1, serviceUserHeader),
                String.class);
        assertThat(result.getStatusCode()).isEqualTo(HttpStatus.OK);

        KeyDto keyDto = jsonMapper.readValue(result.getBody(), KeyDto.class);

        // Arrange
        String namespace = "nvcf";
        String policyName = "apikey.allow";
        String requestBody = "{" +
                             "    \"input\": {" +
                             "        \"apiKey\": \"" + keyDto.getValue() + "\"" +
                             "    }" +
                             "}";
        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        HttpEntity<String> entity = new HttpEntity<>(requestBody, headers);

        TestClock.resetToDefaults();

        // Act
        ResponseEntity<String> response = restTemplate.exchange(
                "/v1/namespaces/{namespace}/evaluations/{policy-name}",
                HttpMethod.POST, entity, String.class, namespace, policyName);

        // Assert
        assertThat(response.getStatusCode()).isEqualTo(OK);
        assertJsonBodyEquals(
                "{\"namespace\":\"nvcf\",\"result\":{\"allowed\":false},\"rule_name\":\"apikey.allow\"}",
                response.getBody());
        assertThat(response.getHeaders().getCacheControl()).contains("max-age=60");
    }

    @Test
    void runAuthz_shouldReturnNegativeResponseWithCode200ForInvalidKeys() {
        // Arrange
        String namespace = "nvcf";
        String policyName = "apikey.allow";
        String requestBody =
                "{" +
                "    \"input\": {" +
                "        \"apiKey\": \"nvcfapi-test-non-existing-api-key-value\"" +
                "    }" +
                "}";

        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);
        HttpEntity<String> entity = new HttpEntity<>(requestBody, headers);

        // Act
        ResponseEntity<String> response = restTemplate.exchange(
                "/v1/namespaces/{namespace}/evaluations/{policy-name}",
                HttpMethod.POST, entity, String.class, namespace, policyName);

        // Assert
        assertThat(response.getStatusCode()).isEqualTo(OK);
        assertJsonBodyEquals(
                "{\"namespace\":\"nvcf\",\"result\":{\"allowed\":false},\"rule_name\":\"apikey.allow\"}",
                response.getBody());
        assertThat(response.getHeaders().getCacheControl()).contains("max-age=60");
    }

    @Test
    void runAuthz_shouldReturnErrorForInvalidPolicyName() {
        // Arrange
        String namespace = "nvcf";
        String policyName = "invalid-policy";
        String requestBody = """
                {
                    "input": {
                        "apiKey": "test-api-key"
                    }
                }
                """;

        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(MediaType.APPLICATION_JSON);

        HttpEntity<String> entity = new HttpEntity<>(requestBody, headers);

        // Act
        ResponseEntity<String> response = restTemplate.exchange(
                "/v1/namespaces/{namespace}/evaluations/{policy-name}",
                HttpMethod.POST, entity, String.class, namespace, policyName);

        // Assert
        assertThat(response.getStatusCode()).isEqualTo(BAD_REQUEST);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:bad-request",\
                        "title":"Bad Request",\
                        "status":400,\
                        "detail":"Rule name is not supported: invalid-policy",\
                        "instance":"/v1/namespaces/nvcf/evaluations/invalid-policy"}""", response.getBody());
    }

    @Test
    void runAuthz_shouldReturnErrorForMissingApiKey() {
        // Arrange
        String namespace = "nvcf";
        String policyName = "apikey.allow";
        String requestBody = """
                {
                    "input": {}
                }
                """;

        HttpHeaders headers = new HttpHeaders();
        headers.setContentType(
                org.springframework.http.MediaType.parseMediaType(APPLICATION_JSON_VALUE));

        HttpEntity<String> entity = new HttpEntity<>(requestBody, headers);

        // Act
        ResponseEntity<String> response = restTemplate.exchange(
                "/v1/namespaces/{namespace}/evaluations/{policy-name}",
                HttpMethod.POST, entity, String.class, namespace, policyName);

        // Assert
        assertThat(response.getStatusCode()).isEqualTo(BAD_REQUEST);
        assertJsonBodyEquals(
                """
                        {"type":"urn:nv-boot:problem-details:bad-request",\
                        "title":"Bad Request",\
                        "status":400,\
                        "detail":"Api key is not provided",\
                        "instance":"/v1/namespaces/nvcf/evaluations/apikey.allow"}""", response.getBody());
    }
} 
