/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for nvsnap-cuda-checkpoint's --gpu-map parsing.
 *
 * The parsing decides which physical GPU a restore lands on. Getting it wrong
 * does not fail loudly -- it either rejects a valid map (restore blocked) or,
 * worse, builds a plausible-looking map with transposed UUIDs and migrates the
 * process onto the wrong device. Neither is visible without a GPU, so it is
 * tested here against stubs instead of only on a live node.
 *
 * Builds the real translation unit with main() renamed, so the tests exercise
 * the shipped code rather than a copy of it.
 *
 * Run: docker/agent/tests/run_gpu_map_test.sh
 */
#define main nvsnap_cuda_checkpoint_main
#include "../nvsnap-cuda-checkpoint.c"
#undef main

#include <assert.h>

/* ---- CUDA stubs ---------------------------------------------------------
 * Eight fake devices whose UUIDs are byte i repeated, so a mis-mapped pair is
 * obvious in a failure message rather than an opaque hex diff.
 */
#define FAKE_DEVICES 8
static int g_visible = FAKE_DEVICES;

CUresult cuGetErrorString(CUresult r, const char **s) { (void)r; *s = "stub"; return CUDA_SUCCESS; }
CUresult cuInit(unsigned int f) { (void)f; return CUDA_SUCCESS; }

CUresult cuDeviceGetCount(int *n) { *n = g_visible; return CUDA_SUCCESS; }

CUresult cuDeviceGet(CUdevice *d, int ordinal)
{
    if (ordinal < 0 || ordinal >= g_visible) return CUDA_ERROR_INVALID_DEVICE;
    *d = ordinal;
    return CUDA_SUCCESS;
}

CUresult cuDeviceGetUuid(CUuuid *u, CUdevice d)
{
    if (d < 0 || d >= g_visible) return CUDA_ERROR_INVALID_DEVICE;
    memset(u->bytes, (char)(0xA0 + d), 16);
    return CUDA_SUCCESS;
}

/* Unused by these tests, but the translation unit references them. */
CUresult cuCheckpointProcessLock(int p, CUcheckpointLockArgs *a) { (void)p; (void)a; return CUDA_SUCCESS; }
CUresult cuCheckpointProcessCheckpoint(int p, CUcheckpointCheckpointArgs *a) { (void)p; (void)a; return CUDA_SUCCESS; }
CUresult cuCheckpointProcessRestore(int p, CUcheckpointRestoreArgs *a) { (void)p; (void)a; return CUDA_SUCCESS; }
CUresult cuCheckpointProcessUnlock(int p, CUcheckpointUnlockArgs *a) { (void)p; (void)a; return CUDA_SUCCESS; }
CUresult cuCheckpointProcessGetState(int p, CUprocessState *s) { (void)p; *s = CU_PROCESS_STATE_RUNNING; return CUDA_SUCCESS; }
CUresult cuCheckpointProcessGetRestoreThreadId(int p, int *t) { (void)p; *t = 1; return CUDA_SUCCESS; }

/* ---- harness ---------------------------------------------------------- */
static int passed, failed;

static void reset(void)
{
    free(g_pairs);
    g_pairs = NULL;
    g_pairs_count = 0;
    g_visible = FAKE_DEVICES;
}

static void ok(const char *name, int cond)
{
    if (cond) { passed++; printf("  ok   %s\n", name); }
    else      { failed++; printf("  FAIL %s\n", name); }
}

static int uuid_is(const CUuuid *u, unsigned char want)
{
    for (int i = 0; i < 16; i++)
        if ((unsigned char)u->bytes[i] != want) return 0;
    return 1;
}

/* Silence the parser's diagnostics for the cases that are meant to fail. */
static void quiet(void)
{
    if (!freopen("/dev/null", "w", stderr))
        perror("freopen");
}

int main(void)
{
    printf("gpu_map parsing\n");

    /* Index form: the convenience path for same-node testing. Resolving to
     * UUIDs here is what stops an index leaking through to the driver, where
     * it would mean a different device on the restore node. */
    reset();
    ok("accepts an all-index map", parse_gpu_map("0:1,1:2,2:3,3:4,4:5,5:6,6:7,7:0") == 0);
    ok("resolves index to UUID (old)", uuid_is(&g_pairs[0].oldUuid, 0xA0));
    ok("resolves index to UUID (new)", uuid_is(&g_pairs[0].newUuid, 0xA1));
    ok("keeps pair order", g_pairs_count == 8 && uuid_is(&g_pairs[7].newUuid, 0xA0));

    /* Identity is the case that pins every GPU in place; it must be expressible,
     * because a partial map is rejected by the driver. */
    reset();
    ok("accepts identity map", parse_gpu_map("0:0,1:1,2:2,3:3,4:4,5:5,6:6,7:7") == 0);

    /* UUID forms, as emitted by nvidia-smi and the k8s device plugin. */
    reset();
    g_visible = 1;
    ok("accepts GPU- prefixed dashed UUID",
       parse_gpu_map("GPU-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa:"
                     "GPU-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb") == 0);
    ok("parses prefixed UUID bytes", uuid_is(&g_pairs[0].oldUuid, 0xAA));

    reset();
    g_visible = 1;
    ok("accepts bare 32-hex UUID",
       parse_gpu_map("cccccccccccccccccccccccccccccccc:dddddddddddddddddddddddddddddddd") == 0);
    ok("parses bare UUID bytes", uuid_is(&g_pairs[0].newUuid, 0xDD));

    reset();
    g_visible = 1;
    ok("accepts uppercase hex",
       parse_gpu_map("EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE:FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF") == 0);
    ok("uppercase parses to same bytes as lowercase", uuid_is(&g_pairs[0].oldUuid, 0xEE));

    quiet();

    /* Every visible GPU must be mapped. A short map is the easy mistake, and
     * the driver's own error for it is far less specific than ours. */
    reset();
    ok("rejects a map shorter than the visible GPU count", parse_gpu_map("0:1") != 0);

    reset();
    g_visible = 2;
    ok("rejects a map longer than the visible GPU count", parse_gpu_map("0:1,1:0,0:1") != 0);

    /* Malformed input must be refused before any state transition. */
    reset();
    g_visible = 1;
    ok("rejects a pair with no colon", parse_gpu_map("0") != 0);

    reset();
    g_visible = 1;
    ok("rejects a short UUID", parse_gpu_map("abcd:0") != 0);

    reset();
    g_visible = 1;
    ok("rejects non-hex characters", parse_gpu_map(
       "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz:0") != 0);

    reset();
    g_visible = 1;
    ok("rejects an out-of-range device index", parse_gpu_map("99:0") != 0);

    reset();
    g_visible = 1;
    ok("rejects an empty side", parse_gpu_map(":0") != 0);

    printf("\n%d passed, %d failed\n", passed, failed);
    return failed ? 1 : 0;
}
