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
package com.nvidia.ess.filter;

import static com.nvidia.ess.constants.Constants.MSG_ILLEGAL_URI;
import static com.nvidia.ess.constants.Constants.X_ESS_REQUEST_ID_HEADER;
import static com.nvidia.ess.util.TestConstants.TEST_ESS_REQUEST_ID;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doReturn;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.springframework.http.MediaType.APPLICATION_PROBLEM_JSON;

import tools.jackson.core.JacksonException;
import tools.jackson.databind.ObjectMapper;
import tools.jackson.databind.json.JsonMapper;
import com.nvidia.boot.exceptions.BadRequestException;
import com.nvidia.ess.exceptions.CustomExceptionHandler;
import com.nvidia.ess.exceptions.InternalErrorException;
import com.nvidia.ess.validator.NotBlankAndUriSafeValidationHelper;
import java.net.URI;
import java.net.URLDecoder;
import java.nio.charset.Charset;
import java.util.List;
import java.util.Map;
import lombok.SneakyThrows;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.mockito.Mockito;
import org.mockito.junit.jupiter.MockitoExtension;
import org.springframework.core.io.buffer.DataBuffer;
import org.springframework.core.io.buffer.DataBufferFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.http.ResponseEntity;
import org.springframework.http.server.reactive.ServerHttpRequest;
import org.springframework.http.server.reactive.ServerHttpResponse;
import org.springframework.security.web.firewall.StrictHttpFirewall;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;
import reactor.test.StepVerifier;

@ExtendWith(MockitoExtension.class)
class WebFilterTests {

    private UriPathValidationWebFilter pathValidationWebFilter;

    @BeforeEach
    void setUp() {

        // Real ValidationHelper with real StrictHttpFirewall
        var validationHelper = new NotBlankAndUriSafeValidationHelper();
        validationHelper.setHttpFirewall(new StrictHttpFirewall());

        // UriPathValidationWebFilter to be tested.
        pathValidationWebFilter = new UriPathValidationWebFilter();
        pathValidationWebFilter.setHelper(validationHelper);
    }

    private static ServerWebExchange mockRequestAndServerWebExchange(String rawTestUri) {
        // Mock the URI object to return raw and decoded forms of the URI path.
        var decodedTestUri = URLDecoder.decode(rawTestUri, Charset.defaultCharset());
        var uri = Mockito.mock(URI.class);
        doReturn(decodedTestUri).when(uri).getPath();
        doReturn(rawTestUri).when(uri).getRawPath();

        // Mock the request to return the mocked URI.
        var request = Mockito.mock(ServerHttpRequest.class);
        doReturn(uri).when(request).getURI();

        // Mock the ServerWebExchange to return the mocked request.
        var exchange = Mockito.mock(ServerWebExchange.class);
        doReturn(request).when(exchange).getRequest();

        return exchange;
    }

    @SneakyThrows
    private void runPathValidationPassFailTestCase(String rawTestUri, boolean passExpected) {

        // Mocked method-calls below double as verifiers that those methods were called with the
        // expected arguments matched. `verify(...)` invocations are unnecessary (Mockito strict-mode).

        var exchange = mockRequestAndServerWebExchange(rawTestUri);

        var chain = Mockito.mock(WebFilterChain.class);

        if (passExpected) {
            // If the validator passed, we expect the rest of the WebFilterChain to continue
            // execution.
            doReturn(Mono.empty()).when(chain).filter(any());

            // The `filter()` call should complete successfully.
            StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                    .expectComplete()
                    .verify();
        } else {

            // Expect validator to have failed. A response will need to be written to
            // the client by the WebFilter with further request-processing truncated.

            var ex = new BadRequestException(MSG_ILLEGAL_URI);
            var pd = ex.getBody();
            var headersFromErrorHandler = new HttpHeaders();
            // Test transfer of headers set by error-handler into the final response.
            headersFromErrorHandler.set(X_ESS_REQUEST_ID_HEADER, TEST_ESS_REQUEST_ID);

            // Error-handler is mocked to return a ResponseEntity for a BAD_REQUEST.
            var errorHandler = Mockito.mock(CustomExceptionHandler.class);
            doReturn(Mono.just(new ResponseEntity<Object>(pd, headersFromErrorHandler, HttpStatus.BAD_REQUEST)))
                    .when(errorHandler)
                    .handleException(any(), any());
            pathValidationWebFilter.setErrorHandler(errorHandler);

            // Real ObjectMapper.
            var objectMapper = new JsonMapper();
            pathValidationWebFilter.setObjectMapper(objectMapper);

            var responseHeaders = Mockito.mock(HttpHeaders.class);
            // Verify that the request-ID header set by the mocked error-handler (as a test) is transferred
            // into the final response.
            doNothing().when(responseHeaders).addAll(X_ESS_REQUEST_ID_HEADER, List.of(TEST_ESS_REQUEST_ID));
            // Verify that the correct content-type header is set in the final response (application/problem+json).
            doNothing().when(responseHeaders).setContentType(APPLICATION_PROBLEM_JSON);

            // Verify that the JSON-serialized ProblemDetail is the written response-body.
            var expectedResponseBody = objectMapper.writeValueAsString(pd).getBytes();
            var bf = Mockito.mock(DataBufferFactory.class);
            var db = Mockito.mock(DataBuffer.class);
            doReturn(db).when(bf).wrap(expectedResponseBody);

            // Finish mocking the response and verify that the correct status-code is set in the
            // final response.
            var response = Mockito.mock(ServerHttpResponse.class);
            doReturn(true).when(response).setStatusCode(HttpStatus.BAD_REQUEST);
            doReturn(responseHeaders).when(response).getHeaders();
            doReturn(bf).when(response).bufferFactory();
            doReturn(Mono.<Void>empty()).when(response).writeWith(any());

            // Inject the mocked response into the mocked ServerWebExchange.
            doReturn(response).when(exchange).getResponse();

            // Error-response-write was mocked as a success.
            StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                    .expectComplete()
                    .verify();
        }
    }

