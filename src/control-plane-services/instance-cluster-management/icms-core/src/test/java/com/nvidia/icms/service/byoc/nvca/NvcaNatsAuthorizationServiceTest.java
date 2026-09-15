/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.service.byoc.nvca;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties.NatsAuth;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties.SubjectList;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties.SubjectPermissions;
import java.time.Duration;
import java.util.Collections;
import java.util.List;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

class NvcaNatsAuthorizationServiceTest {

    private static final String VALID_CLUSTER_ID = "c0b4d6b8-1234-4567-89ab-cdef01234567";

    private NvcaConfigurationProperties config;
    private NvcaNatsAuthorizationService service;

    @BeforeEach
    void setUp() {
        config = new NvcaConfigurationProperties();
        var natsAuth = new NatsAuth();
        natsAuth.setAccount("APP");
        natsAuth.setTtl(Duration.ofHours(1));

        var perms = new SubjectPermissions();
        var pub = new SubjectList();
        pub.setAllow(List.of(
                "$JS.API.CONSUMER.MSG.NEXT.CreateNvcaFunctionTaskStream."
                        + "CreateNvcaFunctionTaskStream-{clusterId}",
                "$JS.ACK.CreateNvcaFunctionTaskStream.CreateNvcaFunctionTaskStream-{clusterId}.>"));
        perms.setPublish(pub);

        var sub = new SubjectList();
        sub.setAllow(List.of("_INBOX.>"));
        perms.setSubscribe(sub);

        natsAuth.setPermissions(perms);
        config.setNatsAuth(natsAuth);

        service = new NvcaNatsAuthorizationService(config);
    }

    @Test
    void buildResponse_substitutesClusterIdInAllSubjectTemplates() {
        var resp = service.buildResponse(VALID_CLUSTER_ID);

        assertThat(resp.getAccount()).isEqualTo("APP");
        assertThat(resp.getUserId()).isEqualTo("cluster-" + VALID_CLUSTER_ID);
        assertThat(resp.getTtl()).isEqualTo(Duration.ofHours(1).toNanos());
        assertThat(resp.getPermissions().getPublish().getAllow())
                .containsExactly(
                        "$JS.API.CONSUMER.MSG.NEXT.CreateNvcaFunctionTaskStream."
                                + "CreateNvcaFunctionTaskStream-" + VALID_CLUSTER_ID,
                        "$JS.ACK.CreateNvcaFunctionTaskStream.CreateNvcaFunctionTaskStream-"
                                + VALID_CLUSTER_ID + ".>");
        assertThat(resp.getPermissions().getSubscribe().getAllow())
                .containsExactly("_INBOX.>");
    }

    @Test
    void buildResponse_rejectsClusterIdWithAclMetacharacters() {
        // Anything containing ACL-significant characters (`>`, `*`, `.`) must trip the
        // UUID validator — otherwise a malicious aud like `nvcf-icms:>` would widen
        // `nvcf.cluster.>.>` to match every cluster's subjects.
        assertThatThrownBy(() -> service.buildResponse("abc.def"))
                .isInstanceOf(IllegalArgumentException.class);
        assertThatThrownBy(() -> service.buildResponse("*"))
                .isInstanceOf(IllegalArgumentException.class);
        assertThatThrownBy(() -> service.buildResponse(">"))
                .isInstanceOf(IllegalArgumentException.class);
        assertThatThrownBy(() -> service.buildResponse("not-a-uuid"))
                .isInstanceOf(IllegalArgumentException.class);
    }

    @Test
    void buildResponse_rejectsNullOrEmptyClusterId() {
        assertThatThrownBy(() -> service.buildResponse(null))
                .isInstanceOf(IllegalArgumentException.class);
        assertThatThrownBy(() -> service.buildResponse(""))
                .isInstanceOf(IllegalArgumentException.class);
    }

    @Test
    void buildResponse_emptyTemplateLists_failClosed() {
        config.getNatsAuth().setPermissions(new SubjectPermissions());

        assertThatThrownBy(() -> service.buildResponse(VALID_CLUSTER_ID))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("NATS auth permissions");
    }

    @Test
    void buildResponse_missingNatsAuthConfig_failsClosed() {
        config.setNatsAuth(null);

        assertThatThrownBy(() -> service.buildResponse(VALID_CLUSTER_ID))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("NATS auth configuration");
    }

    @Test
    void buildResponse_broadAllowTemplateWithoutClusterPlaceholder_failsClosed() {
        var pub = new SubjectList();
        pub.setAllow(List.of("public.>"));
        config.getNatsAuth().getPermissions().setPublish(pub);

        assertThatThrownBy(() -> service.buildResponse(VALID_CLUSTER_ID))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("clusterId");
    }

    @Test
    void buildResponse_allowlistsStaticInboxSubscribeTemplate() {
        var pub = new SubjectList();
        pub.setAllow(List.of("nvcf.cluster.{clusterId}.>"));
        config.getNatsAuth().getPermissions().setPublish(pub);
        var sub = new SubjectList();
        sub.setAllow(List.of("_INBOX.>"));
        config.getNatsAuth().getPermissions().setSubscribe(sub);

        var resp = service.buildResponse(VALID_CLUSTER_ID);

        assertThat(resp.getPermissions().getSubscribe().getAllow()).containsExactly("_INBOX.>");
    }

    @Test
    void buildResponse_onlyStaticInboxAllow_failsClosed() {
        var pub = new SubjectList();
        pub.setAllow(List.of());
        config.getNatsAuth().getPermissions().setPublish(pub);
        var sub = new SubjectList();
        sub.setAllow(List.of("_INBOX.>"));
        config.getNatsAuth().getPermissions().setSubscribe(sub);

        assertThatThrownBy(() -> service.buildResponse(VALID_CLUSTER_ID))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("cluster-scoped");
    }

    @Test
    void buildResponse_blankDenyTemplate_failsClosed() {
        config.getNatsAuth().getPermissions().getPublish().setDeny(List.of(" "));

        assertThatThrownBy(() -> service.buildResponse(VALID_CLUSTER_ID))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("deny template");
    }

    @Test
    void buildResponse_nullDenyTemplate_failsClosed() {
        config.getNatsAuth().getPermissions().getPublish().setDeny(Collections.singletonList(null));

        assertThatThrownBy(() -> service.buildResponse(VALID_CLUSTER_ID))
                .isInstanceOf(IllegalStateException.class)
                .hasMessageContaining("deny template");
    }
}
