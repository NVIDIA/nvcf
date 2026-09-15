/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Runtime configuration loaded from environment variables.
 */
#ifndef NVSNAP_CONFIG_H
#define NVSNAP_CONFIG_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    int      log_level;               /* 0=off .. 4=debug */
    int      metrics_enabled;         /* 0/1 */
    int      fault_injection_enabled; /* 0/1 */
    size_t   host_pool_size;          /* bytes */
    char     agent_socket_path[256];
    double   oversubscription_ratio;
    int      detailed_tracing;        /* 0/1 */
} NvSnapConfig;

void              nvsnap_config_init(void);
const NvSnapConfig *nvsnap_config_get(void);

/* Utility: parse a human-readable size string ("4G", "512M", "1024"). */
size_t nvsnap_parse_size(const char *str);

#ifdef __cplusplus
}
#endif

#endif /* NVSNAP_CONFIG_H */