    @Test
    void testUriPathValidationWebFilter_invalidUriPath_errorHandlerBroke_internalServerError() {

        var exchange = mockRequestAndServerWebExchange("/uri/with-%25-invalid-chars");
        var chain = Mockito.mock(WebFilterChain.class);

        // Error-handler broke and threw an exception.
        var errorHandler = Mockito.mock(CustomExceptionHandler.class);
        doReturn(Mono.error(new RuntimeException()))
                .when(errorHandler)
                .handleException(any(), any());
        pathValidationWebFilter.setErrorHandler(errorHandler);

        // Expect InternalErrorException from WebFilter
        StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                .expectError(InternalErrorException.class)
                .verify();
    }

    @Test
    void testUriPathValidationWebFilter_invalidUriPath_errorHandlerReturnedNullResponseBody_internalServerError() {

        var exchange = mockRequestAndServerWebExchange("/uri/with-%25-invalid-chars");
        var chain = Mockito.mock(WebFilterChain.class);

        // Error-handler returned 400 but null response-body (which is unexpected).
        var errorHandler = Mockito.mock(CustomExceptionHandler.class);
        doReturn(Mono.just(new ResponseEntity<Object>(HttpStatus.BAD_REQUEST)))
                .when(errorHandler)
                .handleException(any(), any());
        pathValidationWebFilter.setErrorHandler(errorHandler);

        // Expect InternalErrorException from WebFilter
        StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                .expectError(InternalErrorException.class)
                .verify();
    }

    @Test
    void testUriPathValidationWebFilter_invalidUriPath_errorHandlerDidNotReturnProblemDetail_internalServerError() {
        var exchange = mockRequestAndServerWebExchange("/uri/with-%25-invalid-chars");
        var chain = Mockito.mock(WebFilterChain.class);

        // Error-handler returned 400 but response-body is not a ProblemDetail object (which is unexpected).
        var errorHandler = Mockito.mock(CustomExceptionHandler.class);
        doReturn(Mono.just(new ResponseEntity<Object>(Map.of("errorAttr", "value"), HttpStatus.BAD_REQUEST)))
                .when(errorHandler)
                .handleException(any(), any());
        pathValidationWebFilter.setErrorHandler(errorHandler);

        // Expect InternalErrorException from WebFilter
        StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                .expectError(InternalErrorException.class)
                .verify();
    }

    @Test
    @SneakyThrows
    void testUriPathValidationWebFilter_invalidUriPath_problemDetailJsonSerializationFailed_internalServerError() {
        var exchange = mockRequestAndServerWebExchange("/uri/with-%25-invalid-chars");
        var chain = Mockito.mock(WebFilterChain.class);

        // Error-handler returned 400 with a ProblemDetail object.
        var errorHandler = Mockito.mock(CustomExceptionHandler.class);
        doReturn(Mono.just(new ResponseEntity<Object>(ProblemDetail.forStatusAndDetail(HttpStatus.BAD_REQUEST, MSG_ILLEGAL_URI), HttpStatus.BAD_REQUEST)))
                .when(errorHandler)
                .handleException(any(), any());
        pathValidationWebFilter.setErrorHandler(errorHandler);

        // However JSON-serialization of the ProblemDetail failed.
        var objectMapper = Mockito.mock(ObjectMapper.class);
        // JacksonException is abstract, so Mockito cannot instantiate it from the `Class` form of
        // doThrow (reflective instantiation fails -> MockitoException). Throw a concrete anonymous
        // subclass instead (same pattern as SecretControllerTest).
        doThrow(new JacksonException("simulated serialization failure") {})
                .when(objectMapper).writeValueAsString(any());
        pathValidationWebFilter.setObjectMapper(objectMapper);

        // Expect InternalErrorException from WebFilter
        StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                .expectError(InternalErrorException.class)
                .verify();
    }

