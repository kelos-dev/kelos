package integration

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Install Script", func() {
	It("Should install the axon CLI binary", func() {
		By("Building the axon binary for the current platform")
		repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
		Expect(err).NotTo(HaveOccurred())

		buildCmd := exec.Command("make", "build", "WHAT=cmd/axon")
		buildCmd.Dir = repoRoot
		buildCmd.Stdout = GinkgoWriter
		buildCmd.Stderr = GinkgoWriter
		Expect(buildCmd.Run()).To(Succeed())

		builtBinary := filepath.Join(repoRoot, "bin", "axon")
		Expect(builtBinary).To(BeAnExistingFile())

		By("Starting a local HTTP server to serve the binary")
		binaryData, err := os.ReadFile(builtBinary)
		Expect(err).NotTo(HaveOccurred())

		binaryName := fmt.Sprintf("axon-%s-%s", runtime.GOOS, runtime.GOARCH)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/"+binaryName {
				w.WriteHeader(http.StatusOK)
				w.Write(binaryData)
				return
			}
			http.NotFound(w, r)
		}))
		defer server.Close()

		By("Running hack/install.sh with a temporary install directory")
		installDir := GinkgoT().TempDir()
		installScript := filepath.Join(repoRoot, "hack", "install.sh")

		cmd := exec.Command("bash", installScript)
		cmd.Env = append(os.Environ(),
			"AXON_RELEASE_URL="+server.URL,
			"INSTALL_DIR="+installDir,
		)
		cmd.Stdout = GinkgoWriter
		cmd.Stderr = GinkgoWriter
		Expect(cmd.Run()).To(Succeed())

		By("Verifying the binary was installed")
		installedBinary := filepath.Join(installDir, "axon")
		Expect(installedBinary).To(BeAnExistingFile())

		By("Verifying the installed binary is executable")
		info, err := os.Stat(installedBinary)
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm() & 0111).NotTo(BeZero())

		By("Verifying the installed binary runs")
		versionCmd := exec.Command(installedBinary, "version")
		output, err := versionCmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(output)).NotTo(BeEmpty())
	})
})
