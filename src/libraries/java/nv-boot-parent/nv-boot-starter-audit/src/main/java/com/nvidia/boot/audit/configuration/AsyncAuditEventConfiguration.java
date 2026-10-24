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

import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.event.ApplicationEventMulticaster;
import org.springframework.context.event.SimpleApplicationEventMulticaster;

/**
 * Registers ApplicationEventMulticaster with a single-thread executor so audit events
 * (and other Spring events) are dispatched asynchronously, avoiding blocking of the
 * calling thread. Uses @ConditionalOnMissingBean if the app or other internal libraries
 * register its own.
 */
@Configuration
@ConditionalOnMissingBean(ApplicationEventMulticaster.class)
public class AsyncAuditEventConfiguration {

    @Bean(destroyMethod = "shutdown")
    public ExecutorService auditEventExecutor() {
        return Executors.newSingleThreadExecutor();
    }

    @Bean(name = "applicationEventMulticaster")
    public ApplicationEventMulticaster applicationEventMulticaster(
            ExecutorService auditEventExecutor) {
        var multicaster = new SimpleApplicationEventMulticaster();
        multicaster.setTaskExecutor(auditEventExecutor);
        return multicaster;
    }}
