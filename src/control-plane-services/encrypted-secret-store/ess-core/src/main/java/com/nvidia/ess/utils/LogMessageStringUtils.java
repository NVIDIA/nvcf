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
package com.nvidia.ess.utils;

import com.nvidia.ess.exceptions.ProblemSummary;
import lombok.experimental.UtilityClass;
import lombok.extern.slf4j.Slf4j;

@Slf4j
@UtilityClass
public class LogMessageStringUtils {

    public static String namespaceEntityTuple(String namespace, String entity) {
        return String.format("(namespace='%s', entity='%s')", namespace, entity);
    }
  
    public static String namespaceEntitySecretTuple(String namespace, String entity, String secretPath) {
        return String.format("(namespace='%s', entity='%s', secretPath='%s')", namespace, entity, secretPath);
    }

    public static ProblemSummary errorSummary(String error, String subError, String namespace, String entity, String secretPath) {
        return ProblemSummary.builder()
                .problemBrief(String.format("%s::%s", error, subError))
                .affectedResource(namespaceEntitySecretTuple(namespace, entity, secretPath))
                .build();
    }

    public static ProblemSummary errorSummary(String error, String subError, String namespace, String entity) {
        return ProblemSummary.builder()
                .problemBrief(String.format("%s::%s", error, subError))
                .affectedResource(namespaceEntityTuple(namespace, entity))
                .build();
    }
}
