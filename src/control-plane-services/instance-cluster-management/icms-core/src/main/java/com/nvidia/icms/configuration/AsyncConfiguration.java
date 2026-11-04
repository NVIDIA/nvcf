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
package com.nvidia.icms.configuration;

import java.util.concurrent.Executor;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Primary;
import org.springframework.scheduling.concurrent.ThreadPoolTaskExecutor;

/**
 * Configuration class for setting up asynchronous execution in ICMS.
 * <p>
 * This class configures the thread pool executor used for handling asynchronous tasks
 * such as event processing through the {@code @Async} annotation. It defines parameters
 * for the thread pool including core size, max size, and queue capacity to optimize
 * performance while managing system resources effectively.
 * </p>
 * <p>
 * The thread pool is used by components like {@code InstanceServiceEventListener} to process
 * events asynchronously without blocking the main request processing flow.
 * </p>
 */
@Configuration
public class AsyncConfiguration {

    /**
     * Creates and configures the thread pool task executor for async operations.
     * <p>
     * This executor is used by Spring to run methods annotated with {@code @Async}.
     * The executor is marked as primary to ensure it's selected when multiple TaskExecutor
     * beans are available in the application context.
     * </p>
     * 
     * @return Configured ThreadPoolTaskExecutor instance
     */
    @Primary
    @Bean(name = "taskExecutor")
    public Executor taskExecutor() {
        ThreadPoolTaskExecutor executor = new ThreadPoolTaskExecutor();
        executor.setCorePoolSize(5);     // Number of threads to keep alive even when idle
        executor.setMaxPoolSize(10);     // Maximum number of threads to allow in the pool
        executor.setQueueCapacity(100);  // Size of queue before blocking new tasks
        executor.setThreadNamePrefix("async-task-");  // Prefix for thread names for easier debugging
        executor.initialize();
        return executor;
    }
}
