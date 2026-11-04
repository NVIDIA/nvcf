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

package com.nvidia.boot.audit.configuration;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.AuditProperties;
import com.nvidia.boot.audit.AuditService;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.ApplicationEventPublisher;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class AuditConfiguration {

    @Bean
    @ConditionalOnMissingBean(AuditService.class)
    public AuditService auditService(
            ApplicationEventPublisher eventPublisher,
            ObjectProvider<AuditProperties> auditProperties,
            JsonMapper jsonMapper) {
        return new AuditService(eventPublisher, auditProperties, jsonMapper);
    }
}
