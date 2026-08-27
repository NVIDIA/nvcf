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
package com.nvidia.icms.service.extensions;

import com.nvidia.icms.service.extensions.api.CloudHealthEventService;
import com.nvidia.icms.service.extensions.api.ClusterAuthorizationService;
import com.nvidia.icms.service.extensions.api.ClusterRegistrationHandler;
import com.nvidia.icms.service.extensions.api.InstanceCreationService;
import com.nvidia.icms.service.extensions.api.InstanceDescriptionHelper;
import com.nvidia.icms.service.extensions.api.ExpiredInstanceProcessor;
import com.nvidia.icms.service.extensions.api.HeartbeatRecorder;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleService;
import com.nvidia.icms.service.extensions.api.InstanceLifecycleHelper;
import com.nvidia.icms.service.extensions.api.LaunchSpecificationService;
import com.nvidia.icms.service.extensions.api.InstanceDestinationProvider;
import com.nvidia.icms.service.extensions.api.ReservationProcessor;
import com.nvidia.icms.service.extensions.api.ReservedBackupInstanceProcessor;
import com.nvidia.icms.service.extensions.api.InstanceTerminationService;
import com.nvidia.icms.service.extensions.api.UnhealthyInstanceService;
import com.nvidia.icms.service.extensions.impl.NoOpCloudHealthEventService;
import com.nvidia.icms.service.extensions.impl.NoOpClusterAuthorizationService;
import com.nvidia.icms.service.extensions.impl.NoOpClusterRegistrationHandler;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceCreationService;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceDescriptionHelper;
import com.nvidia.icms.service.extensions.impl.NoOpExpiredInstanceProcessor;
import com.nvidia.icms.service.extensions.impl.NoOpHeartbeatRecorder;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceLifecycleService;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceLifecycleHelper;
import com.nvidia.icms.service.extensions.impl.NoOpLaunchSpecificationService;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceDestinationProvider;
import com.nvidia.icms.service.extensions.impl.NoOpReservationProcessor;
import com.nvidia.icms.service.extensions.impl.NoOpReservedBackupInstanceProcessor;
import com.nvidia.icms.service.extensions.impl.NoOpInstanceTerminationService;
import com.nvidia.icms.service.extensions.impl.NoOpUnhealthyInstanceService;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.annotation.Bean;

/**
 * Registers default implementations for the ICMS extension interfaces.
 *
 * <p>Each bean is declared with {@link ConditionalOnMissingBean} so that a real extension
 * implementation takes precedence when present. The conditions live on an auto-configuration
 * class — which is evaluated after the application's component scan — so that they reliably
 * observe component-scanned implementations. Placing
 * {@code @ConditionalOnMissingBean} directly on a component-scanned {@code @Service} is not
 * reliable because the condition is evaluated during scanning in an undefined order.
 */
@AutoConfiguration
public class DefaultExtensionsAutoConfiguration {

    @Bean
    @ConditionalOnMissingBean
    public LaunchSpecificationService defaultLaunchSpecificationService() {
        return new NoOpLaunchSpecificationService();
    }

    @Bean
    @ConditionalOnMissingBean
    public CloudHealthEventService defaultCloudHealthEventService() {
        return new NoOpCloudHealthEventService();
    }

    @Bean
    @ConditionalOnMissingBean
    public ClusterAuthorizationService defaultClusterAuthorizationService() {
        return new NoOpClusterAuthorizationService();
    }

    @Bean
    @ConditionalOnMissingBean
    public ClusterRegistrationHandler defaultClusterRegistrationHandler() {
        return new NoOpClusterRegistrationHandler();
    }

    @Bean
    @ConditionalOnMissingBean
    public InstanceCreationService defaultInstanceCreationService() {
        return new NoOpInstanceCreationService();
    }

    @Bean
    @ConditionalOnMissingBean
    public InstanceDescriptionHelper defaultInstanceDescriptionHelper() {
        return new NoOpInstanceDescriptionHelper();
    }

    @Bean
    @ConditionalOnMissingBean
    public ExpiredInstanceProcessor defaultExpiredInstanceProcessor() {
        return new NoOpExpiredInstanceProcessor();
    }

    @Bean
    @ConditionalOnMissingBean
    public HeartbeatRecorder defaultHeartbeatRecorder() {
        return new NoOpHeartbeatRecorder();
    }

    @Bean
    @ConditionalOnMissingBean
    public InstanceDestinationProvider defaultInstanceDestinationProvider() {
        return new NoOpInstanceDestinationProvider();
    }

    @Bean
    @ConditionalOnMissingBean
    public ReservationProcessor defaultReservationProcessor() {
        return new NoOpReservationProcessor();
    }

    @Bean
    @ConditionalOnMissingBean
    public ReservedBackupInstanceProcessor defaultReservedBackupInstanceProcessor() {
        return new NoOpReservedBackupInstanceProcessor();
    }

    @Bean
    @ConditionalOnMissingBean
    public InstanceLifecycleService defaultInstanceLifecycleService() {
        return new NoOpInstanceLifecycleService();
    }

    @Bean
    @ConditionalOnMissingBean
    public InstanceLifecycleHelper defaultInstanceLifecycleHelper() {
        return new NoOpInstanceLifecycleHelper();
    }

    @Bean
    @ConditionalOnMissingBean
    public InstanceTerminationService defaultInstanceTerminationService() {
        return new NoOpInstanceTerminationService();
    }

    @Bean
    @ConditionalOnMissingBean
    public UnhealthyInstanceService defaultUnhealthyInstanceService() {
        return new NoOpUnhealthyInstanceService();
    }
}
