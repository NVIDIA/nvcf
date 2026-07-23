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
package com.nvidia.nvct.rest.misc;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpHeaders.ACCESS_CONTROL_REQUEST_HEADERS;
import static org.springframework.http.HttpHeaders.ACCESS_CONTROL_REQUEST_METHOD;
import static org.springframework.http.HttpHeaders.ORIGIN;
import static org.springframework.http.HttpMethod.POST;

import com.nvidia.nvct.IntegrationTestConfiguration;
import com.nvidia.nvct.NvctTestApp;
import java.net.URI;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.cors.CorsConfiguration;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@TestInstance(Lifecycle.PER_CLASS)
@SpringBootTest(
        classes = {NvctTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
@AutoConfigureTestRestTemplate
class MiscEndpointsTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();
    private static final Set<String> OBJECT_COMPONENT_SCHEMAS = Set.of(
            "CreateTaskRequest",
            "HealthDto",
            "HelmValidationPolicyDto",
            "KubernetesType",
            "TelemetriesDto",
            "ResultDto",
            "GpuSpecificationDto");

    @Autowired
    private TestRestTemplate testRestTemplate;

    @BeforeAll
    void beforeAll() {
        log.info("{}: Started running tests", this.getClass().getSimpleName());
    }

    @AfterAll
    void cleanup() {
        log.info("{}: Completed running tests", this.getClass().getSimpleName());
    }

    @SneakyThrows
    @Test
    void testHealth() {
        var requestEntity = RequestEntity.get(URI.create("/health")).build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
    }

    @SneakyThrows
    @Test
    void testOpenApiDocs() {
        var requestEntity = RequestEntity.get(URI.create("/v3/openapi")).build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        var responseBody = responseEntity.getBody();
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(responseBody).isNotNull();

        var spec = JSON_MAPPER.readTree(responseBody);
        var componentSchemas = spec.at("/components/schemas");
        var nullOnlySchemas = componentSchemas.properties().stream()
                .filter(entry -> entry.getValue().path("type").isString())
                .filter(entry -> "null".equals(entry.getValue().path("type").asString()))
                .map(entry -> entry.getKey())
                .toList();
        assertThat(nullOnlySchemas).isEmpty();

        OBJECT_COMPONENT_SCHEMAS.forEach(schemaName ->
                assertThat(schema(spec, schemaName).path("type").asString()).isEqualTo("object"));
        assertThat(componentSchemas.has("JsonNode")).isFalse();
        assertThat(schema(spec, "CreateTaskRequest").path("properties")
                .has("gpuSpecification")).isTrue();
        assertThat(schema(spec, "GpuSpecificationDto").path("properties").has("gpu")).isTrue();
        assertThat(schema(spec, "GpuSpecificationDto").at("/properties/configuration/type")
                .asString()).isEqualTo("object");
        var secretSchema = schema(spec, "SecretDto");
        assertThat(secretSchema.at("/properties/value").has("$ref")).isFalse();
        assertThat(jsonStringValues(secretSchema.path("required")))
                .containsExactlyInAnyOrder("name", "value");
        assertThat(jsonStringValues(secretSchema.at("/properties/value/type")))
                .containsExactly("string", "object");
    }

    @ParameterizedTest
    @MethodSource("getRecognizedOrigins")
    void testCorsPreflightRequest(String origin) {
        var requestEntity = RequestEntity.options(URI.create("/v1/nvct/tasks"))
                .header(ORIGIN, origin)
                .header(ACCESS_CONTROL_REQUEST_METHOD, POST.toString())
                .build();
        var responseEntity = testRestTemplate.exchange(requestEntity, Void.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(responseEntity.getHeaders().getVary()).contains(ORIGIN);
        assertThat(responseEntity.getHeaders().getVary()).contains(ACCESS_CONTROL_REQUEST_METHOD);
        assertThat(responseEntity.getHeaders().getVary()).contains(ACCESS_CONTROL_REQUEST_HEADERS);
        assertThat(responseEntity.getHeaders().getAccessControlAllowOrigin()).contains(origin);
        assertThat(responseEntity.getHeaders().getAccessControlAllowCredentials()).isTrue();
        assertThat(responseEntity.getHeaders().getAccessControlAllowMethods()).contains(POST);
        assertThat(responseEntity.getHeaders().getAccessControlExposeHeaders())
                .contains(CorsConfiguration.ALL);
        assertThat(responseEntity.getHeaders().getAccessControlMaxAge()).isEqualTo(86400); // 1d
    }

    private static Stream<String> getRecognizedOrigins() {
        return Stream.of("http://localhost:3000",
                         "https://demo.stg.nvct.nvidia.com",
                         "https://picasso.nvct.nvidia.com",
                         "https://picasso.stg.nvct.nvidia.com",
                         "foo.bar.baz",
                         "*");
    }

    private static JsonNode schema(JsonNode spec, String schemaName) {
        return spec.path("components").path("schemas").path(schemaName);
    }

    private static List<String> jsonStringValues(JsonNode arrayNode) {
        var values = new ArrayList<String>();
        for (var node : arrayNode) {
            values.add(node.asString());
        }
        return values;
    }
}
