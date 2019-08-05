load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = [
        "types.go",
        "win_api_call_error.go",
    ],
    importmap = "k8s.io/kubernetes/vendor/github.com/wk8/go-win-iscsidsc",
    importpath = "github.com/wk8/go-win-iscsidsc",
    visibility = ["//visibility:public"],
    deps = ["//vendor/github.com/google/uuid:go_default_library"],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [
        ":package-srcs",
        "//vendor/github.com/wk8/go-win-iscsidsc/internal:all-srcs",
        "//vendor/github.com/wk8/go-win-iscsidsc/session:all-srcs",
        "//vendor/github.com/wk8/go-win-iscsidsc/target:all-srcs",
        "//vendor/github.com/wk8/go-win-iscsidsc/targetportal:all-srcs",
    ],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)
