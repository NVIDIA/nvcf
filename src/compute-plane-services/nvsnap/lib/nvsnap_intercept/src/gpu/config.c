/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

/*
 * NvSnap — Configuration loading from environment variables.
 */
#include "nvsnap/gpu/config.h"

#include <ctype.h>
#include <stdlib.h>
#include <string.h>

static NvSnapConfig g_config;
static int g_initialized = 0;

/* Parse a size string like "4G", "512M", "1024K", or plain bytes. */
size_t nvsnap_parse_size(const char *str)
{
    if (!str || !*str)
        return 0;

    char *end = NULL;
    unsigned long long val = strtoull(str, &end, 10);

    if (end && *end) {
        switch (toupper((unsigned char)*end)) {
        case 'K': val *= 1024ULL; break;
        case 'M': val *= 1024ULL * 1024ULL; break;
        case 'G': val *= 1024ULL * 1024ULL * 1024ULL; break;
        case 'T': val *= 1024ULL * 1024ULL * 1024ULL * 1024ULL; break;
        default: break;
        }
    }
    return (size_t)val;
}

static int env_int(const char *name, int def)
{
    const char *v = getenv(name);
    if (!v || !*v)
        return def;
    return atoi(v);
}

static double env_double(const char *name, double def)
{
    const char *v = getenv(name);
    if (!v || !*v)
        return def;
    return atof(v);
}

static size_t env_size(const char *name, size_t def)
{
    const char *v = getenv(name);
    if (!v || !*v)
        return def;
    return nvsnap_parse_size(v);
}

static void env_str(const char *name, char *dst, size_t dst_size, const char *def)
{
    const char *v = getenv(name);
    if (!v || !*v)
        v = def;
    if (v) {
        strncpy(dst, v, dst_size - 1);
        dst[dst_size - 1] = '\0';
    } else {
        dst[0] = '\0';
    }
}

void nvsnap_config_init(void)
{
    if (g_initialized)
        return;

    memset(&g_config, 0, sizeof(g_config));

    g_config.log_level               = env_int("NVSNAP_GPU_LOG_LEVEL", 0);
    g_config.metrics_enabled         = env_int("NVSNAP_METRICS", 1);
    g_config.fault_injection_enabled = env_int("NVSNAP_FAULT_INJECTION", 0);
    g_config.host_pool_size          = env_size("NVSNAP_HOST_POOL_SIZE", 0);
    g_config.oversubscription_ratio  = env_double("NVSNAP_OVERSUBSCRIPTION_RATIO", 1.0);
    g_config.detailed_tracing        = env_int("NVSNAP_DETAILED_TRACING", 0);

    env_str("NVSNAP_AGENT_SOCKET", g_config.agent_socket_path,
            sizeof(g_config.agent_socket_path),
            "/tmp/nvsnap_agent.sock");

    g_initialized = 1;
}

const NvSnapConfig *nvsnap_config_get(void)
{
    if (!g_initialized)
        nvsnap_config_init();
    return &g_config;
}
