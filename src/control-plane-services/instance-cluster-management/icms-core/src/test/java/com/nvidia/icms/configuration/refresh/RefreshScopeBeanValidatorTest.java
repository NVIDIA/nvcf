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

import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.BeanCreationException;
import org.springframework.beans.factory.config.BeanDefinition;
import org.springframework.beans.factory.config.ConfigurableListableBeanFactory;
import org.springframework.beans.factory.support.RootBeanDefinition;
import org.springframework.context.ConfigurableApplicationContext;

class RefreshScopeBeanValidatorTest {

    private final ConfigurableApplicationContext context =
            mock(ConfigurableApplicationContext.class);

    private final ConfigurableListableBeanFactory beanFactory =
            mock(ConfigurableListableBeanFactory.class);

    private final RefreshScopeBeanValidator validator =
            new RefreshScopeBeanValidator(context);

    @Test
    void passesWhenEveryRefreshScopedBeanCanBeRecreated() {
        when(context.getBeanFactory()).thenReturn(beanFactory);
        var singleton = new RootBeanDefinition();
        var scoped = new RootBeanDefinition();
        scoped.setScope("refresh");
        when(beanFactory.getBeanDefinitionNames())
                .thenReturn(new String[] {"singletonBean", "scopedTarget.configBean"});
        when(beanFactory.getBeanDefinition("singletonBean")).thenReturn(singleton);
        when(beanFactory.getBeanDefinition("scopedTarget.configBean")).thenReturn(scoped);
        when(beanFactory.getBean("scopedTarget.configBean")).thenReturn(new Object());

        validator.validateRefreshScopeBeans();
    }

    @Test
    void throwsWhenARefreshScopedBeanCannotBeRecreated() {
        when(context.getBeanFactory()).thenReturn(beanFactory);
        var scoped = new RootBeanDefinition();
        scoped.setScope("refresh");
        when(beanFactory.getBeanDefinitionNames())
                .thenReturn(new String[] {"scopedTarget.configBean"});
        when(beanFactory.getBeanDefinition("scopedTarget.configBean")).thenReturn(scoped);
        when(beanFactory.getBean("scopedTarget.configBean"))
                .thenThrow(new BeanCreationException("Could not bind properties"));

        assertThatThrownBy(validator::validateRefreshScopeBeans)
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("scopedTarget.configBean")
                .hasCauseInstanceOf(BeanCreationException.class)
                .hasRootCauseMessage("Could not bind properties");
    }

    @Test
    void ignoresNonRefreshScopedBeansEvenIfTheyWouldFail() {
        when(context.getBeanFactory()).thenReturn(beanFactory);
        BeanDefinition singleton = new RootBeanDefinition();
        when(beanFactory.getBeanDefinitionNames()).thenReturn(new String[] {"singletonBean"});
        when(beanFactory.getBeanDefinition("singletonBean")).thenReturn(singleton);

        validator.validateRefreshScopeBeans();
    }
}