    @Test
    @SneakyThrows
    void testUriPathValidationFilter_invalidUriPath_noHeadersFromResponseEntity_noErrorFail_400() {
        var exchange = mockRequestAndServerWebExchange("/uri/with-%25-invalid-chars");
        var chain = Mockito.mock(WebFilterChain.class);

        // Error-handler returned 400 with a ProblemDetail object but the returned ResponseEntity
        // had a `null` HttpHeaders instance (no headers).
        var errorHandler = Mockito.mock(CustomExceptionHandler.class);
        var pd = new BadRequestException(MSG_ILLEGAL_URI).getBody();
        doReturn(Mono.just(new ResponseEntity<Object>(pd, HttpStatus.BAD_REQUEST)))
                .when(errorHandler)
                .handleException(any(), any());
        pathValidationWebFilter.setErrorHandler(errorHandler);

        // Real ObjectMapper.
        var objectMapper = new JsonMapper();
        pathValidationWebFilter.setObjectMapper(objectMapper);

        // Verify that the JSON-serialized ProblemDetail is the written response-body.
        var expectedResponseBody = objectMapper.writeValueAsString(pd).getBytes();
        var bf = Mockito.mock(DataBufferFactory.class);
        var db = Mockito.mock(DataBuffer.class);
        doReturn(db).when(bf).wrap(expectedResponseBody);

        var responseHeaders = Mockito.mock(HttpHeaders.class);
        // Verify that the correct content-type header is set in the final response (application/problem+json).
        doNothing().when(responseHeaders).setContentType(APPLICATION_PROBLEM_JSON);

        // Finish mocking the response and verify that the correct status-code is set in the
        // final response.
        var response = Mockito.mock(ServerHttpResponse.class);
        doReturn(true).when(response).setStatusCode(HttpStatus.BAD_REQUEST);
        doReturn(responseHeaders).when(response).getHeaders();
        doReturn(bf).when(response).bufferFactory();
        doReturn(Mono.<Void>empty()).when(response).writeWith(any());

        // Inject the mocked response into the mocked ServerWebExchange.
        doReturn(response).when(exchange).getResponse();

        // Error-response-write was mocked as a success.
        StepVerifier.create(pathValidationWebFilter.filter(exchange, chain))
                .expectComplete()
                .verify();

        // Since the ResponseEntity had a `null` HttpHeaders instance, verify that
        // no headers were transferred from the ResponseEntity into the final response.
        verify(responseHeaders, never()).addAll(any(), any());
    }

    @Test
    void testPathValidationWebFilter_validUriPaths_noErrorPass_proceedToRestOfWebFilterChain() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample", true);
        runPathValidationPassFailTestCase("/sys/namespaces/sample:with:colon", true);
        runPathValidationPassFailTestCase("/sys/namespaces/sample.with.dot", true);
        runPathValidationPassFailTestCase("/sys/namespaces/sample%20with+encoded%20space", true);
        runPathValidationPassFailTestCase("/sys/namespaces/sample_with_underscores", true);
        runPathValidationPassFailTestCase("/sys/namespaces/sample%5fwith%5Fencoded-underscores", true);        
    }

    @Test
    void testPathValidationWebFilter_uriPathHasEncodedOrDoubleSlashes_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample//with-double-slash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with%2f%2fencoded-double-slash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with%2F%2Fencoded-double-slash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with%2Fencoded-slash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with%2fencoded-slash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%252ftwice-encoded-slash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%252Ftwice-encoded-slash", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasSemicolons_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample;with-semicolon", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-%3B-with-encoded-semicolon", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-%3b-with-encoded-semicolon", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-%253B-with-twice-encoded-semicolon", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-%253b-with-twice-encoded-semicolon", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasBackslashes_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-\\-backslash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%5c-encoded-backslash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%5C-encoded-backslash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%255c-twice-encoded-backslash", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%255C-twice-encoded-backslash", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasNulls_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-null-\0", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded-null-%00", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded-null-%2500", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasLinefeeds_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-lf-\n", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded-lf-%0a", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded-lf-%0A", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded-lf-%0a", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded-lf-%0A", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasCarriageReturns_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-cr-\r", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded-cr-%0d", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded-cr-%0D", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded-cr-%250d", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded-cr-%250D", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasEncodedDots_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded%2edot", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-encoded%2Edot", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded%252edot", false);
        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-twice-encoded%252Edot", false);
    }

    @Test
    void testPathValidationWebFilter_uriPathHasEncodedModulo_noErrorFail_400() {

        runPathValidationPassFailTestCase("/sys/namespaces/sample-with-%25-encoded-modulo", false);
    }
}
