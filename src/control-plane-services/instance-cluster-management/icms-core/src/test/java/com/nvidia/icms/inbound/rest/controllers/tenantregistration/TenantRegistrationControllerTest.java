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
package com.nvidia.icms.inbound.rest.controllers.tenantregistration;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.doNothing;
import static org.mockito.Mockito.doThrow;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.delete;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import tools.jackson.databind.ObjectMapper;
import com.nvidia.icms.errors.IcmsBadRequestException;
import com.nvidia.icms.errors.IcmsInternalServerException;
import com.nvidia.icms.errors.IcmsNotFoundException;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationRequest;
import com.nvidia.icms.inbound.rest.model.tenantregistration.TenantRegistrationResponse;
import com.nvidia.icms.integration.IntegrationTest;
import com.nvidia.icms.service.tenantregistration.TenantRegistrationService;
import com.nvidia.icms.util.JwtKeyUtils;
import com.nvidia.icms.util.TestUtil;
import java.util.HashMap;
import java.util.Map;
import java.util.UUID;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.http.HttpHeaders;
import org.springframework.http.MediaType;
import org.springframework.security.access.AccessDeniedException;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

class TenantRegistrationControllerTest extends IntegrationTest {

    @Autowired
    private MockMvc mockMvc;

    @Autowired
    private ObjectMapper objectMapper;

    @MockitoBean
    private TenantRegistrationService tenantRegistrationService;

    private static final String BASE_URL = "/v1/si/accounts/{ncaId}/{tenant}/registrations";
    private static final String NCA_ID = "test-nca-id";
    private static final String TENANT = "gdn";
    private static final String TENANT_REGISTRATION_SCOPE = "tenant_registration";
    private static final String OTHER_SCOPE = "other_scope";

