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
package com.nvidia.icms.configuration.openapi;


import com.nvidia.icms.outbound.sqs.model.FunctionDetails;
import com.nvidia.icms.outbound.sqs.model.TaskDetails;
import com.nvidia.icms.outbound.sqs.model.byoc.ByocSqsMessageModel;
import io.swagger.v3.core.converter.ModelConverters;
import io.swagger.v3.oas.models.Components;
import io.swagger.v3.oas.models.media.Schema;
import lombok.extern.slf4j.Slf4j;
import org.springdoc.core.customizers.OpenApiCustomizer;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;

@Configuration
@Slf4j
/**
 * While adding custom class in openAPI we need to make sure that if the class contains
 * another class as dataType for field then we have to add that class as well in the openAPI
 * eg: {@link ByocSqsMessageModel} has {@link ByocSqsMessageModel.LaunchSpecification}
 * as dataType for one the field then we have to add {@link ByocSqsMessageModel.LaunchSpecification}
 * to the components schema
 */
public class OpenAPIConfiguration {

    @Bean
    public OpenApiCustomizer addMyCustomClassToSchema() {

        return openApi -> {

            Components components = openApi.getComponents();
            if (components == null) {
                components = new Components();
                openApi.setComponents(components);
            }

            addSchemaForByocSqsMessage(components);
        };
    }

    private void addSchemaForByocSqsMessage(Components components) {

        // Add schemas for ByocSqsMessageModel
        Schema<?> byocSqsMessageSchema = ModelConverters.getInstance()
                .read(ByocSqsMessageModel.class).values().iterator().next();

        Schema<?> byocLaunchSpecificationSchema = ModelConverters.getInstance()
                .read(ByocSqsMessageModel.ByocLaunchSpecification.class).values().iterator().next();

        var functionDetailsSchema = ModelConverters.getInstance().read(FunctionDetails.class)
                .values().iterator().next();

        Schema<?> taskDetailsSchema = ModelConverters.getInstance()
                .read(TaskDetails.class).values().iterator().next();

        // Keep schemaName as that of className
        components.addSchemas("ByocSqsMessageModel", byocSqsMessageSchema);
        components.addSchemas("ByocLaunchSpecification", byocLaunchSpecificationSchema);
        components.addSchemas("TaskDetails", taskDetailsSchema);
        components.addSchemas("FunctionDetails", functionDetailsSchema);
    }
}
