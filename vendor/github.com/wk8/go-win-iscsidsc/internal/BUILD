load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = [
        "conversion.go",
        "hydrate_helpers.go",
        "test_utils.go",
        "types.go",
        "win_api_utils.go",
    ],
    importmap = "k8s.io/kubernetes/vendor/github.com/wk8/go-win-iscsidsc/internal",
    importpath = "github.com/wk8/go-win-iscsidsc/internal",
    visibility = ["//visibility:public"],
    deps = [
        "//vendor/github.com/pkg/errors:go_default_library",
        "//vendor/github.com/wk8/go-win-iscsidsc:go_default_library",
        "//vendor/golang.org/x/sys/windows:go_default_library",
    ],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [":package-srcs"],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)
