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

import static com.nvidia.icms.configuration.YamlEnvironmentTestUtils.loadYamlEnvironment;
import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.IOException;
import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.boot.context.properties.bind.Binder;
import org.springframework.core.env.MapPropertySource;
import org.springframework.core.env.StandardEnvironment;

class NvcaConfigurationPropertiesDefaultsTest {

    @Test
    void selfManagedFlagsDefaultToFalse() {
        NvcaConfigurationProperties properties = new NvcaConfigurationProperties();

        assertFalse(properties.isOidcClusterIdentityEnabled());
    }

    @Test
    void applicationYamlLeavesSelfManagedFlagsDisabledByDefault() throws IOException {
        NvcaConfigurationProperties properties = bindNvcaProperties("application.yaml");

        assertFalse(properties.isOidcClusterIdentityEnabled());
    }

    @Test
    void ncpProfileEnablesSelfManagedFlags() throws IOException {
        NvcaConfigurationProperties properties = bindNvcaProperties("application.yaml", "application-ncp.yaml");

        assertTrue(properties.isOidcClusterIdentityEnabled());
    }

    @Test
    void ncpProfileProvidesClusterScopedNatsAuthPermissions() throws IOException {
        NvcaConfigurationProperties properties = bindNvcaProperties("application.yaml", "application-ncp.yaml");

        List<String> publishAllow = properties.getNatsAuth().getPermissions().getPublish().getAllow();
        List<String> subscribeAllow = properties.getNatsAuth().getPermissions().getSubscribe().getAllow();

        assertEquals(List.of(
                "$JS.API.CONSUMER.CREATE.CreateNvcaFunctionTaskStream."
                        + "CreateNvcaFunctionTaskStream-{clusterId}",
                "$JS.API.CONSUMER.MSG.NEXT.CreateNvcaFunctionTaskStream."
                        + "CreateNvcaFunctionTaskStream-{clusterId}",
                "$JS.ACK.CreateNvcaFunctionTaskStream.CreateNvcaFunctionTaskStream-{clusterId}.>",
                "$JS.ACK.*.*.CreateNvcaFunctionTaskStream.CreateNvcaFunctionTaskStream-{clusterId}.>",
                "$JS.API.CONSUMER.CREATE.TerminateNvcaStream.TerminateNvcaStream-{clusterId}",
                "$JS.API.CONSUMER.MSG.NEXT.TerminateNvcaStream.TerminateNvcaStream-{clusterId}",
                "$JS.ACK.TerminateNvcaStream.TerminateNvcaStream-{clusterId}.>",
                "$JS.ACK.*.*.TerminateNvcaStream.TerminateNvcaStream-{clusterId}.>"), publishAllow);
        assertEquals(List.of("_INBOX.>"), subscribeAllow);
    }

    @Test
    void ncpProfileFlagsCanBeExplicitlyDisabled() throws IOException {
        StandardEnvironment environment = loadYamlEnvironment("application.yaml", "application-ncp.yaml");
        environment.getPropertySources()
                   .addFirst(new MapPropertySource("override",
                                                   Map.of("icms.nvca.oidc-cluster-identity-enabled", "false")));
        NvcaConfigurationProperties properties = bindNvcaProperties(environment);

        assertFalse(properties.isOidcClusterIdentityEnabled());
    }

    private NvcaConfigurationProperties bindNvcaProperties(String... resourceNames) throws IOException {
        return bindNvcaProperties(loadYamlEnvironment(resourceNames));
    }

    private NvcaConfigurationProperties bindNvcaProperties(StandardEnvironment environment) {
        return Binder.get(environment)
                     .bind("icms.nvca", NvcaConfigurationProperties.class)
                     .orElseThrow(() -> new IllegalStateException("nvca config is not bound"));
    }
}
