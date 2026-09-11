# Single-Branch Shallow Workspace Clones Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prevent Kelos workspace initialization from fetching unrelated branch tips while preserving configured refs and targeted task branches.

**Architecture:** Change the clone argument construction shared by Job-backed Tasks and persistent WorkerPools/Sessions from all-branch shallow clones to explicit single-branch shallow clones. Keep full-SHA checkout and the existing targeted `branch-setup` fetch unchanged.

**Tech Stack:** Go, Kubernetes controller builders, standard `testing`, Testify, Make.

---

### Task 1: Lock down Job clone arguments

**Files:**
- Modify: `internal/controller/job_builder_test.go:193`
- Modify: `internal/controller/job_builder.go:540`

- [ ] **Step 1: Change the Job regression expectation first**

In `TestBuildClaudeCodeJob_WorkspaceWithRef`, replace the expected clone arguments with:

```go
expectedArgs := []string{
	"clone",
	"--branch", "main", "--single-branch", "--depth", "1",
	"--", "https://github.com/example/repo.git", WorkspaceMountPath + "/repo",
}
```

- [ ] **Step 2: Run the Job regression test and verify RED**

Run:

```bash
make test TEST_FLAGS='-run TestBuildClaudeCodeJob_WorkspaceWithRef -count=1'
```

Expected: FAIL because the builder still emits `--no-single-branch`.

- [ ] **Step 3: Make the minimal Job builder change**

In `buildAgentJob`, replace the clone argument append with:

```go
cloneArgs = append(cloneArgs, "--single-branch", "--depth", "1", "--", workspace.Repo, targetPath)
```

- [ ] **Step 4: Run the Job regression test and verify GREEN**

Run:

```bash
make test TEST_FLAGS='-run TestBuildClaudeCodeJob_WorkspaceWithRef -count=1'
```

Expected: PASS.

### Task 2: Lock down WorkerPool and Session clone arguments

**Files:**
- Modify: `internal/controller/workerpool_controller_test.go:209`
- Modify: `internal/controller/workerpool_controller.go:735`

- [ ] **Step 1: Add the WorkerPool regression test first**

Add this test before the existing commit-ref test:

```go
func TestWorkerPoolReconciler_BranchWorkspaceUsesSingleBranchClone(t *testing.T) {
	scheme := newWorkerPoolTestScheme()
	pool := newTestWorkerPool("my-pool", "default", 1)
	ws := newTestWorkspace("default")

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.WorkerPool{}).
		WithObjects(pool, ws).
		Build()

	r := newWorkerPoolReconciler(cl, scheme)
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "my-pool", Namespace: "default"},
	})
	require.NoError(t, err)

	var sts appsv1.StatefulSet
	err = cl.Get(context.Background(), types.NamespacedName{Name: "wp-my-pool", Namespace: "default"}, &sts)
	require.NoError(t, err)

	var gitClone *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "git-clone" {
			gitClone = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	require.NotNil(t, gitClone)
	assert.Equal(t, []string{
		"--", "clone", "--branch", "main", "--single-branch", "--depth", "1",
		"--", ws.Spec.Repo, WorkspaceMountPath + "/repo",
	}, gitClone.Args)
}
```

- [ ] **Step 2: Run the WorkerPool regression test and verify RED**

Run:

```bash
make test TEST_FLAGS='-run TestWorkerPoolReconciler_BranchWorkspaceUsesSingleBranchClone -count=1'
```

Expected: FAIL because the controller still emits `--no-single-branch`.

- [ ] **Step 3: Make the minimal WorkerPool builder change**

Replace the clone argument append with:

```go
cloneArgs = append(cloneArgs, "--single-branch", "--depth", "1", "--", workspace.Repo, targetPath)
```

- [ ] **Step 4: Run the WorkerPool regression test and verify GREEN**

Run:

```bash
make test TEST_FLAGS='-run TestWorkerPoolReconciler_BranchWorkspaceUsesSingleBranchClone -count=1'
```

Expected: PASS.

### Task 3: Verify compatibility and repository health

**Files:**
- Verify: `internal/controller/job_builder_test.go`
- Verify: `internal/controller/workerpool_controller_test.go`
- Modify: `test/integration/task_test.go:1285,1485,1563`

- [ ] **Step 1: Update integration expectations for ref, authenticated, and default-branch clones**

Replace the three remaining `--no-single-branch` expected arguments in `test/integration/task_test.go` with `--single-branch`.

- [ ] **Step 2: Verify configured refs, targeted branches, and commit-SHA paths together**

Run:

```bash
make test TEST_FLAGS='-run "TestBuildClaudeCodeJob_WorkspaceWithRef|TestBuildClaudeCodeJob_WorkspaceWithCommitRefFetchesDetached|TestBuildJob_BranchSetupInitContainer|TestWorkerPoolReconciler_BranchWorkspaceUsesSingleBranchClone|TestWorkerPoolReconciler_CommitRefWorkspaceUsesCheckoutScript" -count=1'
```

Expected: PASS for all selected tests.

- [ ] **Step 3: Run repository verification**

Run:

```bash
make verify
```

Expected: PASS with no generated, formatting, module, or vet differences.

- [ ] **Step 4: Run all unit tests**

Run:

```bash
make test
```

Expected: all tests related to this change pass. If `TestClaudeEntrypointUsesPersistentSessionConfig` remains the sole failure, record it as the verified pre-existing baseline fixture failure.

- [ ] **Step 5: Run integration tests and build**

Run:

```bash
make test-integration
make build
```

Expected: both commands PASS.

- [ ] **Step 6: Inspect and commit the implementation**

Run:

```bash
git diff --check
git diff -- internal/controller/job_builder.go internal/controller/job_builder_test.go internal/controller/workerpool_controller.go internal/controller/workerpool_controller_test.go
git add docs/superpowers/plans/2026-08-31-single-branch-shallow-clones.md internal/controller/job_builder.go internal/controller/job_builder_test.go internal/controller/workerpool_controller.go internal/controller/workerpool_controller_test.go test/integration/task_test.go
git commit -m "fix(controller): shallow clone one workspace branch"
```

Expected: one focused implementation commit following the design commit.
