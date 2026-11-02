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
package com.nvidia.nvcf.rest.function.management;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.put;

import com.nvidia.nvcf.configuration.exceptions.ValidationAwareExceptionHandler;
import com.nvidia.nvcf.service.account.AccountService;
import com.nvidia.nvcf.service.azp.AuthorizedPartiesService;
import com.nvidia.nvcf.service.ratelimit.RateLimiterService;
import io.micrometer.tracing.Tracer;
import java.util.UUID;
import java.util.stream.Stream;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.mock.web.MockHttpServletResponse;
import org.springframework.test.util.ReflectionTestUtils;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.setup.MockMvcBuilders;
import tools.jackson.databind.json.JsonMapper;

/**
 * An invalid update body is rejected with 400 before the service runs. The problem detail must
 * carry the class-level request validator's message, so the caller knows which field to fix.
 */
class UpdateFunctionValidationDetailTest {

    private static final JsonMapper JSON = new JsonMapper();

    private static final String UPDATE_URL =
            "/v2/nvcf/functions/" + UUID.randomUUID() + "/versions/" + UUID.randomUUID();

    private static final String IMMUTABLE_FIELDS =
            "'llmConfig.uris' and 'llmConfig.tokenizer' cannot be changed after creation";
    private static final String DUPLICATE_MODEL_NAMES =
            "duplicate model names are not allowed in 'modelUpdates'";

    private MockMvc mockMvc;

    @BeforeEach
    void setUp() {
        var advice = new ValidationAwareExceptionHandler();
        ReflectionTestUtils.setField(advice, "tracer", mock(Tracer.class));
        var controller = new FunctionManagementController(
                mock(AccountService.class),
                mock(AuthorizedPartiesService.class),
                mock(FunctionManagementFacade.class),
                mock(RateLimiterService.class),
                mock(Tracer.class));
        mockMvc = MockMvcBuilders.standaloneSetup(controller)
                .setControllerAdvice(advice)
                .build();
    }

    static Stream<Arguments> invalidUpdates() {
        return Stream.of(
                Arguments.of("llmConfig.uris set",
                             modelUpdates("""
                                     {"modelName": "m", "llmConfig": {"uris": ["/v1/responses"]}}"""),
                             IMMUTABLE_FIELDS),
                Arguments.of("llmConfig.tokenizer set",
                             modelUpdates("""
                                     {"modelName": "m", "llmConfig": {"tokenizer": "tok"}}"""),
                             IMMUTABLE_FIELDS),
                Arguments.of("empty llmConfig",
                             modelUpdates("""
                                     {"modelName": "m", "llmConfig": {}}"""),
                             "at least one of 'tokenRateLimit' or 'routingMethod' must be specified"),
                Arguments.of("duplicate modelName",
                             modelUpdates("""
                                     {"modelName": "m", "llmConfig": {"routingMethod": "round-robin"}},
                                     {"modelName": "m", "llmConfig": {"routingMethod": "round-robin"}}"""),
                             DUPLICATE_MODEL_NAMES),
                Arguments.of("no updatable field",
                             "{}",
                             "at least one of 'tags', 'rateLimit', 'modelUpdates', or "
                                     + "'llmInvocationConfig' must be specified"));
    }

    @ParameterizedTest(name = "{0}")
    @MethodSource("invalidUpdates")
    void invalidUpdateReturnsValidatorMessageInDetail(
            String name, String body, String expectedMessage) throws Exception {
        var response = sendUpdate(body);

        assertThat(response.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(detail(response)).contains(expectedMessage);
    }

    @Test
    void fieldAndRequestLevelErrorsAreBothReported() throws Exception {
        var response = sendUpdate(modelUpdates("""
                {"modelName": ""}, {"modelName": ""}"""));

        assertThat(response.getStatus()).isEqualTo(HttpStatus.BAD_REQUEST.value());
        assertThat(detail(response))
                .contains("field=modelUpdates[0].modelName")
                .contains(DUPLICATE_MODEL_NAMES);
    }

    private MockHttpServletResponse sendUpdate(String body) throws Exception {
        return mockMvc.perform(put(UPDATE_URL)
                                       .contentType(MediaType.APPLICATION_JSON)
                                       .content(body))
                .andReturn()
                .getResponse();
    }

    private static String detail(MockHttpServletResponse response) throws Exception {
        return JSON.readTree(response.getContentAsString()).get("detail").asString();
    }

    private static String modelUpdates(String entries) {
        return "{\"modelUpdates\": [" + entries + "]}";
    }
}
