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

import static java.lang.String.format;

import com.fasterxml.jackson.annotation.JsonCreator;
import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import tools.jackson.core.JacksonException;
import tools.jackson.databind.JsonNode;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.audit.AuditUtils;
import java.time.Instant;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import lombok.Getter;
import org.apache.commons.lang3.StringUtils;

/**
 * Immutable audit payload aligned with internal NVIDIA serialization of events
 * (including {@code machineId}, optional {@code hmacBefore}/{@code hmacAfter}, and JSON diff
 * summaries when both {@code jsonBefore} and {@code jsonAfter} are set).
 */
@Getter
@JsonInclude(JsonInclude.Include.NON_NULL)
public final class AuditEventPayload {

    private static final String UNKNOWN_MACHINE_ID = "unknown";

    private static volatile String macAddressCache;

    // Resolve machineId on first use, not at class init. AuditUtils.getMacAddress() walks network
    // interfaces and throws if none are available. Running that in a static field initializer would
    // fail class loading for the whole payload type (e.g. minimal containers). Lazy init defers
    // work until an audit payload is built and falls back to "unknown" if lookup still fails.
    private static String getMacAddressLazy() {
        if (macAddressCache == null) {
            synchronized (AuditEventPayload.class) {
                if (macAddressCache == null) {
                    try {
                        macAddressCache = AuditUtils.getMacAddress();
                    } catch (Exception e) {
                        macAddressCache = UNKNOWN_MACHINE_ID;
                    }
                }
            }
        }
        return macAddressCache;
    }

    private static final String MESG_EMPTY_PAYLOAD_FIELD =
            "Payload field '%s' cannot be blank or null";

    private static final JsonMapper MAPPER = JsonMapper.builder()
            .findAndAddModules()
            .changeDefaultPropertyInclusion(v -> v.withValueInclusion(JsonInclude.Include.NON_NULL))
            .build();

    /**
     * Pattern for formatted audit HMAC strings: exactly three non-empty colon-separated segments
     * ({@code algorithm}:{@code kid}:{@code base64Digest}). Used by the signed payload builder when
     * validating {@code hmacBefore} / {@code hmacAfter}; callers may reuse it for verification.
     */
    public static final Pattern FORMATTED_HMAC_PATTERN =
            Pattern.compile("^[^:]+:[^:]+:[^:]+$");

    private final UUID id;
    private final Instant timestamp;
    private final String machineId;

    private final String operation;
    private final String type;
    private final String actorId;
    private final String actorLocation;
    private final String subjectId;
    private final String subjectLocation;
    private final String objectId;
    private final String objectLocation;
    private final String groupType;
    private final String hmacBefore;
    private final String hmacAfter;
    private final String state;
    private final String stateSummary;
    private final String historySummary;
    private final String summary;
    private final Map<String, Object> data;

    /**
     * Jackson constructor (also used by {@link BuilderImpl} with {@code null} id/timestamp/machineId
     * so those fields are assigned internally).
     */
    @JsonCreator
    private AuditEventPayload(
            @JsonProperty("id") UUID id,
            @JsonProperty("timestamp") Instant timestamp,
            @JsonProperty("machineId") String machineId,
            @JsonProperty("operation") String operation,
            @JsonProperty("type") String type,
            @JsonProperty("actorId") String actorId,
            @JsonProperty("actorLocation") String actorLocation,
            @JsonProperty("subjectId") String subjectId,
            @JsonProperty("subjectLocation") String subjectLocation,
            @JsonProperty("objectId") String objectId,
            @JsonProperty("objectLocation") String objectLocation,
            @JsonProperty("groupType") String groupType,
            @JsonProperty("hmacBefore") String hmacBefore,
            @JsonProperty("hmacAfter") String hmacAfter,
            @JsonProperty("state") String state,
            @JsonProperty("stateSummary") String stateSummary,
            @JsonProperty("historySummary") String historySummary,
            @JsonProperty("summary") String summary,
            @JsonProperty("data") Map<String, Object> data) {
        this.id = id != null ? id : UUID.randomUUID();
        this.timestamp = timestamp != null ? timestamp : Instant.now();
        this.machineId = machineId != null ? machineId : getMacAddressLazy();
        this.operation = operation;
        this.type = type;
        this.actorId = actorId;
        this.actorLocation = actorLocation;
        this.subjectId = subjectId;
        this.subjectLocation = subjectLocation;
        this.objectId = objectId;
        this.objectLocation = objectLocation;
        this.groupType = groupType;
        this.hmacBefore = hmacBefore;
        this.hmacAfter = hmacAfter;
        this.state = state;
        this.stateSummary = stateSummary;
        this.historySummary = historySummary;
        this.summary = summary;
        this.data = data != null ? new LinkedHashMap<>(data) : new LinkedHashMap<>();
    }

    public static Builder builder() {
        return new BuilderImpl(null, null);
    }

