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

package com.nvidia.boot.audit;

import static com.nvidia.boot.audit.AuditUtils.computeHmacFormatted;
import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.node.ArrayNode;
import tools.jackson.databind.node.ObjectNode;
import com.nvidia.boot.audit.event.BootAuditEvent;
import java.util.Base64;
import java.util.Collections;
import java.util.Map;
import java.util.regex.Pattern;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.ArgumentCaptor;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.context.ApplicationEventPublisher;
import org.springframework.security.core.Authentication;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;

@ExtendWith(MockitoExtension.class)
class AuditServiceTest {

    @Mock
    private ApplicationEventPublisher eventPublisher;

    @Mock
    private ObjectProvider<AuditProperties> auditPropertiesProvider;

    private JsonMapper jsonMapper;

    private AuditService auditService;

    /** Raw HMAC key bytes (must match {@link #signingAuditProperties()} JSON entry). */
    private static final byte[] SIGNING_KEY_RAW = new byte[32];

    private static final String SIGNING_KID = "test-signing-kid";

    static {
        for (int i = 0; i < SIGNING_KEY_RAW.length; i++) {
            SIGNING_KEY_RAW[i] = (byte) (i + 1);
        }
    }

    @BeforeEach
    void setUp() {
        when(auditPropertiesProvider.getIfAvailable()).thenReturn(null);
        jsonMapper = JsonMapper.builder().build();
        auditService = new AuditService(eventPublisher, auditPropertiesProvider, jsonMapper);
    }

    private AuditProperties signingAuditProperties() throws JacksonException {
        var props = new AuditProperties();
        props.setHmacKid(SIGNING_KID);
        props.setHmacKeys(encodedSigningHmacKeys());
        return props;
    }

    /** Base64-encoded key-store JSON matching {@link #SIGNING_KID} and raw key {@link #SIGNING_KEY_RAW}. */
    private String encodedSigningHmacKeys() throws JacksonException {
        ArrayNode keys = jsonMapper.createArrayNode();
        ObjectNode entry = jsonMapper.createObjectNode();
        entry.put("kid", SIGNING_KID);
        entry.put("key", Base64.getEncoder().encodeToString(SIGNING_KEY_RAW));
        keys.add(entry);
        ObjectNode root = jsonMapper.createObjectNode();
        root.set("keys", keys);
        return Base64.getEncoder().encodeToString(jsonMapper.writeValueAsBytes(root));
    }

    private void useSigningAuditService() throws JacksonException {
        when(auditPropertiesProvider.getIfAvailable()).thenReturn(signingAuditProperties());
        auditService = new AuditService(eventPublisher, auditPropertiesProvider, jsonMapper);
    }

    @Test
    void auditEventPayloadBuilderReturnsBuilder() {
        var builder = auditService.auditEventPayloadBuilder();

        assertThat(builder).isNotNull();
        var payload = builder.operation("CREATE").type("TASK").build();
        assertThat(payload).isNotNull();
        assertThat(payload.getOperation()).isEqualTo("CREATE");
    }

    @Test
    void auditEventPayloadBuilderWithAuthAndPropsPopulatesActorAndSubject() {
        var auth = mock(Authentication.class);
        when(auth.getName()).thenReturn("test-user");

        var builder = auditService.auditEventPayloadBuilder(auth,
                                                            Map.of("remoteAddress", "192.168.1.1"));
        var payload = builder.operation("CREATE").type("TASK").build();

        assertThat(payload.getActorId()).isEqualTo("test-user");
        assertThat(payload.getSubjectId()).isEqualTo("test-user");
        assertThat(payload.getActorLocation()).isEqualTo("192.168.1.1");
        assertThat(payload.getSubjectLocation()).isEqualTo("192.168.1.1");
    }

    @Test
    void auditEventPayloadBuilderWithNullAuthUsesDefaultActorId() {
        var builder = auditService.auditEventPayloadBuilder(null, Collections.emptyMap());
        var payload = builder.operation("CREATE").type("TASK").build();

        assertThat(payload.getActorId()).isEqualTo("unknown");
        assertThat(payload.getSubjectId()).isEqualTo("unknown");
    }

    @Test
    void auditEventPayloadBuilderWithNullCustomPropsUsesDefaultLocation() {
        var auth = mock(Authentication.class);
        when(auth.getName()).thenReturn("user");

        var builder = auditService.auditEventPayloadBuilder(auth, null);
        var payload = builder.operation("CREATE").type("TASK").build();

        assertThat(payload.getActorLocation()).isEqualTo("127.0.0.1");
        assertThat(payload.getSubjectLocation()).isEqualTo("127.0.0.1");
    }

