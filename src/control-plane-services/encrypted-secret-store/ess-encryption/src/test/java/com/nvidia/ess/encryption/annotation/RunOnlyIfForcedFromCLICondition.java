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
package com.nvidia.ess.encryption.annotation;

import java.util.Objects;
import org.junit.jupiter.api.extension.ConditionEvaluationResult;
import org.junit.jupiter.api.extension.ExecutionCondition;
import org.junit.jupiter.api.extension.ExtensionContext;

class RunOnlyIfForcedFromCLICondition implements ExecutionCondition {

    private static final String FORCE_RUN_DISABLED_TESTS_CLI = "forceRunDisabledTests";

    @Override
    public ConditionEvaluationResult evaluateExecutionCondition(ExtensionContext context) {

        var prop = System.getProperty(FORCE_RUN_DISABLED_TESTS_CLI);

        if (Objects.isNull(prop)) {
            return ConditionEvaluationResult.disabled("Not running disabled test: " + context.getDisplayName());
        }

        return Boolean.parseBoolean(prop)
                ? ConditionEvaluationResult.enabled("Force-running disabled test: " + context.getDisplayName())
                : ConditionEvaluationResult.disabled(String.format(
                        "Not running disabled test %s as -D%s=%s does not evaluate to true",
                        context.getDisplayName(), FORCE_RUN_DISABLED_TESTS_CLI, prop)
                );
    }
}
