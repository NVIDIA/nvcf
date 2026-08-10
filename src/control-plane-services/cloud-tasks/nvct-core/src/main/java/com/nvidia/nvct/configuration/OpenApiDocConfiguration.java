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
package com.nvidia.nvct.configuration;

import com.nvidia.nvct.rest.event.XAccountEventController;
import com.nvidia.nvct.rest.result.XAccountResultController;
import com.nvidia.nvct.rest.secret.XAccountSecretManagementController;
import com.nvidia.nvct.rest.task.XAccountTaskManagementController;
import io.swagger.v3.oas.models.OpenAPI;
import io.swagger.v3.oas.models.info.Contact;
import io.swagger.v3.oas.models.info.Info;
import jakarta.annotation.PostConstruct;
import java.util.List;
import org.apache.commons.lang3.StringUtils;
import org.springdoc.core.customizers.OpenApiCustomizer;
import org.springdoc.core.customizers.OperationCustomizer;
import org.springdoc.core.utils.SpringDocUtils;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
public class OpenApiDocConfiguration {

    private final boolean excludeSuperAdminApis;

    public OpenApiDocConfiguration(
            @Value("${nvct.openapi.exclude.super-admin-apis:true}") boolean excludeSuperAdminApis) {
        this.excludeSuperAdminApis = excludeSuperAdminApis;
    }

    @PostConstruct
    public void postConstruct() {
        // In general, we do NOT want details of our NVIDIA Super Admin endpoints to be exposed
        // via our OpenAPI Specs generated from the /v3/openapi endpoint. However, we still want
        // the ability to generate OpenAPI Specs that contain details for  all(Super Admin and
        // Account Admin) the endpoints to help our SRE and UI teams. So, we cannot just
        // use the @Hidden annotation in our cross-account controllers and exclude the docs for
        // Super Admin endpoints the OpenAPI Specs. We are planning on automating the process of
        // generating OpenAPI Specs from within a dedicated job in the CI pipeline. Once a MR is
        // merged and a new tag is ready, the job will generate OpenAPI Specs containing just
        // the Account Admin endpoints by doing this:
        //
        //    $ cd local_env; docker-compose up; cd ..
        //    $ java -Dspring.profiles.active=local -jar target/app.jar
        //    $ curl localhost:8080/v3/openapi > nvct-openapi.json
        //
        // Then, the job will terminate the app and generate uber OpenAPI Specs that will contain
        // details of both NVIDIA Super Admin and Account Admin endpoints like this:
        //
        //    $ java -Dspring.profiles.active=local \
        //           -Dnvct.openapi.exclude.super-admin-apis=false -jar target/app.jar
        //    $ curl localhost:8080/v3/openapi > full-nvct-openapi.json
        //
        // Once the job generates the two JSON files, it will shutdown the docker containers,
        // terminate the app, and automatically update the gitlab repo containing OpenAPI Specs
        // with the newly generated JSON files.
        if (excludeSuperAdminApis) {
            var superAdminControllers = List.of(XAccountEventController.class,
                                                 XAccountResultController.class,
                                                 XAccountTaskManagementController.class,
                                                 XAccountSecretManagementController.class)
                                                 .toArray(new Class[0]);
            SpringDocUtils.getConfig().addHiddenRestControllers(superAdminControllers);
        }
    }

    // Using Cloud Tasks as title instead of spring.application.name property. We cannot change
    // the value of spring.application.name property easily at this point.
    @SuppressWarnings("unused")
    @Bean
    public OpenAPI customOpenAPI(
            @Value("${spring.application.version}") String version) {
        var title = "Cloud Tasks";
        return new OpenAPI()
                .info(new Info().title(title)
                              .version(version)
                              .contact(new Contact().name("NVIDIA").url("https://www.nvidia.com/"))
                              .termsOfService("https://www.nvidia.com/en-us/legal_info"));

    }

    @SuppressWarnings("unused")
    @Bean
    public OperationCustomizer operationCustomizer() {
        // Replace '\n' introduced due to Java multi-line strings in the description
        // field of @Operation annotation.
        return (operation, handlerMethod) -> {
            var description = operation.getDescription();
            if (StringUtils.isNotBlank(description)) {
                operation.setDescription(description.replace("\n", " "));
            }
            return operation;
        };
    }

    @SuppressWarnings("unused")
    @Bean
    public OpenApiCustomizer openApiCustomizer() {
        // Replace '\n' introduced due to Java multi-line strings in the description
        // field of @Tag annotation.
        return openApi -> openApi.getTags().forEach(tag -> {
            var description = tag.getDescription();
            if (StringUtils.isNotBlank(description)) {
                tag.setDescription(description.replace("\n", " "));
            }
        });
    }

}
