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

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.when;
import static org.springframework.util.MimeTypeUtils.APPLICATION_JSON_VALUE;

import com.nvidia.boot.migration.notification.event.DataMigrationEvent;
import com.nvidia.boot.migration.notification.event.DataMigrationEventPayload;
import com.nvidia.boot.migration.notification.event.DataMigrationType;
import io.cloudevents.CloudEvent;
import io.cloudevents.core.data.PojoCloudEventData;
import io.cloudevents.core.format.EventFormat;
import io.nats.client.Connection;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.Optional;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import tools.jackson.databind.json.JsonMapper;

@ExtendWith(MockitoExtension.class)
class DataMigrationNotificationServiceTest {

    private static final String EVENT_TYPE = "data-migration";

    @Mock
    private EventFormat format;

    @Mock
    private Connection connection;

    private DataMigrationNotificationService notificationService;

    @BeforeEach
    void setUp() {
        notificationService = new DataMigrationNotificationService(
                new JsonMapper(),
                format,
                notificationProperties());
    }

    @ParameterizedTest(name = "{0}")
    @MethodSource("notificationMethods")
    void notificationMethodPublishesSerializedCloudEvent(
            String testName,
            NotificationMethod notificationMethod,
            String expectedSubject,
            DataMigrationType expectedType) throws Exception {
        var serialized = "{\"specversion\":\"1.0\"}".getBytes(StandardCharsets.UTF_8);
        when(format.serialize(any(CloudEvent.class))).thenReturn(serialized);

        notificationMethod.notify(
                notificationService, connection, "backfill", Optional.of("copy finished"));

        var cloudEventCaptor = ArgumentCaptor.forClass(CloudEvent.class);
        verify(format).serialize(cloudEventCaptor.capture());
        verify(connection).publish(expectedSubject, serialized);
        verify(connection).flush(Duration.ofSeconds(5));

        var cloudEvent = cloudEventCaptor.getValue();
        assertCloudEventMetadata(cloudEvent);

        var payload = payloadFrom(cloudEvent);
        assertThat(payload.getApplicationName()).isEqualTo("test-app");
        assertThat(payload.getApplicationVersion()).isEqualTo("1.2.3");
        assertThat(payload.getHostname()).isEqualTo("host-1");
        assertThat(payload.getJobName()).isEqualTo("backfill");
        assertThat(payload.getType()).isEqualTo(expectedType);
        assertThat(payload.getMessage()).isEqualTo("copy finished");
    }

    @ParameterizedTest(name = "{0}")
    @MethodSource("notificationMethods")
    void notificationMethodOmitsMessageWhenMessageIsNull(
            String testName,
            NotificationMethod notificationMethod,
            String expectedSubject,
            DataMigrationType expectedType) throws Exception {
        var serialized = "{\"specversion\":\"1.0\"}".getBytes(StandardCharsets.UTF_8);
        when(format.serialize(any(CloudEvent.class))).thenReturn(serialized);

        notificationMethod.notify(notificationService, connection, "validate", Optional.empty());

        var cloudEventCaptor = ArgumentCaptor.forClass(CloudEvent.class);
        verify(format).serialize(cloudEventCaptor.capture());
        verify(connection).publish(expectedSubject.replace(".backfill.", ".validate."), serialized);
        verify(connection).flush(Duration.ofSeconds(5));

        var cloudEvent = cloudEventCaptor.getValue();
        assertCloudEventMetadata(cloudEvent);

        var payload = payloadFrom(cloudEvent);
        assertThat(payload.getApplicationName()).isEqualTo("test-app");
        assertThat(payload.getApplicationVersion()).isEqualTo("1.2.3");
        assertThat(payload.getHostname()).isEqualTo("host-1");
        assertThat(payload.getJobName()).isEqualTo("validate");
        assertThat(payload.getType()).isEqualTo(expectedType);
        assertThat(payload.getMessage()).isNull();
    }

