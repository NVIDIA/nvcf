/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.nvcf.service.function;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.verifyNoInteractions;
import static org.mockito.Mockito.verifyNoMoreInteractions;
import static org.mockito.Mockito.when;

import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.nvcf.configuration.llm.LlmRoutingExpressionsProperties;
import com.nvidia.nvcf.persistence.function.entity.ApiBodyFormat;
import com.nvidia.nvcf.persistence.function.entity.FunctionEntity;
import com.nvidia.nvcf.persistence.function.entity.FunctionStatus;
import com.nvidia.nvcf.persistence.function.entity.FunctionType;
import com.nvidia.nvcf.rest.function.management.dto.CreateFunctionRequest;
import com.nvidia.nvcf.rest.function.management.dto.FunctionModelDto;
import com.nvidia.nvcf.rest.function.management.dto.FunctionTypeEnum;
import com.nvidia.nvcf.rest.function.management.dto.UpdateFunctionRequest;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.EnumSource;
import org.mockito.InjectMocks;
import org.mockito.Mock;
import org.mockito.Spy;
import org.mockito.junit.jupiter.MockitoExtension;

@ExtendWith(MockitoExtension.class)
class FunctionLlmServiceRoutingMethodTest {

    @Mock
    private FunctionLookupService functionLookupService;
    @Mock
    private FunctionMapperService functionMapperService;
    @Spy
    private LlmRoutingMethodValidator llmRoutingMethodValidator =
            new LlmRoutingMethodValidator(new LlmRoutingExpressionsProperties());
    @InjectMocks
    private FunctionLlmService service;

    @Test
    void validatesEveryLlmModelOnce() {
        var request = CreateFunctionRequest.builder()
                .name("test-function")
                .inferenceUrl(URI.create("/v1/chat/completions"))
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(model("first", "pulsar"), model("second", "Power_Of_Two")))
                .build();

        service.validateCreateFunctionRequestWithLlmConfig(request);

        verify(llmRoutingMethodValidator).validate("first", "pulsar");
        verify(llmRoutingMethodValidator).validate("second", "Power_Of_Two");
        verifyNoMoreInteractions(llmRoutingMethodValidator);
    }

    @ParameterizedTest
    @EnumSource(value = FunctionTypeEnum.class, names = {"DEFAULT", "STREAMING"})
    void skipsRoutingValidationForNonLlmFunctions(FunctionTypeEnum functionType) {
        var request = CreateFunctionRequest.builder()
                .name("test-function")
                .inferenceUrl(URI.create("/v1/chat/completions"))
                .functionType(functionType)
                .models(List.of(model("first", "pulsar;seed=x")))
                .build();

        service.validateCreateFunctionRequestWithLlmConfig(request);

        verifyNoInteractions(llmRoutingMethodValidator);
    }

    @Test
    void createUsesGateBeforeGrammar() {
        var request = CreateFunctionRequest.builder()
                .name("test-function")
                .inferenceUrl(URI.create("/v1/chat/completions"))
                .functionType(FunctionTypeEnum.LLM)
                .models(List.of(model("first", "round robin;seed=?1")))
                .build();

        assertThatThrownBy(() -> service.validateCreateFunctionRequestWithLlmConfig(request))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("first")
                .hasMessageContaining("not enabled");
    }

    @Test
    void updateUsesGateBeforeGrammarAndPreservesStoredValueOnRejection() {
        var function = FunctionEntity.builder()
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .ncaId("test-account")
                .functionName("test-function")
                .functionStatus(FunctionStatus.INACTIVE)
                .inferenceUrl("/v1/chat/completions")
                .apiBodyFormat(ApiBodyFormat.CUSTOM)
                .functionType(FunctionType.LLM)
                .modelSpecs(Map.of())
                .build();
        var storedModel = model("first", "pulsar");
        when(functionMapperService.toFunctionModels(function.getModelSpecs()))
                .thenReturn(List.of(storedModel));
        var request = UpdateFunctionRequest.builder()
                .modelUpdates(List.of(UpdateFunctionRequest.ModelUpdateDto.builder()
                        .modelName("first")
                        .llmConfig(UpdateFunctionRequest.LlmConfigUpdateDto.builder()
                                .routingMethod("round robin;seed=?1")
                                .build())
                        .build()))
                .build();

        assertThatThrownBy(() -> service.applyLlmUpdates(function, request))
                .isInstanceOf(BadRequestException.class)
                .hasMessageContaining("first")
                .hasMessageContaining("not enabled");
        assertThat(storedModel.getLlmConfig().getRoutingMethod()).isEqualTo("pulsar");
    }

    private static FunctionModelDto model(String name, String routingMethod) {
        return FunctionModelDto.builder()
                .name(name)
                .llmConfig(FunctionModelDto.LlmConfigDto.builder()
                        .uris(List.of("/v1/chat/completions"))
                        .routingMethod(routingMethod)
                        .build())
                .build();
    }
}
