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
package com.nvidia.ess.config;

import com.nvidia.boot.audit.AuditProperties;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

/**
 * Opt-in HMAC signing for audit events emitted by {@code AuditFilter} via
 * {@code com.nvidia.boot.audit.AuditService} (nv-boot-starter-audit 1.10.0+).
 */
@Configuration
@ConditionalOnProperty(name = {"audit.hmac.keys", "audit.hmac.kid"})
public class HmacConfig {

    @Bean
    @RefreshScope
    public AuditProperties auditProperties(
            @Value("${audit.hmac.keys}") String hmacKeys,
            @Value("${audit.hmac.kid}") String hmacKid) {
        var props = new AuditProperties();
        props.setHmacKeys(hmacKeys);
        props.setHmacKid(hmacKid);
        return props;
    }
}
