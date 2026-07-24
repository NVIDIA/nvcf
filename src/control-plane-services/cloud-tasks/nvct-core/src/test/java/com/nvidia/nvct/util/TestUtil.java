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
package com.nvidia.nvct.util;

import static com.nvidia.nvct.rest.task.dto.ValidationPolicyNameEnum.UNRESTRICTED;
import static com.nvidia.nvct.util.NvctConstants.SCOPE_LIST_TASKS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_ENVIRONMENT;
import static com.nvidia.nvct.util.TestConstants.TEST_CONTAINER_IMAGE;
import static com.nvidia.nvct.util.TestConstants.TEST_MODELS;
import static com.nvidia.nvct.util.TestConstants.TEST_RESOURCES;
import static com.nvidia.nvct.util.TestConstants.TEST_RESULTS_LOCATION_1;

import com.nimbusds.jwt.JWTParser;
import com.nvidia.nvct.persistence.task.entity.GpuSpecUdt;
import com.nvidia.nvct.persistence.task.entity.ResultHandlingStrategy;
import com.nvidia.nvct.persistence.task.entity.TaskEntity;
import com.nvidia.nvct.persistence.task.entity.TaskStatus;
import com.nvidia.nvct.rest.task.dto.HealthDto;
import com.nvidia.nvct.rest.task.dto.HelmValidationPolicyDto;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.List;
import java.util.Optional;
import java.util.Set;
import java.util.UUID;
import java.util.function.Supplier;
import lombok.SneakyThrows;
import org.apache.commons.lang3.StringUtils;
import org.springframework.core.io.ClassPathResource;
import org.testcontainers.containers.ContainerState;
import tools.jackson.databind.json.JsonMapper;

public class TestUtil {

    @SneakyThrows
    public static byte[] readFileAsBytes(String pathToFile) {
        return new ClassPathResource(pathToFile).getContentAsByteArray();
    }

    @SneakyThrows
    public static String readFileAsString(String pathToFile) {
        return new String(readFileAsBytes(pathToFile));
    }

    public static String getContainerUrl(ContainerState containerState, int port) {
        return "http://" + containerState.getHost() + ":" + containerState.getMappedPort(port);
    }


    @SneakyThrows
    public static boolean isListTasksScopeInToken(String token) {
        if (StringUtils.isBlank(token) || token.startsWith("nvapi")) {
            return false;
        }

        var jwt = JWTParser.parse(token);
        var scopes = (List<String>) jwt.getJWTClaimsSet().getClaim("scopes");
        return scopes.stream().anyMatch(scope -> scope.endsWith(SCOPE_LIST_TASKS));
    }

    public static String getToken(Object tokenSupplier) {
        if (tokenSupplier instanceof Supplier<?>) {
            return (String) ((Supplier<?>) tokenSupplier).get();
        }
        return (String) tokenSupplier;
    }

    @SneakyThrows
    public static TaskEntity createContainerBasedTaskEntity(
            UUID id,
            String ncaId,
            String name,
            TaskStatus status,
            JsonMapper jsonMapper) {
        if (status == null) {
            status = TaskStatus.QUEUED;
        }
        return TaskEntity.builder()
                .taskId(id)
                .ncaId(ncaId)
                .name(name)
                .status(status)
                .percentComplete(80)
                .health(testHealth())
                .createdAt(Instant.now())
                .lastUpdatedAt(Instant.now().plusSeconds(5))
                .lastHeartbeatAt(Instant.now().plusSeconds(10))
                .description("task-description")
                .tags(Set.of("tag1", "tag2"))
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_CONTAINER_IMAGE.toString())
                .containerEnvironment(
                        Base64.getEncoder().encodeToString(
                                jsonMapper.writeValueAsBytes(TEST_CONTAINER_ENVIRONMENT)))
                .models(TEST_MODELS)
                .resources(TEST_RESOURCES)
                .maxRuntimeDuration(Duration.parse("PT7H"))
                .maxQueuedDuration(Duration.parse("PT24H"))
                .terminalGracePeriodDuration(Duration.parse("PT1H"))
                .gpuSpec(GpuSpecUdt.builder()
                                 .backend("GFN").gpu("T10").instanceType("g6.full")
                                 .clusters(Set.of("cluster01", "cluster02"))
                                 .helmValidationPolicy(
                                         jsonMapper.writeValueAsString(buildHelmValidationPolicyDto()))
                                 .build())
                .resultHandlingStrategy(ResultHandlingStrategy.UPLOAD)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .build();
    }

    public static TaskEntity createTaskEntity(
            UUID id,
            String ncaId,
            String name,
            JsonMapper jsonMapper) {
        return createContainerBasedTaskEntity(id, ncaId, name, null, jsonMapper);
    }

    @SneakyThrows
    public static TaskEntity createHelmBasedTaskEntity(
            UUID id,
            String ncaId,
            String name,
            JsonMapper jsonMapper) {
        return TaskEntity.builder()
                .taskId(id)
                .ncaId(ncaId)
                .name(name)
                .status(TaskStatus.QUEUED)
                .percentComplete(80)
                .health(testHealth())
                .createdAt(Instant.now())
                .lastUpdatedAt(Instant.now().plusSeconds(5))
                .lastHeartbeatAt(Instant.now().plusSeconds(10))
                .description("task-description")
                .tags(Set.of("tag1", "tag2"))
                .helmChart(TestConstants.TEST_HELM_CHART.toString())
                .containerEnvironment(
                        Base64.getEncoder().encodeToString(
                                jsonMapper.writeValueAsBytes(TEST_CONTAINER_ENVIRONMENT)))
                .models(TEST_MODELS)
                .resources(TEST_RESOURCES)
                .maxRuntimeDuration(Duration.parse("PT7H"))
                .maxQueuedDuration(Duration.parse("PT24H"))
                .terminalGracePeriodDuration(Duration.parse("PT1H"))
                .gpuSpec(GpuSpecUdt.builder()
                                 .backend("GFN").gpu("T10").instanceType("g6.full")
                                 .clusters(Set.of("cluster01", "cluster02"))
                                 .helmValidationPolicy(
                                         jsonMapper.writeValueAsString(buildHelmValidationPolicyDto()))
                                 .build())
                .resultHandlingStrategy(ResultHandlingStrategy.UPLOAD)
                .resultsLocation(TEST_RESULTS_LOCATION_1)
                .build();
    }

    public static HelmValidationPolicyDto buildHelmValidationPolicyDto() {
        return HelmValidationPolicyDto.builder()
                .name(UNRESTRICTED)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("apps").version("v1").kind("Deployment").build(),
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("").version("v1").kind("Service").build()))
                .build();
    }

    @SneakyThrows
    private static String testHealth() {
        return Optional.of(new JsonMapper().writeValueAsString(HealthDto.builder()
                                                                 .backend("GFN")
                                                                 .gpu("T10")
                                                                 .instanceType("g6.full")
                                                                 .error("error-message")
                                                                 .build()))
                .orElseThrow();
    }
}
