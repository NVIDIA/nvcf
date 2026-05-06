#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

bazel build //proto/cel/expr:all

rm -vf ./*.pb.go

files=( $(bazel cquery //proto/cel/expr:expr_go_proto --output=starlark --starlark:expr="'\n'.join([f.path for f in target.output_groups.go_generated_srcs.to_list()])") )
for src in "${files[@]}";
do
  cp -v "${src}" ./
done
