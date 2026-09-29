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
package com.nvidia.nvcf.configuration;

import com.nvidia.nvcf.util.NvcfOAuth2ClientUtils.ManagedHttpResources;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.health.contributor.Health;
import org.springframework.boot.health.contributor.HealthIndicator;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.context.request.RequestContextHolder;
import org.springframework.web.context.request.ServletRequestAttributes;
import org.springframework.web.reactive.function.client.WebClient;
import org.springframework.web.reactive.function.client.support.WebClientAdapter;
import org.springframework.web.service.annotation.GetExchange;
import org.springframework.web.service.invoker.HttpServiceProxyFactory;

// SRE observed that when a pod becomes unhealthy, they could not successfully invoke
// the public health endpoint(localhost:8080/health) and the request timed out. However,
// in the same unhealthy pod, SRE could successfully invoke the private actuator health
// endpoint(localhost:8181/actuator/health) and obtain a response showing that the
// service(as well as other components such as Cassandra, NATS, etc.) as "UP".
//
// Both, K8s probes and ELB health checks, are configured to use the private health
// endpoint to determine whether the pod is healthy. This explains why:
//   * K8s did not terminate the unhealthy pod, and
//   * ELB did not take the pod out of rotation but kept routing traffic to it
//
// All the HealthIndicators are exercised when the public and the private health
// endpoints are invoked. When this indicator is called as a result of invoking the
// public health endpoint, it will be a no-op. However, when this indicator is
// called as a result of invoking the private health endpoint, it will call the
// public health endpoint to check whether thread pool associated with port 8080 is
// exhausted or the app is possibly out of memory or process/resource limits reached.
// This will help K8s and ELB to do the right things.
@Slf4j
@Configuration
public class NvcfPublicHealthIndicator implements HealthIndicator {

    public static final String CLIENT_REGISTRATION_ID = "nvcf-public-health";

    private static final String MESG_PUBLIC_HEALTH_RESPONSE = "Public health response: %s";
    private static final String MESG_PUBLIC_HEALTH_CHECK_FAILED = "Public health check failed: %s";

    private final NvcfService nvcfService;

    public NvcfPublicHealthIndicator(
            WebClient.Builder webClientBuilder,
            ManagedHttpResources nvcfPublicHealthIndicatorHttpResources) {
        var webClient = webClientBuilder
                .baseUrl("http://localhost:8080")
                .clientConnector(nvcfPublicHealthIndicatorHttpResources.connector())
                .build();
        this.nvcfService = HttpServiceProxyFactory
                .builderFor(WebClientAdapter.create(webClient))
                .build()
                .createClient(NvcfService.class);
    }

    @Override
    public Health health() {
        var requestAttributes = RequestContextHolder.getRequestAttributes();
        if (requestAttributes == null) { // Should not happen. Keeps Sonar/IDE happy.
            return Health.down().build();
        }

        var request = ((ServletRequestAttributes) requestAttributes).getRequest();
        var port = request.getServerPort();
        if (port == 8080) {
            // HealthIndicator is called when the public health endpoint was invoked. Since the
            // request made it through, the app must be healthy.
            return Health.up().build();
        }

        // HealthIndicator is called when the private health endpoint was invoked. Invoke the
        // public health endpoint to check if the app is healthy.
        try {
            var response = nvcfService.health();
            if (response == null) {
                return Health.down().build();
            }
            log.info(MESG_PUBLIC_HEALTH_RESPONSE.formatted(response));
            return response.status().equals("UP") ? Health.up().build() : Health.down().build();
        } catch (Exception ex) {
            log.info(MESG_PUBLIC_HEALTH_CHECK_FAILED.formatted(ex.getMessage()));
            return Health.down().build();
        }
    }

    record NvcfHealthResponse(String status) { }

    interface NvcfService {
        @GetExchange("/health")
        NvcfHealthResponse health();
    }
}
