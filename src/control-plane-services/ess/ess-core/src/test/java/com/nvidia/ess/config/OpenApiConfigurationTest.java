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
package com.nvidia.ess.config;

import static com.nvidia.ess.config.OpenApiConfiguration.PROBLEM_DETAIL_SCHEMA;
import static com.nvidia.ess.constants.Constants.X_ESS_AGENT_ID_HEADER;
import static com.nvidia.ess.constants.Constants.X_ESS_REQUEST_ID_HEADER;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.Mockito.lenient;
import static org.mockito.Mockito.times;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import io.swagger.v3.oas.annotations.enums.ParameterIn;
import io.swagger.v3.oas.models.Components;
import io.swagger.v3.oas.models.OpenAPI;
import io.swagger.v3.oas.models.Operation;
import io.swagger.v3.oas.models.PathItem;
import io.swagger.v3.oas.models.PathItem.HttpMethod;
import io.swagger.v3.oas.models.Paths;
import io.swagger.v3.oas.models.media.IntegerSchema;
import io.swagger.v3.oas.models.media.ObjectSchema;
import io.swagger.v3.oas.models.media.Schema;
import io.swagger.v3.oas.models.media.StringSchema;
import io.swagger.v3.oas.models.servers.Server;
import java.math.BigDecimal;
import java.util.List;
import java.util.Map;
import java.util.stream.Stream;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.test.util.ReflectionTestUtils;

@ExtendWith(MockitoExtension.class)
class OpenApiConfigurationTest {
    @Mock
    private OpenAPI openAPI;

    @Mock
    private PathItem pathItem;

    @Mock
    private Operation operation;

    @Test
    void addOptionalHeaders_shouldAddRequestIdHeader() {
        OpenApiConfiguration configuration = new OpenApiConfiguration();
        Paths paths = new Paths()
                .addPathItem("/test", pathItem);
        when(openAPI.getPaths()).thenReturn(paths);
        when(pathItem.readOperations()).thenReturn(List.of(operation));

        configuration.addOptionalHeaders().customise(openAPI);

        verify(operation).addParametersItem(argThat(parameter ->
                parameter.getName().equals(X_ESS_REQUEST_ID_HEADER) &&
                        parameter.getIn().equals(ParameterIn.HEADER.toString()) &&
                        !parameter.getRequired() &&
                        parameter.getSchema() instanceof StringSchema schema &&
                        schema.getMaxLength() == 200 &&
                        "^[\\s\\S]*$".equals(schema.getPattern())
        ));
    }


    @Test
    void addOptionalHeaders_onSecretGetApi_shouldAddAgentIdHeader() {
        OpenApiConfiguration configuration = new OpenApiConfiguration();
        Paths paths = new Paths()
                .addPathItem("/v1/{entityType}/{entityId}/**", pathItem);
        when(openAPI.getPaths()).thenReturn(paths);
        when(pathItem.readOperations()).thenReturn(List.of(operation));
        when(pathItem.readOperationsMap()).thenReturn(Map.of(HttpMethod.GET, operation));

        configuration.addOptionalHeaders().customise(openAPI);

        verify(operation).addParametersItem(argThat(parameter ->
                parameter.getName().equals(X_ESS_AGENT_ID_HEADER) &&
                        parameter.getIn().equals(ParameterIn.HEADER.toString()) &&
                        !parameter.getRequired() &&
                        parameter.getSchema() instanceof StringSchema schema &&
                        schema.getMaxLength() == 200 &&
                        "^[\\s\\S]*$".equals(schema.getPattern())
        ));
    }

    static Stream<Arguments> pathNameAndOperationHttpMethod() {
        return Stream.of(
                Arguments.of("/v1/{entityType}/{entityId}/**", HttpMethod.DELETE),
                Arguments.of("/v1/{entityType}/{entityId}/**", HttpMethod.PUT),
                Arguments.of("/v1/{entityType}/{entityId}", HttpMethod.GET)
        );
    }

