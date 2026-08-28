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
package com.nvidia.icms.configuration;

import static com.nvidia.icms.configuration.YamlEnvironmentTestUtils.loadYamlEnvironment;
import static org.assertj.core.api.Assertions.assertThat;

import com.nvidia.icms.configuration.nats.NatsConfigurationProperties;
import com.nvidia.icms.configuration.byoc.ByocConfigurationProperties;
import java.io.IOException;
import java.time.Duration;
import java.util.Map;
import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.core.env.MapPropertySource;

class NcpProfileConfigurationTest {

    @Test
    void ncpProfileBindsSelfHostedRuntimeValues() throws IOException {
        var environment = loadYamlEnvironment("application.yaml", "application-ncp.yaml");
        environment.getPropertySources().addFirst(new MapPropertySource(
                "test-secrets",
                Map.of("kv.nkey_seed", "test-nkey-seed")));

        var nats = Binder.get(environment)
                .bind("icms.nats", NatsConfigurationProperties.class)
                .orElseThrow(IllegalStateException::new);
        var byoc = Binder.get(environment)
                .bind("icms.byoc", ByocConfigurationProperties.class)
                .orElseThrow(IllegalStateException::new);

        assertThat(nats.isNatsEnabled()).isTrue();
        assertThat(nats.isCreateNatsStreams()).isTrue();
        assertThat(nats.getConnectionTimeout()).isEqualTo(Duration.ofSeconds(10));
        assertThat(nats.getPingInterval()).isEqualTo(Duration.ofSeconds(5));
        assertThat(nats.getReconnectWait()).isEqualTo(Duration.ofMillis(100));
        assertThat(nats.getReconnectJitter()).isEqualTo(Duration.ofSeconds(1));
        assertThat(nats.isReconnectAllowed()).isFalse();
        assertThat(nats.getForceReconnectFlush()).isEqualTo(Duration.ofSeconds(5));
        assertThat(nats.getDelayBetweenMessages()).isEqualTo(Duration.ofMillis(100));
        assertThat(nats.getMessageTtl()).isEqualTo(Duration.ofHours(96));
        assertThat(nats.getNkeySeed()).contains("test-nkey-seed");
        assertThat(byoc.getEnv()).isEqualTo("default");
        assertThat(environment.getProperty("spring.cassandra.contact-points"))
                .isEqualTo("cassandra.cassandra-system.svc.cluster.local");
        assertThat(environment.getProperty("spring.cassandra.local-datacenter")).isEqualTo("ncp");
        assertThat(environment.getProperty("spring.security.oauth2.resourceserver.jwt.issuer-uri"))
                .isEqualTo("http://api.sis.svc.cluster.local");
        // AuthManagerResolver enables the second (CLI admin) issuer only when BOTH
        // admin-issuer-uri and admin-jwk-set-uri are non-blank; assert the pair so
        // dropping either key fails here.
        assertThat(environment.getProperty("spring.security.oauth2.resourceserver.jwt.admin-issuer-uri"))
                .isEqualTo("http://api.nvcf.svc.cluster.local");
        assertThat(environment.getProperty("spring.security.oauth2.resourceserver.jwt.admin-jwk-set-uri"))
                .isNotBlank();
        // icms.nvca.sis-config.* is intentionally not asserted here: the ncp profile
        // does not override it, so the values are application.yaml fallbacks (TBD until
        // the self-hosted OIDC cluster-identity wiring is settled).
    }

    @Test
    void bootstrapUsesConfiguredHostname() throws IOException {
        var environment = loadYamlEnvironment("bootstrap.yaml", "bootstrap-ncp.yaml");
        environment.getPropertySources().addFirst(new MapPropertySource(
                "host-environment",
                Map.of("HOSTNAME", "sis-api")));

        assertThat(environment.getProperty("spring.application.host.id"))
                .isEqualTo("sis-api");
        assertThat(environment.getProperty("spring.application.env")).isEqualTo("ncp");
    }
}
