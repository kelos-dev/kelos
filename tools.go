//go:build tools

package tools

import (
	_ "github.com/google/yamlfmt/cmd/yamlfmt"
	_ "github.com/onsi/ginkgo/v2/ginkgo"
	_ "mvdan.cc/sh/v3/cmd/shfmt"
	_ "sigs.k8s.io/controller-runtime/tools/setup-envtest"
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen"
)
