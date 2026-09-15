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
package com.nvidia.nvcf.service.function;

import static org.apache.commons.lang3.StringUtils.isNotBlank;

import com.google.protobuf.Duration;
import com.nvidia.nvcf.proto.AutoscalingConfiguration;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto.ScalingDetails;
import com.nvidia.nvcf.rest.function.deployment.dto.AutoscalingConfigurationDto.StickinessWindow;
import java.nio.ByteBuffer;
import java.util.Objects;
import lombok.RequiredArgsConstructor;
import lombok.SneakyThrows;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Component;
import tools.jackson.databind.json.JsonMapper;

/**
 * Converts between autoscaling ByteBuffer (legacy proto), JSON string (gpu_specifications table),
 * and DTO (REST). Used so that {@link FunctionMapperService} does not depend on
 * GPU-spec UDTs for the response path; persistence and legacy resolution use this instead.
 */
@Component
@RequiredArgsConstructor
public class AutoscalingConfigurationMapper {

    private final JsonMapper jsonMapper;

    /**
     * Convert DTO to JSON string for persisting in gpu_specifications table.
     */
    @SneakyThrows
    public String toAutoscalingConfigurationJson(AutoscalingConfigurationDto dto) {
        if (dto == null) {
            return null;
        }
        return jsonMapper.writeValueAsString(dto);
    }

    /**
     * Convert JSON string (gpu_specifications table) to DTO.
     */
    @SneakyThrows
    public AutoscalingConfigurationDto toAutoscalingConfigurationDto(String json) {
        if (StringUtils.isBlank(json)) {
            return null;
        }
        return jsonMapper.readValue(json, AutoscalingConfigurationDto.class);
    }

    /**
     * Convert legacy ByteBuffer (proto bytes) to JSON string for persisting in gpu_specifications.
     */
    @SneakyThrows
    public String toAutoscalingConfigurationJson(ByteBuffer buffer) {
        if (buffer == null || !buffer.hasRemaining()) {
            return null;
        }
        var config = AutoscalingConfiguration.parseFrom(buffer);
        return toAutoscalingConfigurationJson(toAutoscalingConfigurationDto(config));
    }

    /**
     * Convert JSON string (gpu_specifications) to gRPC proto.
     */
    public AutoscalingConfiguration toAutoscalingConfiguration(String json) {
        var dto = toAutoscalingConfigurationDto(json);
        if (dto == null) {
            return null;
        }
        return toAutoscalingConfigurationProto(dto);
    }

    /**
     * Convert autoscaling configuration DTO to proto (e.g. for legacy deployment entity).
     */
    public static AutoscalingConfiguration toAutoscalingConfigurationProto(
            AutoscalingConfigurationDto autoscalingConfig) {
        var builder = AutoscalingConfiguration.newBuilder();
        if (Objects.nonNull(autoscalingConfig.scaleUpDetails())) {
            builder.setScaleUpDetails(toScalingDetailsProto(autoscalingConfig.scaleUpDetails()));
        }
        if (Objects.nonNull(autoscalingConfig.scaleDownDetails())) {
            builder.setScaleDownDetails(
                    toScalingDetailsProto(autoscalingConfig.scaleDownDetails()));
        }
        return builder.build();
    }

    private AutoscalingConfigurationDto toAutoscalingConfigurationDto(
            AutoscalingConfiguration config) {
        var builder = AutoscalingConfigurationDto.builder();
        if (config.hasScaleUpDetails()) {
            builder.scaleUpDetails(toScalingDetailsDto(config.getScaleUpDetails()));
        }
        if (config.hasScaleDownDetails()) {
            builder.scaleDownDetails(toScalingDetailsDto(config.getScaleDownDetails()));
        }
        return builder.build();
    }

    private static com.nvidia.nvcf.proto.ScalingDetails toScalingDetailsProto(
            ScalingDetails details) {
        var b = com.nvidia.nvcf.proto.ScalingDetails.newBuilder()
                .setFactor(details.factor())
                .setThreshold(details.threshold());
        if (isNotBlank(details.metric())) {
            b.setMetric(details.metric());
        }
        if (Objects.nonNull(details.stickiness())) {
            b.setStickiness(toStickinessWindowProto(details.stickiness()));
        }
        return b.build();
    }

    private static com.nvidia.nvcf.proto.StickinessWindow toStickinessWindowProto(
            StickinessWindow stickiness) {
        return com.nvidia.nvcf.proto.StickinessWindow.newBuilder()
                .setSize(Duration.newBuilder()
                                 .setSeconds(stickiness.size().getSeconds())
                                 .setNanos(stickiness.size().getNano())
                                 .build())
                .setThreshold(Duration.newBuilder()
                                      .setSeconds(stickiness.threshold().getSeconds())
                                      .setNanos(stickiness.threshold().getNano())
                                      .build())
                .build();
    }

    private static ScalingDetails toScalingDetailsDto(
            com.nvidia.nvcf.proto.ScalingDetails proto) {
        var b = ScalingDetails.builder()
                .factor(proto.getFactor())
                .threshold(proto.getThreshold());
        if (isNotBlank(proto.getMetric())) {
            b.metric(proto.getMetric());
        }
        if (proto.hasStickiness()) {
            b.stickiness(toStickinessWindowDto(proto.getStickiness()));
        }
        return b.build();
    }

    private static StickinessWindow toStickinessWindowDto(
            com.nvidia.nvcf.proto.StickinessWindow proto) {
        return StickinessWindow.builder()
                .size(java.time.Duration.ofSeconds(
                        proto.getSize().getSeconds(),
                        proto.getSize().getNanos()))
                .threshold(java.time.Duration.ofSeconds(
                        proto.getThreshold().getSeconds(),
                        proto.getThreshold().getNanos()))
                .build();
    }
}