    @ParameterizedTest(name = "applicationName={0}")
    @MethodSource("invalidNatsSubjectTokens")
    void constructorRejectsApplicationNameContainingReservedNatsSubjectCharacter(
            String applicationName, String invalidCharacter) {
        var properties = notificationProperties(applicationName);

        assertThatThrownBy(() -> new DataMigrationNotificationService(
                new JsonMapper(), format, properties))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("applicationName")
                .hasMessageContaining(invalidCharacter);

        verifyNoInteractions(format, connection);
    }

    @ParameterizedTest(name = "{0} rejects taskName={1}")
    @MethodSource("notificationMethodsWithInvalidTaskNames")
    void notificationMethodRejectsTaskNameContainingReservedNatsSubjectCharacter(
            String testName,
            NotificationMethod notificationMethod,
            String taskName,
            String invalidCharacter) {
        assertThatThrownBy(() -> notificationMethod.notify(
                notificationService,
                connection,
                taskName,
                Optional.of("message")))
                .isInstanceOf(IllegalArgumentException.class)
                .hasMessageContaining("taskName")
                .hasMessageContaining(invalidCharacter);

        verifyNoInteractions(format, connection);
    }

    private static Stream<Arguments> notificationMethods() {
        return Stream.of(
                Arguments.of(
                        "notifyOnExecutionStart",
                        (NotificationMethod) DataMigrationNotificationService::notifyOnStart,
                        "test-app.backfill.START",
                        DataMigrationType.START),
                Arguments.of(
                        "notifyOnCompleteSuccessfully",
                        (NotificationMethod) DataMigrationNotificationService::notifyOnEnd,
                        "test-app.backfill.END",
                        DataMigrationType.END),
                Arguments.of(
                        "notifyOnCompleteWithError",
                        (NotificationMethod) DataMigrationNotificationService::notifyOnError,
                        "test-app.backfill.ERROR",
                        DataMigrationType.ERROR));
    }

    private static Stream<Arguments> notificationMethodsWithInvalidTaskNames() {
        return notificationMethods()
                .flatMap(notificationMethodArguments -> invalidNatsSubjectTokens()
                        .map(invalidTokenArguments -> Arguments.of(
                                notificationMethodArguments.get()[0],
                                notificationMethodArguments.get()[1],
                                invalidTokenArguments.get()[0],
                                invalidTokenArguments.get()[1])));
    }

    private static Stream<Arguments> invalidNatsSubjectTokens() {
        return Stream.of(
                Arguments.of("invalid.name", "."),
                Arguments.of("invalid*name", "*"),
                Arguments.of("invalid>name", ">"));
    }

    @FunctionalInterface
    private interface NotificationMethod {
        void notify(
                DataMigrationNotificationService notificationService,
                Connection connection,
                String taskName,
                Optional<String> message);
    }

    private static void assertCloudEventMetadata(CloudEvent cloudEvent) {
        assertThat(cloudEvent.getId()).isNotBlank();
        assertThat(cloudEvent.getSource()).isEqualTo(URI.create("test-app:1.2.3@host-1"));
        assertThat(cloudEvent.getType()).isEqualTo(EVENT_TYPE);
        assertThat(cloudEvent.getDataContentType()).isEqualTo(APPLICATION_JSON_VALUE);
        assertThat(cloudEvent.getData()).isNotNull();
        assertThat(cloudEvent.getTime()).isNotNull();
    }

    private static DataMigrationEventPayload payloadFrom(CloudEvent cloudEvent) {
        assertThat(cloudEvent.getData()).isInstanceOf(PojoCloudEventData.class);
        var data = (PojoCloudEventData<?>) cloudEvent.getData();
        assertThat(data.getValue()).isInstanceOf(DataMigrationEvent.class);
        return ((DataMigrationEvent) data.getValue()).getPayload();
    }

    private static DataMigrationNotificationProperties notificationProperties() {
        return notificationProperties("test-app");
    }

    private static DataMigrationNotificationProperties notificationProperties(String applicationName) {
        var properties = new DataMigrationNotificationProperties();
        properties.setName(applicationName);
        properties.setVersion("1.2.3");
        properties.setHostname("host-1");
        return properties;
    }
}
