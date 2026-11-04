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
package com.nvidia.ess.auth.jwk;



import static com.nvidia.ess.constants.OpenTelemetryAttributes.URL_FULL_KEY;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.atLeast;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.KeyUse;
import com.nimbusds.jose.jwk.gen.RSAKeyGenerator;
import com.nvidia.ess.telemetry.TelemetryComponents;
import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import java.time.Duration;
import java.util.UUID;
import lombok.extern.slf4j.Slf4j;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mock;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;
import reactor.util.context.ContextView;

@ExtendWith(MockitoExtension.class)
@Slf4j
class JwkSetServiceTest {
    @Mock
    private WebClient.Builder webClientBuilder;
    @Mock
    private JwkCacheProperties jwkCacheProperties;
    @Mock
    private TelemetryComponents telemetryComponents;
    private final MeterRegistry meterRegistry = new SimpleMeterRegistry();

    @Mock
    private WebClient webClient;
    @Mock
    private WebClient.RequestHeadersUriSpec uriSpec;

    @Mock
    private WebClient.ResponseSpec responseSpec;

    @BeforeEach
    void setup() {
        when(webClientBuilder.build())
                .thenReturn(webClient);
        when(jwkCacheProperties.getMaxSize()).thenReturn(1000);
        when(jwkCacheProperties.getInitSize()).thenReturn(100);

        when(webClient.get())
                .thenReturn(uriSpec);

        when(uriSpec.uri(anyString()))
                .thenReturn(uriSpec);
        when(uriSpec.retrieve())
                .thenReturn(responseSpec);

    }

    @Test
    void getJwkSet_onTriggeringRefreshWithinExpiry_shouldRefreshNewValueWithoutBlocking() throws JOSEException {
        when(jwkCacheProperties.getExpireAfterWrite()).thenReturn(Duration.ofDays(1));
        when(jwkCacheProperties.getRefreshAfterWrite()).thenReturn(Duration.ofNanos(1));

        JwkSetService jwkSetService = new JwkSetService(webClientBuilder, jwkCacheProperties, meterRegistry, telemetryComponents);

        var jwkSet1 = new JWKSet(new RSAKeyGenerator(2048)
                .keyUse(KeyUse.SIGNATURE)
                .keyID(UUID.randomUUID().toString())
                .generate())
                .toPublicJWKSet();
        var jwkSet2 = new JWKSet(new RSAKeyGenerator(2048)
                .keyUse(KeyUse.SIGNATURE)
                .keyID(UUID.randomUUID().toString())
                .generate())
                .toPublicJWKSet();

        when(responseSpec.bodyToMono(String.class))
                .thenReturn(Mono.just(jwkSet1.toString()))
                // add delay to simulate I/O latency
                .thenReturn(Mono.delay(Duration.ofMillis(100)).thenReturn(jwkSet2.toString()));

        // blocked: fetch jwkSet1 and return jwkSet1
        StepVerifier.create(jwkSetService.getJwkSet("test"))
                .expectNext(jwkSet1)
                .verifyComplete();

        // not blocked: fetch jwkSet2 and return jwkSet1
        //  wait for the fetch of jwkSet2 to complete
        StepVerifier.create(jwkSetService.getJwkSet("test")
                        .delayElement(Duration.ofMillis(200)))
                .expectNext(jwkSet1)
                .verifyComplete();

        // not blocked: fetch jwkSet2 (again) and return jwkSet2
        StepVerifier.create(jwkSetService.getJwkSet("test"))
                .expectNext(jwkSet2)
                .verifyComplete();

        verify(telemetryComponents, atLeast(3))
                .setSpanAttribute(any(ContextView.class), eq(URL_FULL_KEY), eq("test"));
    }


    @Test
    void getJwkSet_onTriggeringExpiry_shouldLoadNewValue() throws JOSEException {
        when(jwkCacheProperties.getExpireAfterWrite()).thenReturn(Duration.ofNanos(1));
        when(jwkCacheProperties.getRefreshAfterWrite()).thenReturn(Duration.ofNanos(1));

        JwkSetService jwkSetService = new JwkSetService(webClientBuilder, jwkCacheProperties, meterRegistry, telemetryComponents);

        var jwkSet1 = new JWKSet(new RSAKeyGenerator(2048)
                .keyUse(KeyUse.SIGNATURE)
                .keyID(UUID.randomUUID().toString())
                .generate())
                .toPublicJWKSet();
        var jwkSet2 = new JWKSet(new RSAKeyGenerator(2048)
                .keyUse(KeyUse.SIGNATURE)
                .keyID(UUID.randomUUID().toString())
                .generate())
                .toPublicJWKSet();

        when(responseSpec.bodyToMono(String.class))
                .thenReturn(Mono.just(jwkSet1.toString()))
                .thenReturn(Mono.just(jwkSet2.toString()));

        // blocked: fetch jwkSet1 and return jwkSet1
        //  wait for scheduler to evict cache
        StepVerifier.create(jwkSetService.getJwkSet("test")
                        .delayElement(Duration.ofMillis(10)))
                .expectNext(jwkSet1)
                .verifyComplete();

        // evicted already
        // blocked: fetch jwkSet2 and return jwkSet2
        StepVerifier.create(jwkSetService.getJwkSet("test"))
                .expectNext(jwkSet2)
                .verifyComplete();

        verify(telemetryComponents, atLeast(2))
                .setSpanAttribute(any(ContextView.class), eq(URL_FULL_KEY), eq("test"));
    }
}