    @Test
    void register_success() throws Exception {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        UUID registrationId = UUID.randomUUID();
        TenantRegistrationResponse response = TenantRegistrationResponse.builder()
                .registrationId(registrationId)
                .build();

        when(tenantRegistrationService.register(eq(NCA_ID), eq(TENANT), any(TenantRegistrationRequest.class), any(Map.class)))
                .thenReturn(response);

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isCreated())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON))
                .andExpect(jsonPath("$.registrationId").value(registrationId.toString()));

        verify(tenantRegistrationService).register(eq(NCA_ID), eq(TENANT), any(TenantRegistrationRequest.class), any(Map.class));
    }

    @Test
    void register_withoutAuthority_returnsUnauthorized() throws Exception {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void register_withIncorrectScope_returnsForbidden() throws Exception {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, OTHER_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isForbidden());
    }

    @Test
    void register_withNullTenantRegistrationData_returnsBadRequest() throws Exception {
        // Prepare
        TenantRegistrationRequest request = TenantRegistrationRequest.builder()
                .tenantRegistrationData(null)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .build();

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isBadRequest());
    }

    @Test
    void register_withEmptyTenantRegistrationData_returnsBadRequest() throws Exception {
        // Prepare
        TenantRegistrationRequest request = TenantRegistrationRequest.builder()
                .tenantRegistrationData(new HashMap<>())
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .build();

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isBadRequest());
    }

    @Test
    void register_withNullFunctionId_returnsBadRequest() throws Exception {
        // Prepare
        Map<String, String> tenantData = new HashMap<>();
        tenantData.put("gdnAppId", "test-app-id");

        TenantRegistrationRequest request = TenantRegistrationRequest.builder()
                .tenantRegistrationData(tenantData)
                .functionId(null)
                .functionVersionId(UUID.randomUUID())
                .build();

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isBadRequest());
    }

    @Test
    void register_withNullFunctionVersionId_returnsBadRequest() throws Exception {
        // Prepare
        Map<String, String> tenantData = new HashMap<>();
        tenantData.put("gdnAppId", "test-app-id");

        TenantRegistrationRequest request = TenantRegistrationRequest.builder()
                .tenantRegistrationData(tenantData)
                .functionId(UUID.randomUUID())
                .functionVersionId(null)
                .build();

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isBadRequest());
    }

    @Test
    void register_invalidTenant_returnsBadRequest() throws Exception {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        
        doThrow(new IcmsBadRequestException("Invalid tenant: invalid-tenant"))
                .when(tenantRegistrationService).register(eq(NCA_ID), eq("invalid-tenant"), any(TenantRegistrationRequest.class), any(Map.class));

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, "invalid-tenant")
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isBadRequest());
    }

    @Test
    void register_serviceThrowsInternalServerError_returnsInternalServerError() throws Exception {
        // Prepare
        TenantRegistrationRequest request = createValidRequest();
        
        doThrow(new IcmsInternalServerException("Database error"))
                .when(tenantRegistrationService).register(eq(NCA_ID), eq(TENANT), any(TenantRegistrationRequest.class), any(Map.class));

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isInternalServerError());
    }

    @Test
    void register_withMalformedJson_returnsBadRequest() throws Exception {
        // Prepare
        String malformedJson = "{ invalid json }";

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(malformedJson))
                .andExpect(status().isBadRequest());
    }

    @Test
    void register_withMultipleTenantsData_success() throws Exception {
        // Prepare
        Map<String, String> tenantData = new HashMap<>();
        tenantData.put("gdnAppId", "test-app-id");
        tenantData.put("customField1", "value1");
        tenantData.put("customField2", "value2");

        TenantRegistrationRequest request = TenantRegistrationRequest.builder()
                .tenantRegistrationData(tenantData)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .build();

        UUID registrationId = UUID.randomUUID();
        TenantRegistrationResponse response = TenantRegistrationResponse.builder()
                .registrationId(registrationId)
                .build();

        when(tenantRegistrationService.register(eq(NCA_ID), eq(TENANT), any(TenantRegistrationRequest.class), any(Map.class)))
                .thenReturn(response);

        // Act & Assert
        mockMvc.perform(post(BASE_URL, NCA_ID, TENANT)
                        .contentType(MediaType.APPLICATION_JSON)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE))
                        .content(objectMapper.writeValueAsString(request)))
                .andExpect(status().isCreated())
                .andExpect(content().contentType(MediaType.APPLICATION_JSON))
                .andExpect(jsonPath("$.registrationId").value(registrationId.toString()));
    }

    @Test
    void delete_success() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        doNothing().when(tenantRegistrationService).delete(eq(NCA_ID), eq(TENANT), eq(registrationId), any(Map.class));

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isNoContent());

        verify(tenantRegistrationService).delete(eq(NCA_ID), eq(TENANT), eq(registrationId), any(Map.class));
    }

    @Test
    void delete_withoutAuthority_returnsUnauthorized() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void delete_withIncorrectScope_returnsForbidden() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, OTHER_SCOPE)))
                .andExpect(status().isForbidden());
    }

    @Test
    void delete_registrationNotFound_returnsNotFound() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        doThrow(new IcmsNotFoundException("Registration not found"))
                .when(tenantRegistrationService).delete(eq(NCA_ID), eq(TENANT), eq(registrationId), any(Map.class));

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isNotFound());
    }

    @Test
    void delete_differentTenant_returnsForbidden() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        doThrow(new AccessDeniedException("Registration doesn't belong to provided tenant"))
                .when(tenantRegistrationService).delete(eq(NCA_ID), eq(TENANT), eq(registrationId), any(Map.class));

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isForbidden())
                .andExpect(jsonPath("$.error").value("Registration doesn't belong to provided tenant"));
    }

    @Test
    void delete_differentNcaId_returnsForbidden() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        doThrow(new AccessDeniedException("Registration doesn't belong to provided ncaId"))
                .when(tenantRegistrationService).delete(eq(NCA_ID), eq(TENANT), eq(registrationId), any(Map.class));

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isForbidden())
                .andExpect(jsonPath("$.error").value("Registration doesn't belong to provided ncaId"));
    }

    @Test
    void delete_invalidTenant_returnsBadRequest() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        doThrow(new IcmsBadRequestException("Invalid tenant"))
                .when(tenantRegistrationService).delete(eq(NCA_ID), eq("invalid-tenant"), eq(registrationId), any(Map.class));

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, "invalid-tenant", registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isBadRequest());
    }

    @Test
    void delete_serviceThrowsInternalServerError_returnsInternalServerError() throws Exception {
        // Prepare
        UUID registrationId = UUID.randomUUID();
        doThrow(new IcmsInternalServerException("Database error"))
                .when(tenantRegistrationService).delete(eq(NCA_ID), eq(TENANT), eq(registrationId), any(Map.class));

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, registrationId)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isInternalServerError());
    }

    @Test
    void delete_invalidUuidFormat_returnsBadRequest() throws Exception {
        // Prepare
        String invalidUuid = "not-a-valid-uuid";

        // Act & Assert
        mockMvc.perform(delete(BASE_URL + "/{registrationId}", NCA_ID, TENANT, invalidUuid)
                        .header(HttpHeaders.AUTHORIZATION,
                                JwtKeyUtils.getAuthHeader(TestUtil.DUMMY_CUSTOMER_1, TENANT_REGISTRATION_SCOPE)))
                .andExpect(status().isBadRequest());
    }

    private TenantRegistrationRequest createValidRequest() {
        Map<String, String> tenantData = new HashMap<>();
        tenantData.put("gdnAppId", "test-app-id");
        
        return TenantRegistrationRequest.builder()
                .tenantRegistrationData(tenantData)
                .functionId(UUID.randomUUID())
                .functionVersionId(UUID.randomUUID())
                .build();
    }
}
