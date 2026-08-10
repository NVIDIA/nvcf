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

package com.nvidia.boot.core;

import com.nvidia.boot.core.cors.ServletCoreCorsConfiguration;
import com.nvidia.boot.core.cors.ReactiveCoreCorsConfiguration;
import com.nvidia.boot.core.health.HealthConfiguration;
import com.nvidia.boot.core.openapi.OpenApiConfiguration;
import com.nvidia.boot.core.openapi.ServletOpenApiCorsConfiguration;
import com.nvidia.boot.core.openapi.ReactiveOpenApiCorsConfiguration;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.context.annotation.Import;

@AutoConfiguration
@Import({
        HealthConfiguration.class,
        OpenApiConfiguration.class,
        ReactiveCoreCorsConfiguration.class,
        ReactiveOpenApiCorsConfiguration.class,
        ServletCoreCorsConfiguration.class,
        ServletOpenApiCorsConfiguration.class
})
public class CoreAutoConfiguration {
}
