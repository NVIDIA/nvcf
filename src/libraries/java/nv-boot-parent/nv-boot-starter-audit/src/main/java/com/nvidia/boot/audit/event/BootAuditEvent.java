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

package com.nvidia.boot.audit.event;

import com.fasterxml.jackson.annotation.JsonCreator;
import com.fasterxml.jackson.annotation.JsonIgnore;
import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.json.JsonMapper;
import tools.jackson.databind.SerializationFeature;
import tools.jackson.databind.cfg.MapperConfig;
import tools.jackson.databind.introspect.AnnotatedMember;
import tools.jackson.databind.introspect.JacksonAnnotationIntrospector;
import lombok.EqualsAndHashCode;
import org.springframework.context.ApplicationEvent;

/**
 * Spring application event carrying an audit payload and optional payload-level HMAC.
 */
@EqualsAndHashCode(callSuper = false)
@JsonInclude(JsonInclude.Include.NON_NULL)
public class BootAuditEvent extends ApplicationEvent {

    // Ignores fields from super classes when deserializing.
    public static final JsonMapper AUDIT_EVENT_JSON_MAPPER = JsonMapper.builder()
            .findAndAddModules()
            .annotationIntrospector(new IgnoreInheritedMemberIntrospector())
            .changeDefaultPropertyInclusion(v -> v.withValueInclusion(JsonInclude.Include.NON_NULL))
            .disable(SerializationFeature.FAIL_ON_EMPTY_BEANS)
            .build();

    private final AuditEventPayload payload;

    // HMAC of the entire serialized payload; null when signing is not configured.
    private final String hmac;

    @JsonIgnore
    @EqualsAndHashCode.Exclude
    private final Class<?> auditedClass;

    public BootAuditEvent(
            AuditEventPayload payload,
            String hmac,
            Class<?> auditedClass) {
        super(payload);
        this.payload = payload;
        this.hmac = hmac;
        this.auditedClass = auditedClass;
    }

    @JsonCreator
    public static BootAuditEvent fromJsonProperties(
            @JsonProperty("payload") AuditEventPayload payload,
            @JsonProperty("hmac") String hmac) {
        return new BootAuditEvent(payload, hmac, BootAuditEvent.class);
    }

    public static BootAuditEvent fromJson(String json) throws JacksonException {
        return AUDIT_EVENT_JSON_MAPPER.readValue(json, BootAuditEvent.class);
    }

    @JsonProperty("payload")
    public AuditEventPayload getPayload() {
        return payload;
    }

    @JsonProperty("hmac")
    public String getHmac() {
        return hmac;
    }

    @JsonIgnore
    public String getSourceClassName() {
        return auditedClass.getCanonicalName();
    }

    public String toJson() {
        try {
            return AUDIT_EVENT_JSON_MAPPER.writeValueAsString(this);
        } catch (JacksonException e) {
            throw new IllegalStateException("Failed to serialize audit event", e);
        }
    }

    @Override
    public String toString() {
        return toJson();
    }

    private static class IgnoreInheritedMemberIntrospector extends JacksonAnnotationIntrospector {

        @Override
        public boolean hasIgnoreMarker(MapperConfig<?> config, AnnotatedMember member) {
            return member.getDeclaringClass().getName().contains("org.spring")
                    || member.getDeclaringClass().getName().contains("java.util")
                    || super.hasIgnoreMarker(config, member);
        }
    }
}