    @Test
    void auditEventPayloadBuilderWithJwtAuthUsesIssuerAndSubject() {
        var jwtAuth = mock(JwtAuthenticationToken.class);
        when(jwtAuth.getTokenAttributes())
                .thenReturn(
                        Map.of(
                                "iss", "https://auth.example.com",
                                "sub", "user-123"));

        var builder = auditService.auditEventPayloadBuilder(jwtAuth, Collections.emptyMap());
        var payload = builder.operation("CREATE").type("TASK").build();

        assertThat(payload.getActorId()).isEqualTo("https://auth.example.com_user-123");
        assertThat(payload.getSubjectId()).isEqualTo("user-123");
    }

    @Test
    void auditEventPayloadBuilderWithJwtAuthIssuerOnlyUsesIssuerAsActorId() {
        var jwtAuth = mock(JwtAuthenticationToken.class);
        when(jwtAuth.getTokenAttributes()).thenReturn(Map.of("iss", "https://auth.example.com"));

        var builder = auditService.auditEventPayloadBuilder(jwtAuth, Collections.emptyMap());
        var payload = builder.operation("CREATE").type("TASK").build();

        assertThat(payload.getActorId()).isEqualTo("https://auth.example.com");
        assertThat(payload.getSubjectId()).isEqualTo("unknown");
    }

    @Test
    void auditPublishesEvent() {
        var builder = auditService.auditEventPayloadBuilder()
                .operation("CREATE")
                .type("TASK")
                .summary("Created task");

        auditService.audit(builder);

        var eventCaptor = ArgumentCaptor.forClass(BootAuditEvent.class);
        verify(eventPublisher).publishEvent(eventCaptor.capture());

        var event = eventCaptor.getValue();
        assertThat(event.getPayload()).isNotNull();
        assertThat(event.getPayload().getOperation()).isEqualTo("CREATE");
        assertThat(event.getPayload().getType()).isEqualTo("TASK");
        assertThat(event.getHmac()).isNull();
    }

    /**
     * Counterpart to {@link #auditPublishesEvent()}: when {@link AuditProperties} supplies
     * {@code hmacKid} and {@code hmacKeys}, the published event carries a non-null, formatted
     * top-level {@code hmac}.
     */
    @Test
    void auditPublishesEventWithMockedAuditPropertiesAttachesFormattedEventHmac() {
        var props = mock(AuditProperties.class);
        when(props.getHmacKid()).thenReturn(SIGNING_KID);
        when(props.getHmacKeys()).thenReturn(encodedSigningHmacKeys());
        when(auditPropertiesProvider.getIfAvailable()).thenReturn(props);
        auditService = new AuditService(eventPublisher, auditPropertiesProvider, jsonMapper);

        var builder = auditService.auditEventPayloadBuilder()
                .operation("CREATE")
                .type("TASK")
                .summary("signed audit");

        auditService.audit(builder);

        var eventCaptor = ArgumentCaptor.forClass(BootAuditEvent.class);
        verify(eventPublisher).publishEvent(eventCaptor.capture());

        var event = eventCaptor.getValue();
        assertThat(event.getPayload()).isNotNull();
        assertThat(event.getPayload().getOperation()).isEqualTo("CREATE");
        assertThat(event.getPayload().getType()).isEqualTo("TASK");

        assertThat(event.getHmac()).isNotNull();
        assertThat(event.getHmac())
                .matches(Pattern.compile("^HMac-SHA3-512:[^:]+:.+$"));

        JsonNode payloadTree = jsonMapper.readTree(event.getPayload().toJson());
        assertThat(event.getHmac())
                .isEqualTo(computeHmacFormatted(SIGNING_KID, SIGNING_KEY_RAW, payloadTree));
    }

    @Test
    void customPropertiesArePassedToBuilder() {
        var auth = mock(Authentication.class);
        when(auth.getName()).thenReturn("user");

        var builder =
                auditService.auditEventPayloadBuilder(
                        auth,
                        Map.of("remoteAddress", "10.0.0.1", "customKey", "customValue"));
        var payload = builder.operation("CREATE").type("TASK").build();

        assertThat(payload.getData()).containsEntry("customKey", "customValue");
    }

    @Test
    void auditEventPayloadBuilderUsesSignedModeWhenAuditPropertiesConfigured() {
        useSigningAuditService();

        JsonNode before = jsonMapper.readTree("{\"a\":1}");
        JsonNode after = jsonMapper.readTree("{\"a\":2}");

        var payload = auditService.auditEventPayloadBuilder()
                .operation("UPDATE")
                .type("TASK")
                .jsonBefore(before)
                .jsonAfter(after)
                .build();

        assertThat(payload.getHmacBefore())
                .isEqualTo(computeHmacFormatted(SIGNING_KID, SIGNING_KEY_RAW, before));
        assertThat(payload.getHmacAfter())
                .isEqualTo(computeHmacFormatted(SIGNING_KID, SIGNING_KEY_RAW, after));
        assertThat(payload.getStateSummary()).isNotNull();
        assertThat(payload.getHistorySummary()).isNotNull();
    }
}