    // Builder that computes {@code hmacBefore}/{@code hmacAfter} from
    // {@code jsonBefore}/{@code jsonAfter} when those nodes are set.
    public static Builder signedBuilder(String hmacKid, byte[] hmacKey) {
        return new BuilderImpl(hmacKid, hmacKey);
    }

    public Map<String, Object> getData() {
        return Collections.unmodifiableMap(data);
    }

    public String toJson() {
        try {
            return MAPPER.writeValueAsString(this);
        } catch (JacksonException e) {
            throw new RuntimeException("Failed to serialize audit payload", e);
        }
    }

    public static AuditEventPayload fromJson(String json) {
        try {
            return MAPPER.readValue(json, AuditEventPayload.class);
        } catch (JacksonException e) {
            throw new RuntimeException("Failed to deserialize audit payload", e);
        }
    }

    @Override
    public String toString() {
        return toJson();
    }

    public interface Builder {
        Builder operation(String operation);

        Builder type(String type);

        Builder actorId(String actorId);

        Builder actorLocation(String actorLocation);

        /**
         * Sets the logical subject id for the audit entry. At {@link #build()} time, if this
         * value is blank per {@link org.apache.commons.lang3.StringUtils#isBlank(String)} (null,
         * empty, or whitespace only), the implementation replaces it with {@code actorId} via a
         * local {@code resolvedSubjectId} assignment. The built {@link AuditEventPayload} therefore
         * never retains a blank subject id; callers must not assume an omitted or blank subject
         * stays blank on the payload.
         */
        Builder subjectId(String subjectId);

        Builder subjectLocation(String subjectLocation);

        Builder objectId(String objectId);

        Builder objectLocation(String objectLocation);

        Builder groupType(String groupType);

        Builder state(String state);

        Builder summary(String summary);

        Builder jsonBefore(JsonNode jsonBefore);

        Builder jsonAfter(JsonNode jsonAfter);

        /**
         * Adds or replaces a single entry in the builder's internal data map. Later
         * {@code custom} calls with the same {@code key} overwrite earlier values. The map is
         * shared with {@link #data(Map)}: keys merged from {@code data} overwrite prior
         * {@code custom} entries for the same key when {@code data} runs later, and a subsequent
         * {@code custom} with the same key overwrites values supplied by {@code data}.
         */
        Builder custom(String key, Object value);

        /**
         * Merges the given map into the builder's internal data map. Existing keys are left in
         * place except where the incoming map defines the same key, in which case the incoming
         * value wins. Prior {@link #custom(String, Object)} entries are preserved for keys absent
         * from {@code data}. A null argument does not clear existing entries; it is treated as
         * "merge nothing" while ensuring the internal map exists.
         */
        Builder data(Map<String, Object> data);

        /**
         * Validates required fields and returns a new {@link AuditEventPayload}. Blank
         * {@code subjectId} values (per {@link org.apache.commons.lang3.StringUtils#isBlank(String)})
         * are normalized to {@code actorId} when populating the payload: the builder's
         * {@code build()} method assigns a {@code resolvedSubjectId} local variable
         * ({@code actorId} when {@code subjectId} is blank, otherwise {@code subjectId}).
         */
        AuditEventPayload build();
    }

    private static final class BuilderImpl implements Builder {

        private static final String MESG_INVALID_PARAM =
                "Param '%s' cannot be empty or null";

        private final String hmacKid;
        private final byte[] hmacKey;

        private String operation;
        private String type;
        private String actorId;
        private String actorLocation;
        private String subjectId;
        private String subjectLocation;
        private String objectId;
        private String objectLocation;
        private String groupType;
        private String state;
        private String summary;
        private JsonNode jsonBefore;
        private JsonNode jsonAfter;
        private Map<String, Object> data = new LinkedHashMap<>();

        private BuilderImpl(String hmacKid, byte[] hmacKey) {
            if (hmacKid != null || hmacKey != null) {
                if (StringUtils.isBlank(hmacKid)) {
                    throw new IllegalArgumentException(format(MESG_INVALID_PARAM, "hmacKid"));
                }
                if (hmacKey == null) {
                    throw new IllegalArgumentException(format(MESG_INVALID_PARAM, "hmacKey"));
                }
            }
            this.hmacKid = hmacKid;
            this.hmacKey = hmacKey;
        }

        @Override
        public Builder operation(String operation) {
            this.operation = operation;
            return this;
        }

        @Override
        public Builder type(String type) {
            this.type = type;
            return this;
        }

        @Override
        public Builder actorId(String actorId) {
            this.actorId = actorId;
            return this;
        }

        @Override
        public Builder actorLocation(String actorLocation) {
            this.actorLocation = actorLocation;
            return this;
        }

        /**
         * {@inheritDoc}
         */
        @Override
        public Builder subjectId(String subjectId) {
            this.subjectId = subjectId;
            return this;
        }

