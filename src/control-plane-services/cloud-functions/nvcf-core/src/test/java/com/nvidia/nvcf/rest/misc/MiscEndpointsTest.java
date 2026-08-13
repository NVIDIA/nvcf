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
package com.nvidia.nvcf.rest.misc;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.http.HttpHeaders.ACCESS_CONTROL_REQUEST_HEADERS;
import static org.springframework.http.HttpHeaders.ACCESS_CONTROL_REQUEST_METHOD;
import static org.springframework.http.HttpHeaders.ORIGIN;
import static org.springframework.http.HttpMethod.POST;

import com.nvidia.nvcf.IntegrationTestConfiguration;
import com.nvidia.nvcf.NvcfTestApp;
import com.nvidia.nvcf.util.MockNvcfServer;
import java.net.URI;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.stream.Stream;
import lombok.SneakyThrows;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.Disabled;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestInstance;
import org.junit.jupiter.api.TestInstance.Lifecycle;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.resttestclient.TestRestTemplate;
import org.springframework.boot.resttestclient.autoconfigure.AutoConfigureTestRestTemplate;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.web.server.LocalServerPort;
import org.springframework.http.HttpStatus;
import org.springframework.http.RequestEntity;
import org.springframework.test.context.ContextConfiguration;
import org.springframework.web.cors.CorsConfiguration;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;

@Slf4j
@TestInstance(Lifecycle.PER_CLASS)
@AutoConfigureTestRestTemplate
@SpringBootTest(
        classes = {NvcfTestApp.class, IntegrationTestConfiguration.class},
        webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
        properties = "spring.profiles.active=test")
@ContextConfiguration(initializers = IntegrationTestConfiguration.Initializer.class)
class MiscEndpointsTest {

    private static final JsonMapper JSON_MAPPER = JsonMapper.builder().build();
    private static final Set<String> OBJECT_COMPONENT_SCHEMAS = Set.of(
            "LlmConfigUpdateDto",
            "LlmInvocationConfigDto",
            "PriorityDto",
            "RateLimitDto",
            "HealthDto",
            "LlmConfigDto",
            "TelemetriesDto",
            "AutoscalingConfigurationDto",
            "ScalingDetails",
            "StickinessWindow",
            "DeploymentResponse",
            "FunctionDeploymentDto",
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

    @LocalServerPort
    private int port;

    @SneakyThrows
    @Test
    void testHealth() {
        MockNvcfServer.start(URI.create("http://localhost:8080").toURL());
        var requestEntity = RequestEntity.get(URI.create("/health")).build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        MockNvcfServer.stop();
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
        assertThat(schema(spec, "DeploymentResponse").path("properties").has("deployment")).isTrue();
        assertThat(schema(spec, "FunctionDeploymentDto").path("properties")
                .has("deploymentSpecifications")).isTrue();
        assertThat(schema(spec, "GpuSpecificationDto").path("properties").has("gpu")).isTrue();
        var secretSchema = schema(spec, "SecretDto");
        assertThat(secretSchema.at("/properties/value").has("$ref")).isFalse();
        assertThat(jsonStringValues(secretSchema.path("required")))
                .containsExactlyInAnyOrder("name", "value");
        assertThat(jsonStringValues(secretSchema.at("/properties/value/type")))
                .containsExactly("string", "object");

        var invokeOperation = spec.path("paths")
                .path("/v2/nvcf/pexec/functions/{functionId}/versions/{versionId}")
                .path("post");
        assertThat(parameterNames(invokeOperation)).doesNotContain("headers");
        assertPathParameterIsNonNullable(invokeOperation, "versionId");

        var queueOperation = spec.path("paths")
                .path("/v2/nvcf/queues/functions/{functionId}/versions/{versionId}")
                .path("get");
        assertPathParameterIsNonNullable(queueOperation, "versionId");

        var tokenOperation = spec.path("paths")
                .path("/v2/nvcf/tokens/functions/{functionId}/versions/{functionVersionId}")
                .path("post");
        assertPathParameterIsNonNullable(tokenOperation, "functionVersionId");
    }

    @Test
    @Disabled("requires turning tracing on which breaks CI")
    void testMetrics() {
        var uri = URI.create("http://localhost:9464/metrics");
        var requestEntity = RequestEntity.get(uri).build();
        var responseEntity = testRestTemplate.exchange(requestEntity, String.class);
        var responseBody = responseEntity.getBody();
        assertThat(responseEntity.getStatusCode()).isEqualTo(HttpStatus.OK);
        assertThat(responseBody).isNotNull();
    }

    @ParameterizedTest
    @MethodSource("getRecognizedOrigins")
    void testCorsPreflightRequest(String origin) {
        var requestEntity = RequestEntity.options(URI.create("/v2/nvcf/functions"))
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
                         "https://demo.stg.nvcf.nvidia.com",
                         "https://picasso.nvcf.nvidia.com",
                         "https://picasso.stg.nvcf.nvidia.com",
                         "foo.bar.baz",
                         "*");
    }

    private static JsonNode schema(JsonNode spec, String schemaName) {
        return spec.path("components").path("schemas").path(schemaName);
    }

    private static List<String> parameterNames(JsonNode operation) {
        return jsonStringValues(operation.path("parameters"), "name");
    }

    private static List<String> jsonStringValues(JsonNode arrayNode) {
        var names = new ArrayList<String>();
        for (var node : arrayNode) {
            names.add(node.asString());
        }
        return names;
    }

    private static List<String> jsonStringValues(JsonNode arrayNode, String propertyName) {
        var names = new ArrayList<String>();
        for (var node : arrayNode) {
            names.add(node.path(propertyName).asString());
        }
        return names;
    }

    private static void assertPathParameterIsNonNullable(JsonNode operation, String name) {
        var parameter = pathParameter(operation, name);
        assertThat(parameter.path("required").booleanValue()).isTrue();
        var schema = parameter.path("schema");
        assertThat(schema.path("type").asString()).isEqualTo("string");
        assertThat(schema.path("format").asString()).isEqualTo("uuid");
    }

    private static JsonNode pathParameter(JsonNode operation, String name) {
        for (var parameter : operation.path("parameters")) {
            if ("path".equals(parameter.path("in").asString())
                    && name.equals(parameter.path("name").asString())) {
                return parameter;
            }
        }
        throw new AssertionError("Missing path parameter: " + name);
    }
}
