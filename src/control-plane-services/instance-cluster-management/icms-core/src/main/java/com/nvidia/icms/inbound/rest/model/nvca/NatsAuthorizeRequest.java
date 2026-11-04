/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.inbound.rest.model.nvca;

import jakarta.validation.constraints.NotBlank;
import lombok.Data;

/**
 * Request body for the NATS auth-callout webhook endpoint
 * ({@code POST /v1/nvca/nats-authorize} or {@code POST /v1/si/oidc/nats-authorize}).
 *
 * <p>This is the wire contract emitted by the {@code webhook} plugin in
 * {@code nvcf-nats-auth-callout-service} (see {@code webhook.Request} at
 * {@code internal/plugins/webhook/webhook.go}). Field names match the plugin's
 * JSON marshaling exactly — changing them breaks the plugin.</p>
 */
@Data
public class NatsAuthorizeRequest {

    /** NATS account the client is connecting to (e.g. {@code APP}). */
    @NotBlank
    private String account;

    /** Alias of the plugin instance configured on the auth-callout side. */
    @NotBlank
    private String pluginName;

    /** The PSAT / SPIFFE JWT the client presented as its connect token. */
    @NotBlank
    private String payload;
}
