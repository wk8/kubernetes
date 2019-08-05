load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = [
        "add.go",
        "list.go",
        "remove.go",
    ],
    importmap = "k8s.io/kubernetes/vendor/github.com/wk8/go-win-iscsidsc/targetportal",
    importpath = "github.com/wk8/go-win-iscsidsc/targetportal",
    visibility = ["//visibility:public"],
    deps = [
        "//vendor/github.com/pkg/errors:go_default_library",
        "//vendor/github.com/wk8/go-win-iscsidsc:go_default_library",
        "//vendor/github.com/wk8/go-win-iscsidsc/internal:go_default_library",
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