    @ParameterizedTest
    @MethodSource("pathNameAndOperationHttpMethod")
    void addOptionalHeaders_onNonSecretGetApis_shouldNotAddAgentIdHeader(String pathName, HttpMethod httpMethod) {
        OpenApiConfiguration configuration = new OpenApiConfiguration();
        Paths paths = new Paths()
                .addPathItem(pathName, pathItem);
        when(openAPI.getPaths()).thenReturn(paths);
        when(pathItem.readOperations()).thenReturn(List.of(operation));
        lenient().when(pathItem.readOperationsMap()).thenReturn(Map.of(httpMethod, operation));

        configuration.addOptionalHeaders().customise(openAPI);

        verify(operation, times(0)).addParametersItem(argThat(parameter ->
                parameter.getName().equals(X_ESS_AGENT_ID_HEADER) &&
                        parameter.getIn().equals(ParameterIn.HEADER.toString()) &&
                        !parameter.getRequired() &&
                        parameter.getSchema() instanceof StringSchema
        ));
    }

    @Test
    void enhanceProblemDetailSchema_shouldAddOwaspConstraints() {
        OpenApiConfiguration configuration = new OpenApiConfiguration();

        Schema<?> typeSchema = new StringSchema().format("uri");
        Schema<?> titleSchema = new StringSchema();
        Schema<?> statusSchema = new IntegerSchema().format("int32");
        Schema<?> detailSchema = new StringSchema();
        Schema<?> instanceSchema = new StringSchema().format("uri");

        Schema<?> problemDetail = new ObjectSchema()
                .addProperty("type", typeSchema)
                .addProperty("title", titleSchema)
                .addProperty("status", statusSchema)
                .addProperty("detail", detailSchema)
                .addProperty("instance", instanceSchema);

        OpenAPI api = new OpenAPI()
                .components(new Components()
                        .addSchemas(PROBLEM_DETAIL_SCHEMA, problemDetail));

        configuration.enhanceProblemDetailSchema().customise(api);

        assertThat(typeSchema.getMaxLength()).isEqualTo(2048);
        assertThat(typeSchema.getPattern()).isNull();
        assertThat(titleSchema.getMaxLength()).isEqualTo(500);
        assertThat(titleSchema.getPattern()).isEqualTo("^[\\s\\S]*$");
        assertThat(detailSchema.getMaxLength()).isEqualTo(5000);
        assertThat(detailSchema.getPattern()).isEqualTo("^[\\s\\S]*$");
        assertThat(instanceSchema.getMaxLength()).isEqualTo(2048);
        assertThat(instanceSchema.getPattern()).isNull();
        assertThat(statusSchema.getMinimum()).isEqualTo(BigDecimal.valueOf(100));
        assertThat(statusSchema.getMaximum()).isEqualTo(BigDecimal.valueOf(599));
    }

    @Test
    void enhanceProblemDetailSchema_shouldHandleMissingSchema() {
        OpenApiConfiguration configuration = new OpenApiConfiguration();

        OpenAPI api = new OpenAPI()
                .components(new Components()
                        .addSchemas("OtherSchema", new ObjectSchema()));

        configuration.enhanceProblemDetailSchema().customise(api);
        assertThat(api.getComponents().getSchemas()).doesNotContainKey(PROBLEM_DETAIL_SCHEMA);
    }

    @Test
    void customizeServer_shouldAddExtensionAndDescription() {
        OpenApiConfiguration configuration = new OpenApiConfiguration();
        ReflectionTestUtils.setField(configuration, "applicationEnv", "production");
        Server server = new Server().url("https://ess.nvidia.com");
        OpenAPI api = new OpenAPI().addServersItem(server);

        configuration.customizeServer().customise(api);

        assertThat(server.getExtensions())
                .containsEntry("x-internal", true);
        assertThat(server.getDescription()).isEqualTo("Production server");
    }

    @Test
    void customizeServer_shouldUseApplicationEnvForDescription() {
        OpenApiConfiguration configuration = new OpenApiConfiguration();
        ReflectionTestUtils.setField(configuration, "applicationEnv", "staging");
        Server server = new Server().url("https://ess.stg.nvidia.com");
        OpenAPI api = new OpenAPI().addServersItem(server);

        configuration.customizeServer().customise(api);

        assertThat(server.getDescription()).isEqualTo("Staging server");
    }
}
