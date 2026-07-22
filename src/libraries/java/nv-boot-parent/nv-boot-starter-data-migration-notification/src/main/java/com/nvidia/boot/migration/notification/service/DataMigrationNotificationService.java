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

package com.nvidia.boot.migration.notification.service;

import static org.springframework.util.MimeTypeUtils.APPLICATION_JSON_VALUE;

import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.migration.notification.event.DataMigrationEvent;
import com.nvidia.boot.migration.notification.event.DataMigrationEventPayload;
import com.nvidia.boot.migration.notification.event.DataMigrationType;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.builder.CloudEventBuilder;
import io.cloudevents.core.data.PojoCloudEventData;
import io.cloudevents.core.format.EventFormat;
import io.nats.client.Connection;
import jakarta.annotation.Nullable;
import java.net.URI;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Optional;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.exception.ExceptionUtils;
import org.springframework.context.ApplicationEvent;
import org.springframework.stereotype.Service;

@Slf4j
@Service
public class DataMigrationNotificationService {

    private static final String MESG_ERROR_SENDING_NATS = "Error sending NATs notification: {}";
    private static final String MESG_INVALID_NATS_SUBJECT_FIELD =
            "NATS subject field '%s' must not contain '.', '*', or '>'";
    private static final String EVENT_TYPE_DATA_MIGRATION = "data-migration";

    private final EventFormat format;
    private final JsonMapper jsonMapper;
    private final String applicationName;
    private final String applicationVersion;
    private final String hostname;
    private final URI source;

    public DataMigrationNotificationService(
            JsonMapper jsonMapper,
            EventFormat format,
            DataMigrationNotificationProperties properties) {
        this.jsonMapper = jsonMapper;
        this.format = format;
        this.applicationName = properties.getName();
        this.applicationVersion = properties.getVersion();
        this.hostname = properties.getHostname();
        validateNatsSubjectField("applicationName", applicationName);
        var sourceValue = "%s:%s@%s".formatted(
                applicationName,
                applicationVersion,
                hostname);
        this.source = URI.create(sourceValue);
    }

    public void notifyOnStart(
            Connection connection, String taskName, Optional<String> message) {
        dispatchDataMigrationEvent(connection, taskName, DataMigrationType.START, message);
    }

    public void notifyOnEnd(
            Connection connection, String taskName, Optional<String> message) {
        dispatchDataMigrationEvent(connection, taskName, DataMigrationType.END, message);
    }

    public void notifyOnError(
            Connection connection, String taskName, Optional<String> message) {
        dispatchDataMigrationEvent(connection, taskName, DataMigrationType.ERROR, message);
    }

    private void dispatchDataMigrationEvent(
            Connection connection,
            String taskName,
            DataMigrationType type,
            Optional<String> message) {
        var subject = buildNatsSubject(taskName, type);
        var migrationEventPayload = DataMigrationEventPayload.builder()
                .applicationName(applicationName)
                .applicationVersion(applicationVersion)
                .hostname(hostname)
                .jobName(taskName)
                .type(type)
                .message(message.orElse(null))
                .build();
        var migrationEvent = new DataMigrationEvent(migrationEventPayload);
        var cloudEvent = getCloudEvent(migrationEvent);
        try {
            connection.publish(subject, format.serialize(cloudEvent));
            connection.flush(Duration.ofSeconds(5));
        } catch (Exception e) {
            log.error(MESG_ERROR_SENDING_NATS, ExceptionUtils.getStackTrace(e));
        }
    }

    private String buildNatsSubject(
            String taskName,
            DataMigrationType type) {
        var typeToken = type.toString();
        validateNatsSubjectField("applicationName", applicationName);
        validateNatsSubjectField("taskName", taskName);
        validateNatsSubjectField("type", typeToken);
        return applicationName + "." + taskName + "." + typeToken;
    }

    private static void validateNatsSubjectField(String fieldName, @Nullable String fieldValue) {
        if (fieldValue != null
                && (fieldValue.indexOf('.') >= 0
                || fieldValue.indexOf('*') >= 0
                || fieldValue.indexOf('>') >= 0)) {
            throw new IllegalArgumentException(
                    MESG_INVALID_NATS_SUBJECT_FIELD.formatted(fieldName));
        }
    }

    private CloudEvent getCloudEvent(ApplicationEvent event) {
        var cloudEventData = PojoCloudEventData.wrap(event, jsonMapper::writeValueAsBytes);
        return CloudEventBuilder.v1()
                .withId(UUID.randomUUID().toString())
                .withSource(source)
                .withTime(Instant.now().atOffset(ZoneOffset.UTC))
                .withType(EVENT_TYPE_DATA_MIGRATION)
                .withDataContentType(APPLICATION_JSON_VALUE)
                .withData(cloudEventData)
                .build();
    }
}
