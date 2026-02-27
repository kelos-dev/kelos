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

var _ = Describe("Install Script", Ordered, func() {
	var (
		repoRoot   string
		binaryData []byte
		binaryName string
		server     *httptest.Server
	)

	BeforeAll(func() {
		var err error
		repoRoot, err = filepath.Abs(filepath.Join("..", ".."))
		Expect(err).NotTo(HaveOccurred())

		buildCmd := exec.Command("make", "build", "WHAT=cmd/kelos")
		buildCmd.Dir = repoRoot
		buildCmd.Stdout = GinkgoWriter
		buildCmd.Stderr = GinkgoWriter
		Expect(buildCmd.Run()).To(Succeed())

		builtBinary := filepath.Join(repoRoot, "bin", "kelos")
		Expect(builtBinary).To(BeAnExistingFile())

		binaryData, err = os.ReadFile(builtBinary)
		Expect(err).NotTo(HaveOccurred())

		binaryName = fmt.Sprintf("kelos-%s-%s", runtime.GOOS, runtime.GOARCH)
	})

	BeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/"+binaryName {
				w.WriteHeader(http.StatusOK)
				w.Write(binaryData)
				return
			}
			http.NotFound(w, r)
		}))
	})

	AfterEach(func() {
		server.Close()
	})

	It("Should install the kelos CLI binary", func() {
		By("Running hack/install.sh with a temporary install directory")
		installDir := GinkgoT().TempDir()
		installScript := filepath.Join(repoRoot, "hack", "install.sh")

		cmd := exec.Command("bash", installScript)
		cmd.Env = append(os.Environ(),
			"KELOS_RELEASE_URL="+server.URL,
			"INSTALL_DIR="+installDir,
		)
		cmd.Stdout = GinkgoWriter
		cmd.Stderr = GinkgoWriter
		Expect(cmd.Run()).To(Succeed())

		By("Verifying the binary was installed")
		installedBinary := filepath.Join(installDir, "kelos")
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

	It("Should create the install directory if it does not exist", func() {
		By("Using a non-existent subdirectory as install target")
		parentDir := GinkgoT().TempDir()
		installDir := filepath.Join(parentDir, "subdir", "bin")
		installScript := filepath.Join(repoRoot, "hack", "install.sh")

		cmd := exec.Command("bash", installScript)
		cmd.Env = append(os.Environ(),
			"KELOS_RELEASE_URL="+server.URL,
			"INSTALL_DIR="+installDir,
		)
		cmd.Stdout = GinkgoWriter
		cmd.Stderr = GinkgoWriter
		Expect(cmd.Run()).To(Succeed())

		By("Verifying the directory was created and binary was installed")
		installedBinary := filepath.Join(installDir, "kelos")
		Expect(installedBinary).To(BeAnExistingFile())
	})

	It("Should fail when install directory is not writable", func() {
		By("Creating a read-only directory")
		parentDir := GinkgoT().TempDir()
		installDir := filepath.Join(parentDir, "readonly")
		Expect(os.MkdirAll(installDir, 0555)).To(Succeed())
		installScript := filepath.Join(repoRoot, "hack", "install.sh")

		cmd := exec.Command("bash", installScript)
		cmd.Env = append(os.Environ(),
			"KELOS_RELEASE_URL="+server.URL,
			"INSTALL_DIR="+installDir,
		)
		output, err := cmd.CombinedOutput()
		Expect(err).To(HaveOccurred())
		Expect(string(output)).To(ContainSubstring("not writable"))
	})
})
