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
package com.nvidia.ess.it;

import java.util.List;
import lombok.Data;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.context.annotation.Configuration;

@Configuration
@ConfigurationProperties("integration-test")
@Data
public class IntegrationTestProperties {
    private OperatorAuth operator;
    private TenantAuth tenant;

    @Data
    public static class OperatorAuth {
        private OAuth2ServerProperties oauth2Server;
        private OAuth2ClientProperties oauth2Client;
    }

    @Data
    public static class TenantAuth {
        private OAuth2ServerProperties oauth2Server;
        private OAuth2ClientProperties nsAdmin;
        private OAuth2ClientProperties entityAdmin;
        private OAuth2ClientProperties secretAdmin;
        private OAuth2ClientProperties secretConsumer;

        private OAuth2ServerProperties notarySignServer;
        private OAuth2ClientProperties notarySignClient;
        private NotaryProperties notary;

        @Data
        public static class NotaryProperties {
            private String iss;
            private String jwks;
            private String sub;
        }
    }

    @Data
    public static class OAuth2ServerProperties {
        private String iss;
        private String jwks;
    }

    @Data
    public static class OAuth2ClientProperties {
        private String iss;
        private String sub;
        private String secret;
        private List<String> scopes;
    }
}
