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
package com.nvidia.icms.configuration.refresh;

import org.springframework.beans.BeansException;
import org.springframework.cloud.autoconfigure.RefreshAutoConfiguration;
import org.springframework.cloud.context.scope.refresh.RefreshScopeRefreshedEvent;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.event.EventListener;
import org.springframework.stereotype.Component;

/**
 * Eagerly re-creates every refresh-scoped bean after a context refresh and fails loudly when
 * one of them no longer binds.
 *
 * <p>{@code RefreshScope.refreshAll()} destroys all refresh-scoped beans without checking that
 * they can be rebuilt from the new environment. A bad value (for example a misspelled enum in
 * a {@code @ConfigurationProperties} bean) therefore stays latent until some request happens
 * to touch the affected bean, at which point bean creation fails mid-request. Worse, requests
 * that never touch the broken bean keep succeeding, so the service looks healthy while parts
 * of it are dead.
 *
 * <p>This listener runs inside {@code ContextRefresher.refresh()} (it is invoked synchronously
 * from {@code refreshAll()}), forces re-creation of every refresh-scoped bean while the caller
 * is still in the refresh path, and throws on failure. Throwing makes the refresh itself fail,
 * so the triggering component can report the real cause. The Kubernetes update strategy then
 * publishes a readiness refusal until a fixed configuration is applied. Any refresh-scoped bean
 * construction failure is treated as an invalid refreshed context because the service cannot
 * safely accept traffic when one of those beans is unavailable.
 */
@Component
public class RefreshScopeBeanValidator {

    private final ConfigurableApplicationContext context;

    public RefreshScopeBeanValidator(ConfigurableApplicationContext context) {
        this.context = context;
    }

    @EventListener(RefreshScopeRefreshedEvent.class)
    public void validateRefreshScopeBeans() {
        var beanFactory = context.getBeanFactory();
        for (String beanName : beanFactory.getBeanDefinitionNames()) {
            var definition = beanFactory.getBeanDefinition(beanName);
            if (!RefreshAutoConfiguration.REFRESH_SCOPE_NAME.equals(definition.getScope())) {
                continue;
            }
            try {
                beanFactory.getBean(beanName);
            } catch (BeansException failure) {
                throw new IllegalStateException(
                        "Configuration refresh left the context broken; refresh-scoped bean '"
                                + beanName + "' could not be re-created",
                        failure);
            }
        }
    }
}
