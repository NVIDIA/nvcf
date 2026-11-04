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

package com.nvidia.apikeys.config;

import lombok.extern.slf4j.Slf4j;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.http.client.SimpleClientHttpRequestFactory;
import org.springframework.retry.backoff.BackOffPolicy;
import org.springframework.retry.backoff.ExponentialRandomBackOffPolicy;
import org.springframework.retry.policy.SimpleRetryPolicy;
import org.springframework.retry.support.RetryTemplate;
import org.springframework.web.client.RestTemplate;

@Slf4j
@Configuration
public class OutboundClientConfiguration {

    private static final int MAX_ATTEMPTS = 3;
    private static final int CONNECT_TIMEOUT = 2000;
    private static final int READ_TIMEOUT = 5000;

    @Bean
    public RestTemplate OciAuthClient() {
        SimpleClientHttpRequestFactory requestFactory = new SimpleClientHttpRequestFactory();
        requestFactory.setConnectTimeout(CONNECT_TIMEOUT);
        requestFactory.setReadTimeout(READ_TIMEOUT);

        RestTemplate restTemplate = new RestTemplate();
        restTemplate.setRequestFactory(requestFactory);

        restTemplate.getInterceptors().add((request, body, execution) -> {
            RetryTemplate retryTemplate = new RetryTemplate();
            retryTemplate.setRetryPolicy(getRetryPolicy());
            retryTemplate.setBackOffPolicy(getBackOffPolicy());
            try {
                return retryTemplate.execute(context -> {
                    log.info("retry context={}", context);
                    return execution.execute(request, body);
                });
            } catch (Exception e) {
                log.error("Failed to retry request {}", request, e);
                throw new RuntimeException(e);
            }
        });

        return restTemplate;
    }

    private static BackOffPolicy getBackOffPolicy() {
        return new ExponentialRandomBackOffPolicy();
    }

    private static SimpleRetryPolicy getRetryPolicy() {
        return new SimpleRetryPolicy(MAX_ATTEMPTS);
    }
}
