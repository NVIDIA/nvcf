/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.service.function;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.configuration.llm.LlmRoutingExpressionsProperties;
import com.nvidia.nvcf.rest.function.management.dto.LlmConfigValidator;
import jakarta.annotation.Nullable;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Component;

@Slf4j
@Component
@RequiredArgsConstructor
public class LlmRoutingMethodValidator {

    private static final String MESG_ROUTING_PARAMETERS_DISABLED =
            "Invalid request: 'llmConfig.routingMethod' for model '%s' carries tuning parameters, "
                    + "which are not enabled on this deployment; specify the algorithm name only";

    private final LlmRoutingExpressionsProperties properties;

    public void validate(String modelName, @Nullable String routingMethod) {
        if (StringUtils.isBlank(routingMethod)) {
            return;
        }
        if (!properties.isEnabled() && routingMethod.contains(";")) {
            var mesg = MESG_ROUTING_PARAMETERS_DISABLED.formatted(modelName);
            log.error(mesg);
            throw new BadRequestException(mesg);
        }
        LlmConfigValidator.validateRoutingMethod(modelName, routingMethod);
    }
}
