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

import static io.cloudevents.jackson.JsonFormat.CONTENT_TYPE;

import com.nvidia.boot.migration.notification.service.DataMigrationNotificationProperties;
import com.nvidia.boot.migration.notification.service.DataMigrationNotificationService;
import io.cloudevents.core.format.EventFormat;
import io.cloudevents.core.provider.EventFormatProvider;
import java.util.Objects;
import org.springframework.boot.autoconfigure.AutoConfiguration;
import org.springframework.boot.autoconfigure.condition.ConditionalOnBean;
import org.springframework.boot.autoconfigure.condition.ConditionalOnMissingBean;
import org.springframework.context.annotation.Bean;
import tools.jackson.databind.json.JsonMapper;

@AutoConfiguration
@ConditionalOnBean(DataMigrationNotificationProperties.class)
public class DataMigrationNotificationAutoConfiguration {

    @Bean
    @ConditionalOnMissingBean(DataMigrationNotificationService.class)
    public DataMigrationNotificationService dataMigrationNotificationService(
            JsonMapper jsonMapper,
            EventFormat eventFormat,
            DataMigrationNotificationProperties properties) {
        return new DataMigrationNotificationService(jsonMapper, eventFormat, properties);
    }

    @Bean
    @ConditionalOnMissingBean(EventFormat.class)
    public EventFormat eventFormat() {
        var format =  EventFormatProvider.getInstance().resolveFormat(CONTENT_TYPE);
        return Objects.requireNonNull(
                format,
                "Failed to resolve CloudEvents JSON format. " +
                        "Ensure cloudevents-json-jackson is on classpath.");
    }
}
