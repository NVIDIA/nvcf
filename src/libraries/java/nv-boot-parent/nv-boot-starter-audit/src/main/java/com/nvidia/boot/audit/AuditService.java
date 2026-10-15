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

import static org.springframework.security.oauth2.jwt.JwtClaimNames.ISS;
import static org.springframework.security.oauth2.jwt.JwtClaimNames.SUB;

import org.springframework.util.CollectionUtils;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.event.AuditEventPayload;
import com.nvidia.boot.audit.event.BootAuditEvent;
import java.util.Base64;
import java.util.Collections;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import lombok.Data;
import lombok.ToString;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.context.ApplicationEventPublisher;
import org.springframework.security.authentication.AbstractAuthenticationToken;
import org.springframework.security.core.Authentication;
import org.springframework.security.core.authority.AuthorityUtils;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;

/**
 * Publishes {@link BootAuditEvent} instances. When an {@link AuditProperties} bean is present with
 * non-blank {@code hmacKeys} and {@code hmacKid}, payload builders use signed mode and each
 * event carries a top-level {@code hmac} over the serialized payload.
 */
@Slf4j
public final class AuditService {

    private static final Authentication AUDIT_AUTHENTICATION_TOKEN = auditAuthenticationToken();
    private static final String DEFAULT_ACTOR_ID = "unknown";
    private static final String REMOTE_ADDRESS = "remoteAddress";

    private static final String MESG_MISSING_KEY = "Missing key for kid '%s'";
    private static final String MESG_MISSING_KEYS_OR_KID = "Missing HMAC keys or kid";

    private final ApplicationEventPublisher eventPublisher;
    private final ObjectProvider<AuditProperties> auditPropertiesProvider;
    private final JsonMapper jsonMapper;

    public AuditService(
            ApplicationEventPublisher eventPublisher,
            ObjectProvider<AuditProperties> auditPropertiesProvider,
            JsonMapper jsonMapper) {
        this.eventPublisher = eventPublisher;
        this.auditPropertiesProvider = auditPropertiesProvider;
        this.jsonMapper = jsonMapper;
    }

    public AuditEventPayload.Builder auditEventPayloadBuilder() {
        return auditEventPayloadBuilder(AUDIT_AUTHENTICATION_TOKEN, Collections.emptyMap());
    }

    public AuditEventPayload.Builder auditEventPayloadBuilder(
            Authentication authentication,
            Map<String, String> customProperties) {
        var props = customProperties != null ?
                                customProperties : Collections.<String, String>emptyMap();
        var builder = resolveSigningContext()
                .map(ctx -> AuditEventPayload.signedBuilder(ctx.kid(), ctx.key()))
                .orElseGet(AuditEventPayload::builder);
        builder.actorId(getActorId(authentication))
                .actorLocation(props.getOrDefault(REMOTE_ADDRESS, "127.0.0.1"))
                .subjectId(getSubjectId(authentication))
                .subjectLocation(props.getOrDefault(REMOTE_ADDRESS, "127.0.0.1"));
        props.forEach(builder::custom);
        return builder;
    }

    public void audit(AuditEventPayload.Builder payloadBuilder) {
        var payload = payloadBuilder.build();
        var eventHmac = resolveSigningContext()
                .map(ctx -> AuditUtils.computeHmacFormatted(
                        ctx.kid(),
                        ctx.key(),
                        readJsonTree(payload.toJson())))
                .orElse(null);
        eventPublisher.publishEvent(new BootAuditEvent(payload, eventHmac, AuditService.class));
    }

    private JsonNode readJsonTree(String json) {
        try {
            return jsonMapper.readTree(json);
        } catch (JacksonException e) {
            throw new IllegalArgumentException("Failed to parse JSON in readJsonTree", e);
        }
    }

    private Optional<SigningContext> resolveSigningContext() {
        var auditProperties = auditPropertiesProvider.getIfAvailable();
        if (auditProperties == null) {
            return Optional.empty();
        }

        var hmacKeys = auditProperties.getHmacKeys();
        var kid = auditProperties.getHmacKid();
        if (StringUtils.isBlank(hmacKeys) || StringUtils.isBlank(kid)) {
            log.error(MESG_MISSING_KEYS_OR_KID);
            throw new IllegalStateException(MESG_MISSING_KEYS_OR_KID);
        }

        try {
            var rawKeys = Base64.getDecoder().decode(hmacKeys);
            var keyStore = jsonMapper.readValue(rawKeys, HmacKeyStore.class);
            var keyMaterial = getKeyByKid(keyStore, kid);
            byte[] key = Base64.getDecoder().decode(keyMaterial);
            return Optional.of(new SigningContext(kid, key));
        } catch (JacksonException | IllegalArgumentException e) {
            var mesg = "Failed to parse HMAC keys. Expected base64-encoded JSON with structure: " +
                                    "{\"keys\": [{\"kid\": \"...\", \"key\": \"...\"}]}";
            log.error(mesg, e);
            throw new IllegalStateException(mesg, e);
        }
    }

    private String getKeyByKid(HmacKeyStore keyStore, String kid) {
        if (keyStore == null || keyStore.getKeys() == null || keyStore.getKeys().isEmpty()) {
            throw new IllegalStateException("Failed to parse HMAC keys. Missing 'keys' array");
        }

        return keyStore.getKeys().stream()
                .filter(hmacKeyMaterial -> kid.equals(hmacKeyMaterial.getKid()))
                .findFirst()
                .map(HmacKeyMaterial::getKey)
                .orElseThrow(() -> new IllegalStateException(MESG_MISSING_KEY.formatted(kid)));
    }

    private String getActorId(Authentication authentication) {
        if (authentication instanceof JwtAuthenticationToken token) {
            var issuer = (String) token.getTokenAttributes().get(ISS);
            if (issuer != null && !issuer.isBlank()) {
                var subject = (String) token.getTokenAttributes().get(SUB);
                return (subject != null && !subject.isBlank())
                        ? issuer + "_" + subject
                        : issuer;
            }
        }
        return (authentication != null && authentication.getName() != null)
                ? authentication.getName() : DEFAULT_ACTOR_ID;
    }

    private String getSubjectId(Authentication authentication) {
        if (authentication instanceof JwtAuthenticationToken token) {
            var subject = (String) token.getTokenAttributes().get(SUB);
            if (subject != null && !subject.isBlank()) {
                return subject;
            }
        }
        return (authentication != null && authentication.getName() != null)
                ? authentication.getName() : DEFAULT_ACTOR_ID;
    }

    private static Authentication auditAuthenticationToken() {
        return new AbstractAuthenticationToken(AuthorityUtils.NO_AUTHORITIES) {
            @Override
            public Object getCredentials() {
                return null;
            }

            @Override
            public Object getPrincipal() {
                return "system:internal-thread";
            }
        };
    }

    private record SigningContext(String kid, byte[] key) {}

    @Data
    private static class HmacKeyMaterial {
        private String kid;

        @ToString.Exclude
        private String key;
    }

    @Data
    private static class HmacKeyStore {
        private List<HmacKeyMaterial> keys;
    }
}
