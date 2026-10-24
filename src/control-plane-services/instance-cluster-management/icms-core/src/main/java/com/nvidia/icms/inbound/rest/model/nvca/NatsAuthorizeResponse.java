/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.inbound.rest.model.nvca;

import com.fasterxml.jackson.annotation.JsonInclude;
import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.List;
import lombok.Builder;
import lombok.Data;

/**
 * Response body for the NATS auth-callout webhook endpoint.
 *
 * <p>Wire contract matching {@code webhook.Response} in
 * {@code nvcf-nats-auth-callout-service} ({@code internal/plugins/webhook/webhook.go})
 * — field names match the plugin's JSON marshaling exactly. Success responses
 * carry {@code userId}, {@code account}, {@code permissions}, {@code ttl};
 * failures return HTTP 4xx so the plugin maps them to {@code ErrTypeUnauthorized}.</p>
 */
@Data
@Builder
@JsonInclude(JsonInclude.Include.NON_NULL)
public class NatsAuthorizeResponse {

    /**
     * Identifier placed in the NATS user JWT's {@code name} field. Format:
     * {@code cluster-<clusterId>} — useful for server-side diagnostics that
     * inspect NATS connection metadata.
     */
    @JsonProperty("userId")
    private String userId;

    /** NATS account the client is bound to in the issued JWT. */
    private String account;

    private Permissions permissions;

    /**
     * JWT lifetime in nanoseconds. Matches the {@code time.Duration} Go type
     * the webhook plugin unmarshals into (see {@code webhook.Response.TTL}).
     */
    private Long ttl;

    @Data
    @Builder
    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class Permissions {
        private SubjectList publish;
        private SubjectList subscribe;
    }

    @Data
    @Builder
    @JsonInclude(JsonInclude.Include.NON_NULL)
    public static class SubjectList {
        private List<String> allow;
        private List<String> deny;
    }
}
