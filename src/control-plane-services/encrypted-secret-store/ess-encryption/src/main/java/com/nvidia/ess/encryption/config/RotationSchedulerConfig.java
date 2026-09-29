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
package com.nvidia.ess.encryption.config;

import com.nvidia.ess.encryption.scheduled.KeyRotatorScheduledService;
import com.nvidia.ess.encryption.scheduled.ReactiveKeyRotatorScheduledService;
import com.nvidia.ess.encryption.scheduled.ServletKeyRotatorScheduledService;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication;
import org.springframework.boot.autoconfigure.condition.ConditionalOnWebApplication.Type;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.scheduling.annotation.EnableScheduling;

@Configuration
@EnableScheduling
@ConditionalOnProperty(value = "encryption.rotation.scheduled.enabled", matchIfMissing = true)
public class RotationSchedulerConfig {

    @Bean
    @ConditionalOnWebApplication(type = Type.REACTIVE)
    public KeyRotatorScheduledService reactiveKeyRotatorScheduledService() {
        return new ReactiveKeyRotatorScheduledService();
    }

    @Bean
    @ConditionalOnMissingBean
    public KeyRotatorScheduledService servletKeyRotatorScheduledService() {
        return new ServletKeyRotatorScheduledService();
    }
}
