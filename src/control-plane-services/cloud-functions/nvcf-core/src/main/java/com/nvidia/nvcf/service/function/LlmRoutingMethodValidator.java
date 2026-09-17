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