        @Override
        public Builder subjectLocation(String subjectLocation) {
            this.subjectLocation = subjectLocation;
            return this;
        }

        @Override
        public Builder objectId(String objectId) {
            this.objectId = objectId;
            return this;
        }

        @Override
        public Builder objectLocation(String objectLocation) {
            this.objectLocation = objectLocation;
            return this;
        }

        @Override
        public Builder groupType(String groupType) {
            this.groupType = groupType;
            return this;
        }

        @Override
        public Builder state(String state) {
            this.state = state;
            return this;
        }

        @Override
        public Builder summary(String summary) {
            this.summary = summary;
            return this;
        }

        @Override
        public Builder jsonBefore(JsonNode jsonBefore) {
            this.jsonBefore = jsonBefore;
            return this;
        }

        @Override
        public Builder jsonAfter(JsonNode jsonAfter) {
            this.jsonAfter = jsonAfter;
            return this;
        }

        /**
         * {@inheritDoc}
         */
        @Override
        public Builder custom(String key, Object value) {
            this.data.put(key, value);
            return this;
        }

        /**
         * {@inheritDoc}
         */
        @Override
        public Builder data(Map<String, Object> data) {
            if (this.data == null) {
                this.data = new LinkedHashMap<>();
            }
            if (data != null) {
                this.data.putAll(data);
            }
            return this;
        }

        /**
         * {@inheritDoc}
         */
        @Override
        public AuditEventPayload build() {
            validateRequired();
            var resolvedSubjectId = StringUtils.isBlank(subjectId) ? actorId : subjectId;

            String hmacBeforeVal = null;
            String hmacAfterVal = null;
            if (hmacKid != null) {
                if (jsonBefore != null) {
                    hmacBeforeVal = AuditUtils.computeHmacFormatted(hmacKid, hmacKey, jsonBefore);
                }
                if (jsonAfter != null) {
                    hmacAfterVal = AuditUtils.computeHmacFormatted(hmacKid, hmacKey, jsonAfter);
                }
            }

            String stateSummaryValue = null;
            String historySummaryValue = null;
            if (jsonBefore != null && jsonAfter != null) {
                stateSummaryValue = AuditJsonDiff.stateSummary(jsonBefore, jsonAfter);
                historySummaryValue = AuditJsonDiff.historySummary(jsonBefore, jsonAfter);
            }

            if (data == null) {
                data = new LinkedHashMap<>();
            }

            validateHmacAndSummaries(hmacBeforeVal, hmacAfterVal,
                                     stateSummaryValue, historySummaryValue);

            return new AuditEventPayload(
                    null,
                    null,
                    null,
                    operation,
                    type,
                    actorId,
                    actorLocation,
                    resolvedSubjectId,
                    subjectLocation,
                    objectId,
                    objectLocation,
                    groupType,
                    hmacBeforeVal,
                    hmacAfterVal,
                    state,
                    stateSummaryValue,
                    historySummaryValue,
                    summary,
                    new LinkedHashMap<>(data));
        }

        private void validateRequired() {
            if (StringUtils.isBlank(operation)) {
                throw new IllegalArgumentException(format(MESG_EMPTY_PAYLOAD_FIELD, "operation"));
            }
            if (StringUtils.isBlank(type)) {
                throw new IllegalArgumentException(format(MESG_EMPTY_PAYLOAD_FIELD, "type"));
            }
            if (StringUtils.isBlank(actorId)) {
                throw new IllegalArgumentException(format(MESG_EMPTY_PAYLOAD_FIELD, "actorId"));
            }
        }

        private void validateHmacAndSummaries(
                String hmacBeforeVal,
                String hmacAfterVal,
                String stateSummaryValue,
                String historySummaryValue) {
            if (StringUtils.isNotBlank(hmacBeforeVal)) {
                Matcher matcher = FORMATTED_HMAC_PATTERN.matcher(hmacBeforeVal);
                if (!matcher.matches()) {
                    throw new IllegalStateException("hmacBefore does not match the pattern");
                }
            }
            if (StringUtils.isNotBlank(hmacAfterVal)) {
                Matcher matcher = FORMATTED_HMAC_PATTERN.matcher(hmacAfterVal);
                if (!matcher.matches()) {
                    throw new IllegalStateException("hmacAfter does not match the pattern");
                }
            }
            if (StringUtils.isNotBlank(stateSummaryValue)) {
                try {
                    MAPPER.readTree(stateSummaryValue);
                } catch (JacksonException e) {
                    throw new IllegalStateException("stateSummary is not valid JSON", e);
                }
            }
            if (StringUtils.isNotBlank(historySummaryValue)) {
                try {
                    MAPPER.readTree(historySummaryValue);
                } catch (JacksonException e) {
                    throw new IllegalStateException("historySummary is not valid JSON", e);
                }
            }
        }
    }
}
