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

package com.nvidia.boot.migration.notification;

import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.boot.migration.notification.service.DataMigrationNotificationProperties;
import com.nvidia.boot.migration.notification.service.DataMigrationNotificationService;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.SpringBootConfiguration;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.context.annotation.Import;
import tools.jackson.databind.json.JsonMapper;

/**
 * Spring context integration test for {@link DataMigrationNotificationAutoConfiguration}.
 */
@SpringBootTest(
        classes = DataMigrationNotificationAutoConfigurationTest.TestApplication.class,
        webEnvironment = SpringBootTest.WebEnvironment.NONE)
class DataMigrationNotificationAutoConfigurationTest {

    @Autowired
    private DataMigrationNotificationService notificationService;

    @Test
    void autoConfiguresDataMigrationBeansWhenPropertiesPresent() {
        assertThat(notificationService).isNotNull();
    }

    /**
     * {@link DataMigrationNotificationAutoConfiguration} is
     * {@code @ConditionalOnBean(DataMigrationNotificationProperties.class)}.
     * Register {@link DataMigrationNotificationProperties} in a separate imported config
     * so it exists before the auto-configuration is processed.
     */
    @SpringBootConfiguration
    @Import({
            DataMigrationNotificationPropertiesConfig.class,
            DataMigrationNotificationAutoConfiguration.class
    })
    static class TestApplication {
        @Bean
        JsonMapper jsonMapper() {
            return new JsonMapper();
        }
    }

    @Configuration
    static class DataMigrationNotificationPropertiesConfig {

        @Bean
        private static DataMigrationNotificationProperties notificationProperties() {
            var properties = new DataMigrationNotificationProperties();
            properties.setName("test-app");
            properties.setVersion("1.2.3");
            properties.setHostname("host-1");
            return properties;
        }
    }
}
