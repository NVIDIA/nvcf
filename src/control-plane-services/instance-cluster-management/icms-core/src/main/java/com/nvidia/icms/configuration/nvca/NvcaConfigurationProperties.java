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
package com.nvidia.icms.configuration.nvca;

import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.cloud.context.config.annotation.RefreshScope;
import org.springframework.context.annotation.Configuration;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;

@RefreshScope
@Configuration
@ConfigurationProperties(prefix = "icms.nvca")
@Data
public class NvcaConfigurationProperties {

    private String creationQueueNameFormat;
    private String tasksCreationQueueNameFormat;
    private boolean tasksCreationQueuesEnabled;
    private String terminationQueueNameFormat;
    
    private SisConfig sisConfig;
    private VaultConfig vaultConfig;
    private ImageCredentialHelper imageCredentialHelper;

    private String clusterVersion;

    private boolean nvcaSelfDestructEnabled;
    private String nvcaSelfDestructMinVersion;

    private List<String> clusterAttributes;

    /**
     * Feature flag: when enabled, validates that every non-wildcard ("*") entry in authorizedNcaIds
     * matches the expected NCA ID format. This is used to harden cluster create/update APIs.
     */
    private boolean authorizedNcaIdRegexValidationEnabled;

    /**
     * Feature flag controlling OIDC/PSAT cluster identity (self-hosted NVCF).
     *
     * When disabled (default), SIS behaves like managed NVCF:
     *   - POST /v1/si/accounts/{ncaId}/clusters ignores jwks/oidcIssuer in the request body
     *   - PUT /v1/nvca/clusters/{id}/jwks returns 404
     *   - POST /v1/nvca/tokens/introspect and /v1/si/oidc/tokens/introspect are not public
     *   - AuthManagerResolver audience-based branch is disabled; falls back to legacy resolvers
     *
     * Self-hosted deployments set this to true through the ncp profile so nvcf-cli and
     * NVCA can use the OIDC/PSAT flow. Managed NVCF deployments leave this disabled.
     */
    private boolean oidcClusterIdentityEnabled = false;

    /**
     * Configuration for the NATS auth-callout webhook endpoint
     * ({@code POST /v1/nvca/nats-authorize} or {@code POST /v1/si/oidc/nats-authorize}),
     * consumed by the {@code webhook}
     * plugin in {@code nvcf-nats-auth-callout-service}. The endpoint itself
     * is additionally gated by {@link #oidcClusterIdentityEnabled}; this block only
     * shapes the returned NATS permissions / account / TTL.
     *
     * <p>Subject templates may contain the {@code {clusterId}} placeholder,
     * which is substituted with the verified cluster ID from the PSAT's
     * {@code nvcf-icms:{clusterId}} audience at request time.</p>
     */
    private NatsAuth natsAuth = new NatsAuth();

    // Nested configuration classes
    @Data
    public static class SisConfig {
        private String publicKeySetEndpoint;
        private String tokenUrl;
        private String spotServiceUrl;
    }

    @Data
    public static class VaultConfig {
        private String address;
    }

    @Data
    public static class ImageCredentialHelper {
        private ImageConfig imageConfig;
        
        @Data
        public static class ImageConfig {
            private String repository;
            private String tag;
        }
    }

    @Data
    public static class NatsAuth {
        /**
         * NATS account name returned in the webhook response. Placed into the
         * issued user JWT by the auth-callout service so the client ends up
         * scoped to this account. Typically {@code APP}.
         */
        private String account = "APP";

        /**
         * TTL for the issued NATS user JWT. Auth-callout converts this to the
         * JWT {@code exp} claim. ISO-8601 duration; default 1 hour.
         */
        private Duration ttl = Duration.ofHours(1);

        /**
         * Plugin aliases accepted from nvcf-nats-auth-callout-service. The
         * self-hosted auth-callout chart maps the SIS webhook plugin to alias
         * {@code oidc}; other plugins such as {@code nkey} must not be allowed
         * to use this endpoint.
         */
        private List<String> allowedPluginNames = new ArrayList<>(List.of("oidc"));

        private SubjectPermissions permissions = new SubjectPermissions();
    }

    @Data
    public static class SubjectPermissions {
        private SubjectList publish = new SubjectList();
        private SubjectList subscribe = new SubjectList();
    }

    @Data
    public static class SubjectList {
        /**
         * Subject patterns the client is allowed to operate on. Each entry may
         * contain the literal placeholder {@code {clusterId}}, which is replaced
         * with the verified cluster ID from the PSAT audience before the
         * permission is issued. Empty list by default because managed NVCF keeps
         * the auth-callout endpoint disabled; self-hosted profiles/deployments
         * must provide scoped patterns or authorization fails closed.
         */
        private List<String> allow = new ArrayList<>();
        private List<String> deny = new ArrayList<>();
    }
}
