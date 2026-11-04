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
package com.nvidia.nvcf.util;

import static com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat.CUSTOM;
import static com.nvidia.nvcf.rest.function.deployment.dto.ValidationPolicyNameEnum.UNRESTRICTED;
import static com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest.GO;
import static com.nvidia.nvcf.util.MockIcmsServer.InstanceState;
import static com.nvidia.nvcf.util.NvcfConstants.DEFAULT_HEALTH_TIMEOUT;
import static com.nvidia.nvcf.util.TestConstants.EXPECTED_STATUS_CODE;
import static com.nvidia.nvcf.util.TestConstants.TEST_CONTAINER_ARGS;
import static com.nvidia.nvcf.util.TestConstants.TEST_DESCRIPTION;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_ID_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME;
import static com.nvidia.nvcf.util.TestConstants.TEST_FUNCTION_NAME_2;
import static com.nvidia.nvcf.util.TestConstants.TEST_HEALTH_URI;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_PORT;
import static com.nvidia.nvcf.util.TestConstants.TEST_INFERENCE_URL;
import static com.nvidia.nvcf.util.TestConstants.TEST_NGC_CONTAINER_IMAGE;
import static com.nvidia.nvcf.util.TestConstants.TEST_TAGS;

import com.nvidia.nvcf.persistence.function.FunctionsRepository;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.HealthUdt;
import com.nvidia.nvcf.persistence.function.entity.Protocol;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.HelmValidationPolicyDto;
import com.nvidia.nvcf.util.MockIcmsServer.IcmsInstancesContext;
import java.time.Duration;
import java.util.List;
import java.util.UUID;
import java.util.function.Supplier;
import lombok.SneakyThrows;
import org.springframework.core.io.ClassPathResource;
import tools.jackson.databind.json.JsonMapper;

public class TestUtil {
    private static final JsonMapper OBJECT_MAPPER = JsonMapper.builder().build();

    @SneakyThrows
    public static byte[] readFileAsBytes(String pathToFile) {
        try (var input = new ClassPathResource(pathToFile).getInputStream()) {
            return input.readAllBytes();
        }
    }

    @SneakyThrows
    public static String readFileAsString(String pathToFile) {
        return new String(readFileAsBytes(pathToFile));
    }

    public static void insertFunctions(
            FunctionsRepository functionsRepository,
            String ncaId,
            FunctionStatus status) {
        var entity1 = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID)
                .functionVersionId(TEST_FUNCTION_ID)
                .ncaId(ncaId)
                .functionName(TEST_FUNCTION_NAME)
                .functionStatus(status)
                .inferenceUrl("v2/models/stable_diffusion_1_5_trt/infer")
                .inferencePort(7777)
                .containerImage(
                        "stg.nvcr.io/nv-cf/c-134557044995160/wombo_txt2img:0.0.4")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .build();
        functionsRepository.save(entity1);

        var entity2 = FunctionEntity.builder()
                .functionId(TEST_FUNCTION_ID_2)
                .functionVersionId(TEST_FUNCTION_ID_2)
                .ncaId(ncaId)
                .functionName(TEST_FUNCTION_NAME_2)
                .functionStatus(status)
                .inferenceUrl("v2/models/stable_diffusion_1_5_trt/infer")
                .inferencePort(7777)
                .containerImage(
                        "stg.nvcr.io/nv-cf/c-134557044995160/wombo_txt2img:0.0.4")
                .apiBodyFormat(ApiBodyFormat.PREDICT_V2)
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .build();
        functionsRepository.save(entity2);
    }

    public static String getToken(Object tokenSupplier) {
        if (tokenSupplier instanceof Supplier<?>) {
            return (String) ((Supplier<?>) tokenSupplier).get();
        }
        return (String) tokenSupplier;
    }

    public static FunctionEntity createFunctionEntity(
            UUID id,
            UUID versionId,
            String ncaId,
            String name) {
        return FunctionEntity.builder()
                .functionId(id)
                .functionVersionId(versionId)
                .functionName(name)
                .functionStatus(FunctionStatus.INACTIVE)
                .ncaId(ncaId)
                .containerArgs(TEST_CONTAINER_ARGS)
                .containerImage(TEST_NGC_CONTAINER_IMAGE.toString())
                .apiBodyFormat(CUSTOM)
                .inferenceUrl(TEST_INFERENCE_URL.toString())
                .utilsContainerImage(GO)
                .tags(TEST_TAGS)
                .description(TEST_DESCRIPTION)
                .health(createHealthUdt())
                .build();
    }

    public static HealthUdt createHealthUdt() {
        return HealthUdt.builder()
                .expectedStatusCode(EXPECTED_STATUS_CODE)
                .timeout(DEFAULT_HEALTH_TIMEOUT)
                .port(TEST_INFERENCE_PORT)
                .protocol(Protocol.HTTP)
                .uri(TEST_HEALTH_URI.toString())
                .build();
    }

    public static AutoscalingConfigurationDto createAutoscalingConfigDto() {
        return createAutoscalingConfigDto("worker_utilization");
    }

    public static AutoscalingConfigurationDto createAutoscalingConfigDto(String metricName) {
        return AutoscalingConfigurationDto.builder()
                .scaleUpDetails(AutoscalingConfigurationDto.ScalingDetails.builder()
                        .metric(metricName)
                        .factor(1.5f)
                        .threshold(80)
                        .stickiness(AutoscalingConfigurationDto.StickinessWindow.builder()
                                .size(Duration.ofMinutes(30))
                                .threshold(Duration.ofMinutes(5))
                                .build())
                        .build())
                .scaleDownDetails(AutoscalingConfigurationDto.ScalingDetails.builder()
                        .metric(metricName)
                        .factor(0.5f)
                        .threshold(20)
                        .stickiness(AutoscalingConfigurationDto.StickinessWindow.builder()
                                .size(Duration.ofMinutes(30))
                                .threshold(Duration.ofMinutes(5))
                                .build())
                        .build())
                .build();
    }

    public static IcmsInstancesContext buildInstancesContext(
            InstanceState state, int instanceCount, UUID gpuSpecId) {
        return IcmsInstancesContext.builder()
                        .instanceState(state)
                        .instanceCount(instanceCount)
                        .gpuSpecId(gpuSpecId).
                        build();
    }

    public static HelmValidationPolicyDto buildHelmValidationPolicyDto() {
        return HelmValidationPolicyDto.builder()
                .name(UNRESTRICTED)
                .extraKubernetesTypes(List.of(
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("apps").version("v1").kind("Deployment").build(),
                        HelmValidationPolicyDto.KubernetesType.builder()
                                .group("infra").version("v1").kind("Service").build()))
                .build();
    }
}
