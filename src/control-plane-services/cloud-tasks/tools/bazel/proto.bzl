"""Project protobuf rules whose generator versions match the application runtime."""

load(
    "@rules_proto_grpc//:defs.bzl",
    "ProtoPluginInfo",
    "proto_compile_attrs",
    "proto_compile_impl",
    "proto_compile_toolchains",
)

# rules_proto_grpc_java does not expose its bundled gRPC plugin as an attribute.
# Keep its proven compile implementation while replacing only that private tool.
nvct_java_grpc_compile = rule(
    implementation = proto_compile_impl,
    attrs = dict(
        proto_compile_attrs,
        _plugins = attr.label_list(
            providers = [ProtoPluginInfo],
            default = [
                Label("@rules_proto_grpc_java//:proto_plugin"),
                Label("//src/control-plane-services/cloud-tasks/tools/bazel:grpc_java_1_63_plugin"),
            ],
            cfg = "exec",
        ),
    ),
    toolchains = proto_compile_toolchains,
)
