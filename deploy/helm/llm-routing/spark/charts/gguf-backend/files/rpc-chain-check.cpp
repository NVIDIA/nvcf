// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
#include "ggml.h"
#include "ggml-backend.h"
#include "ggml-backend-impl.h"
#include "ggml-cpu.h"
#include "ggml-cuda.h"
#include "ggml-rpc.h"
#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstring>
#include <stdexcept>
#include <vector>

static int cpu_graph_calls = 0;
static ggml_status reject_cpu_compute(ggml_backend_t, ggml_cgraph *) {
    ++cpu_graph_calls;
    return GGML_STATUS_FAILED;
}

int main(int argc, char ** argv) {
    if (argc != 3) throw std::runtime_error("Two distinct CUDA-only RPC endpoints are required");
    ggml_backend_t gpu[] = {std::strcmp(argv[1], "local") == 0 ? ggml_backend_cuda_init(0) : ggml_backend_rpc_init(argv[1], 0),
                           ggml_backend_rpc_init(argv[2], 0)};
    if (!gpu[0] || !gpu[1]) throw std::runtime_error("RPC initialization failed");
    auto cpu = ggml_backend_cpu_init();
    // The scheduler requires a CPU backend. Make any CPU graph execution fail.
    cpu->iface.graph_compute = reject_cpu_compute;
    constexpr int k = 256, m = 128, n = 64, p = 48;
    std::vector<float> a(k*m), b(k*n), c(m*p), middle(m*n), expected(p*n), actual(p*n);
    for (int direction = 0; direction < 2; ++direction) {
        auto first = gpu[direction], second = gpu[1-direction];
        ggml_backend_t backends[] = {first, second, cpu};
        auto scheduler = ggml_backend_sched_new(backends, nullptr, 3, 128, false, true);
        ggml_init_params params = {32*ggml_tensor_overhead() + ggml_graph_overhead_custom(128, false), nullptr, true};
        auto ctx = ggml_init(params);
        auto ta = ggml_new_tensor_2d(ctx, GGML_TYPE_F32, k, m);
        auto tb = ggml_new_tensor_2d(ctx, GGML_TYPE_F32, k, n);
        auto tc = ggml_new_tensor_2d(ctx, GGML_TYPE_F32, m, p);
        auto intermediate = ggml_mul_mat(ctx, ta, tb);
        auto final = ggml_mul_mat(ctx, tc, intermediate);
        ggml_mul_mat_set_prec(intermediate, GGML_PREC_F32);
        ggml_mul_mat_set_prec(final, GGML_PREC_F32);
        ggml_set_name(intermediate, "gpu_a_intermediate");
        ggml_set_name(final, "gpu_b_consumes_a");
        ggml_set_input(ta);
        ggml_set_input(tb);
        ggml_set_input(tc);
        ggml_set_output(final);
        auto graph = ggml_new_graph_custom(ctx, 128, false);
        ggml_build_forward_expand(graph, final);
        for (auto tensor : {ta, tb, intermediate}) ggml_backend_sched_set_tensor_backend(scheduler, tensor, first);
        for (auto tensor : {tc, final}) ggml_backend_sched_set_tensor_backend(scheduler, tensor, second);
        if (!ggml_backend_sched_alloc_graph(scheduler, graph)) throw std::runtime_error("Scheduler allocation failed");
        if (ggml_backend_sched_get_tensor_backend(scheduler, intermediate) != first ||
            ggml_backend_sched_get_tensor_backend(scheduler, final) != second)
            throw std::runtime_error("Requested GPU placement changed");
        if (ggml_backend_sched_get_n_splits(scheduler) != 2) throw std::runtime_error("Expected two GPU graph partitions");
        for (int repeat = 0; repeat < 3; ++repeat) {
            std::fill(middle.begin(), middle.end(), 0);
            std::fill(expected.begin(), expected.end(), 0);
            for (int i = 0; i < k*m; ++i) a[i] = float((i + 3*repeat) % 17 - 8) / 8;
            for (int i = 0; i < k*n; ++i) b[i] = float((i + repeat) % 11 - 5) / 8;
            for (int i = 0; i < m*p; ++i) c[i] = float((i + direction) % 7 - 3) / 8;
            for (int col = 0; col < n; ++col)
                for (int row = 0; row < m; ++row)
                    for (int inner = 0; inner < k; ++inner)
                        middle[col*m + row] += a[row*k + inner]*b[col*k + inner];
            for (int col = 0; col < n; ++col)
                for (int row = 0; row < p; ++row)
                    for (int inner = 0; inner < m; ++inner)
                        expected[col*p + row] += c[row*m + inner]*middle[col*m + inner];
            // Only original inputs are uploaded. The scheduler transfers the GPU-produced intermediate.
            ggml_backend_tensor_set(ta, a.data(), 0, a.size()*sizeof(float));
            ggml_backend_tensor_set(tb, b.data(), 0, b.size()*sizeof(float));
            ggml_backend_tensor_set(tc, c.data(), 0, c.size()*sizeof(float));
            if (ggml_backend_sched_graph_compute(scheduler, graph) != GGML_STATUS_SUCCESS)
                throw std::runtime_error("Dependent GPU graph failed");
            ggml_backend_tensor_get(final, actual.data(), 0, actual.size()*sizeof(float));
            float error = 0;
            double checksum = 0;
            for (size_t i = 0; i < actual.size(); ++i) {
                if (!std::isfinite(actual[i])) throw std::runtime_error("Nonfinite output");
                error = std::max(error, std::abs(actual[i] - expected[i]));
                checksum += actual[i];
            }
            if (error > 0.001f || cpu_graph_calls != 0) throw std::runtime_error("Incorrect result or CPU execution");
            std::printf("{\"result\":\"PASS\",\"firstGPU\":\"%s\",\"secondGPU\":\"%s\",\"repeat\":%d,\"maximumAbsoluteError\":%.9g,\"outputChecksum\":%.9g,\"intermediateBytes\":%zu,\"gpuPartitions\":2,\"cpuGraphCalls\":0}\n",
                        argv[direction+1], argv[2-direction], repeat, error, checksum, middle.size()*sizeof(float));
        }
        ggml_backend_sched_free(scheduler);
        ggml_free(ctx);
    }
    ggml_backend_free(cpu);
    for (auto backend : gpu) ggml_backend_free(backend);
}
