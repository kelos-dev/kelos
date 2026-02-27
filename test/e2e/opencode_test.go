package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

// openCodeTestModel uses a free OpenCode model so e2e tests require no authentication.
const openCodeTestModel = "opencode/big-pickle"

var _ = Describe("OpenCode Task", func() {
	f := framework.NewFramework("opencode")

	It("should run an OpenCode Task to completion", func() {
		By("creating credentials secret (empty key for free OpenCode model)")
		f.CreateSecret("opencode-credentials",
			"OPENCODE_API_KEY=")

		By("creating an OpenCode Task")
		f.CreateTask(&kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name: "opencode-task",
			},
			Spec: kelosv1alpha1.TaskSpec{
				Type:   "opencode",
				Model:  openCodeTestModel,
				Prompt: "Print 'Hello from OpenCode e2e test' to stdout",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: kelosv1alpha1.SecretReference{Name: "opencode-credentials"},
				},
			},
		})

		By("waiting for Job to be created")
		f.WaitForJobCreation("opencode-task")

		By("waiting for Job to complete")
		f.WaitForJobCompletion("opencode-task")

		By("verifying Task status is Succeeded")
		Expect(f.GetTaskPhase("opencode-task")).To(Equal("Succeeded"))

		By("getting Job logs")
		logs := f.GetJobLogs("opencode-task")
		GinkgoWriter.Printf("Job logs:\n%s\n", logs)
	})
})
