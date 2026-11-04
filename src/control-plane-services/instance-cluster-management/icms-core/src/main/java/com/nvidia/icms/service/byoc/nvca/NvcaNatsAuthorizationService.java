/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */
package com.nvidia.icms.service.byoc.nvca;

import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties.NatsAuth;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties.SubjectList;
import com.nvidia.icms.configuration.nvca.NvcaConfigurationProperties.SubjectPermissions;
import com.nvidia.icms.inbound.rest.model.nvca.NatsAuthorizeResponse;
import java.util.List;
import java.util.regex.Pattern;
import java.util.stream.Collectors;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.apache.commons.lang3.StringUtils;
import org.springframework.stereotype.Service;

/**
 * Builds the {@link NatsAuthorizeResponse} NATS permissions body from the
 * verified cluster ID.
 *
 * <p>Subject templates in {@code nvca.natsAuth.permissions.*.allow/deny} may
 * include the literal {@code {clusterId}} placeholder; this service validates
 * the clusterId against a strict UUID pattern before substitution so that
 * characters with NATS ACL semantics ({@code > * . ..} and whitespace) can
 * never slip in and widen a pattern unexpectedly.</p>
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class NvcaNatsAuthorizationService {

    private static final String CLUSTER_ID_PLACEHOLDER = "{clusterId}";
    private static final String STATIC_INBOX_SUBSCRIBE_ALLOW = "_INBOX.>";
    private static final String MESSAGE_INVALID_CLUSTER_ID = "clusterId is not a valid UUID";
    private static final String MESSAGE_MISSING_NATS_AUTH =
            "NATS auth configuration is missing";
    private static final String MESSAGE_MISSING_ALLOW_TEMPLATE =
            "NATS auth permissions require at least one cluster-scoped allow template";
    private static final String MESSAGE_BLANK_ALLOW_TEMPLATE =
            "NATS auth permissions contain a blank allow template";
    private static final String MESSAGE_INVALID_ALLOW_TEMPLATE =
            "NATS auth %s allow template must contain {clusterId}: %s";
    private static final String MESSAGE_BLANK_TEMPLATE =
            "NATS auth %s %s template cannot be blank";
    private static final String LOG_NON_UUID_CLUSTER_ID =
            "nats-authorize: refusing to substitute non-UUID clusterId into ACL templates: {}";

    /**
     * Strict UUID pattern. Matches what SIS's {@code ClusterCreationService}
     * produces and rejects anything that could break the ACL rules below.
     */
    static final Pattern VALID_CLUSTER_ID = Pattern.compile(
            "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
                    + "[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$");

    private final NvcaConfigurationProperties nvcaConfig;

    /**
     * @throws IllegalArgumentException if {@code clusterId} doesn't match the UUID pattern —
     *         callers should treat this as an unauthorized response, not a 500.
     * @throws IllegalStateException if configured permissions are empty, blank, or not
     *         scoped to the verified cluster ID.
     */
    public NatsAuthorizeResponse buildResponse(String clusterId) {
        if (clusterId == null || !VALID_CLUSTER_ID.matcher(clusterId).matches()) {
            // The verification pipeline upstream got clusterId from the signed
            // aud claim of a JWT we successfully verified; if it doesn't match
            // our UUID shape that's a SIS-side data-integrity problem, not a
            // caller problem. Log loudly and fail closed.
            log.warn(LOG_NON_UUID_CLUSTER_ID,
                    clusterId == null ? "null" : truncateForLog(clusterId));
            throw new IllegalArgumentException(MESSAGE_INVALID_CLUSTER_ID);
        }

        NatsAuth cfg = nvcaConfig.getNatsAuth();
        if (cfg == null) {
            log.error(MESSAGE_MISSING_NATS_AUTH);
            throw new IllegalStateException(MESSAGE_MISSING_NATS_AUTH);
        }
        SubjectPermissions perms = cfg.getPermissions();
        validateConfiguredPermissions(perms);

        NatsAuthorizeResponse.Permissions resolved = NatsAuthorizeResponse.Permissions.builder()
                .publish(resolveList(perms.getPublish(), clusterId))
                .subscribe(resolveList(perms.getSubscribe(), clusterId))
                .build();

        return NatsAuthorizeResponse.builder()
                .userId("cluster-" + clusterId)
                .account(cfg.getAccount())
                .permissions(resolved)
                .ttl(cfg.getTtl() != null ? cfg.getTtl().toNanos() : null)
                .build();
    }

    private void validateConfiguredPermissions(SubjectPermissions perms) {
        if (perms == null) {
            throw new IllegalStateException(MESSAGE_MISSING_ALLOW_TEMPLATE);
        }
        validateAllowTemplates("publish", perms.getPublish(), false);
        validateAllowTemplates("subscribe", perms.getSubscribe(), true);
        validateNonBlankTemplates("publish", "deny", getDenyTemplates(perms.getPublish()));
        validateNonBlankTemplates("subscribe", "deny", getDenyTemplates(perms.getSubscribe()));
        if (!hasClusterScopedAllow(perms.getPublish())
                && !hasClusterScopedAllow(perms.getSubscribe())) {
            throw new IllegalStateException(MESSAGE_MISSING_ALLOW_TEMPLATE);
        }
    }

    private boolean hasClusterScopedAllow(SubjectList src) {
        return src != null
                && !isEmpty(src.getAllow())
                && src.getAllow().stream()
                        .anyMatch(this::hasClusterIdPlaceholder);
    }

    private void validateAllowTemplates(
            String permissionType, SubjectList src, boolean allowStaticInbox) {
        if (src == null || isEmpty(src.getAllow())) {
            return;
        }
        for (var template : src.getAllow()) {
            if (StringUtils.isBlank(template)) {
                log.error(MESSAGE_BLANK_ALLOW_TEMPLATE);
                throw new IllegalStateException(MESSAGE_BLANK_ALLOW_TEMPLATE);
            }
            var hasClusterId = hasClusterIdPlaceholder(template);
            var isAllowedStaticInbox = allowStaticInbox
                    && STATIC_INBOX_SUBSCRIBE_ALLOW.equals(template);
            if (hasClusterId || isAllowedStaticInbox) {
                continue;
            }
            var message = MESSAGE_INVALID_ALLOW_TEMPLATE.formatted(
                    permissionType, truncateForLog(template));
            log.error(message);
            throw new IllegalStateException(message);
        }
    }

    private void validateNonBlankTemplates(
            String permissionType, String listType, List<String> templates) {
        if (isEmpty(templates)) {
            return;
        }
        for (var template : templates) {
            if (StringUtils.isBlank(template)) {
                var message = MESSAGE_BLANK_TEMPLATE.formatted(permissionType, listType);
                log.error(message);
                throw new IllegalStateException(message);
            }
        }
    }

    private NatsAuthorizeResponse.SubjectList resolveList(SubjectList src, String clusterId) {
        if (src == null || (isEmpty(src.getAllow()) && isEmpty(src.getDeny()))) {
            return null;
        }
        return NatsAuthorizeResponse.SubjectList.builder()
                .allow(substituteAll(src.getAllow(), clusterId))
                .deny(substituteAll(src.getDeny(), clusterId))
                .build();
    }

    private List<String> substituteAll(List<String> templates, String clusterId) {
        if (isEmpty(templates)) {
            return null;
        }
        return templates.stream()
                .map(t -> t.replace(CLUSTER_ID_PLACEHOLDER, clusterId))
                .collect(Collectors.toList());
    }

    private static List<String> getDenyTemplates(SubjectList src) {
        return src != null ? src.getDeny() : null;
    }

    private boolean hasClusterIdPlaceholder(String template) {
        return template != null && template.contains(CLUSTER_ID_PLACEHOLDER);
    }

    private static boolean isEmpty(List<String> list) {
        return list == null || list.isEmpty();
    }

    private static String truncateForLog(String s) {
        return s.length() > 40 ? s.substring(0, 40) + "..." : s;
    }
}
