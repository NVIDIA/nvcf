# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

#go test -run=none -file bench_test.go -test.bench . -cpuprofile=bench_test.out

go test -c
./go-restful.test -test.run=none -test.cpuprofile=tmp.prof -test.bench=BenchmarkMany
./go-restful.test -test.run=none -test.cpuprofile=curly.prof -test.bench=BenchmarkManyCurly

#go tool pprof go-restful.test tmp.prof
go tool pprof go-restful.test curly.prof


