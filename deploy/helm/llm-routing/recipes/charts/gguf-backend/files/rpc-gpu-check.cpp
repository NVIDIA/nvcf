// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
#include "ggml.h"
#include "ggml-alloc.h"
#include "ggml-backend.h"
#include "ggml-rpc.h"
#include <algorithm>
#include <cmath>
#include <cstdio>
#include <stdexcept>
#include <string>
#include <vector>

int main(int argc, char ** argv) {
    if (argc < 2) throw std::runtime_error("At least one RPC endpoint is required");
    constexpr int k = 256, m = 128, n = 64;
    std::vector<float> a(k*m), b(k*n), expected(m*n), actual(m*n);
    for (int i = 0; i < k*m; ++i) a[i] = float(i % 17 - 8) / 8;
    for (int i = 0; i < k*n; ++i) b[i] = float(i % 11 - 5) / 8;
    for (int col = 0; col < n; ++col)
        for (int row = 0; row < m; ++row)
            for (int inner = 0; inner < k; ++inner)
                expected[col*m + row] += a[row*k + inner]*b[col*k + inner];
    for (int rank = 1; rank < argc; ++rank) {
        auto reg = ggml_backend_rpc_add_server(argv[rank]);
        if (!reg || ggml_backend_reg_dev_count(reg) != 1) throw std::runtime_error("Expected one remote GPU");
        auto dev = ggml_backend_reg_dev_get(reg, 0);
        std::string description = ggml_backend_dev_description(dev);
        auto type = ggml_backend_dev_type(dev);
        if (type != GGML_BACKEND_DEVICE_TYPE_GPU && type != GGML_BACKEND_DEVICE_TYPE_IGPU)
            throw std::runtime_error("Remote CPU fallback is forbidden");
        auto backend = ggml_backend_rpc_init(argv[rank], 0);
        if (!backend) throw std::runtime_error("RPC backend initialization failed");
        ggml_init_params params = {16*ggml_tensor_overhead() + ggml_graph_overhead_custom(16, false), nullptr, true};
        auto ctx = ggml_init(params);
        auto ta = ggml_new_tensor_2d(ctx, GGML_TYPE_F32, k, m);
        auto tb = ggml_new_tensor_2d(ctx, GGML_TYPE_F32, k, n);
        auto tc = ggml_mul_mat(ctx, ta, tb);
        ggml_mul_mat_set_prec(tc, GGML_PREC_F32);
        auto graph = ggml_new_graph_custom(ctx, 16, false);
        ggml_build_forward_expand(graph, tc);
        auto buffer = ggml_backend_alloc_ctx_tensors(ctx, backend);
        if (!buffer) throw std::runtime_error("Remote allocation failed");
        ggml_backend_tensor_set(ta, a.data(), 0, a.size()*sizeof(float));
        ggml_backend_tensor_set(tb, b.data(), 0, b.size()*sizeof(float));
        float maximum_error = 0;
        for (int repeat = 0; repeat < 3; ++repeat) {
            if (ggml_backend_graph_compute(backend, graph) != GGML_STATUS_SUCCESS)
                throw std::runtime_error("Remote graph execution failed");
            ggml_backend_tensor_get(tc, actual.data(), 0, actual.size()*sizeof(float));
            for (size_t i = 0; i < actual.size(); ++i) {
                if (!std::isfinite(actual[i])) throw std::runtime_error("Nonfinite GPU result");
                maximum_error = std::max(maximum_error, std::abs(actual[i] - expected[i]));
            }
        }
        if (maximum_error > 0.001f) throw std::runtime_error("Remote result differs from CPU reference");
        size_t free_bytes, total_bytes;
        ggml_backend_rpc_get_device_memory(argv[rank], 0, &free_bytes, &total_bytes);
        std::printf("{\"result\":\"PASS\",\"endpoint\":\"%s\",\"device\":\"%s\",\"repeats\":3,\"maximumAbsoluteError\":%.9g,\"freeBytes\":%zu,\"totalBytes\":%zu}\n",
                    argv[rank], description.c_str(), maximum_error, free_bytes, total_bytes);
        ggml_backend_buffer_free(buffer);
        ggml_free(ctx);
        ggml_backend_free(backend);
    }
}
