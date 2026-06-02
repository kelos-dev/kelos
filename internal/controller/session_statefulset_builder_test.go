package controller

import (
	"testing"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSessionStatefulSetBuilder_Basic(t *testing.T) {
	builder := NewSessionStatefulSetBuilder()

	replicas := int32(2)
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				Replicas: &replicas,
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: AgentTypeClaudeCode,
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "my-secret"},
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "my-workspace"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
		Ref:  "main",
	}

	sts, svc, err := builder.Build(SessionStatefulSetInput{
		TaskSpawner: ts,
		Workspace:   workspace,
	})
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Check StatefulSet name.
	expectedName := "session-test-spawner"
	if sts.Name != expectedName {
		t.Errorf("StatefulSet name: expected %q, got %q", expectedName, sts.Name)
	}

	// Check replicas.
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 2 {
		t.Errorf("Replicas: expected 2, got %v", sts.Spec.Replicas)
	}

	// Check headless Service.
	if svc.Spec.ClusterIP != "None" {
		t.Errorf("Service ClusterIP: expected 'None', got %q", svc.Spec.ClusterIP)
	}
	if svc.Name != expectedName {
		t.Errorf("Service name: expected %q, got %q", expectedName, svc.Name)
	}
	// The StatefulSet must reference the headless Service for stable pod DNS.
	if sts.Spec.ServiceName != svc.Name {
		t.Errorf("StatefulSet ServiceName: expected %q, got %q", svc.Name, sts.Spec.ServiceName)
	}

	// Check labels.
	if sts.Labels["kelos.dev/taskspawner"] != "test-spawner" {
		t.Errorf("StatefulSet label kelos.dev/taskspawner: expected 'test-spawner', got %q", sts.Labels["kelos.dev/taskspawner"])
	}
	if sts.Labels["kelos.dev/component"] != SessionComponentLabel {
		t.Errorf("StatefulSet label kelos.dev/component: expected %q, got %q", SessionComponentLabel, sts.Labels["kelos.dev/component"])
	}

	// Check PVC.
	if len(sts.Spec.VolumeClaimTemplates) != 1 {
		t.Fatalf("Expected 1 VolumeClaimTemplate, got %d", len(sts.Spec.VolumeClaimTemplates))
	}
	pvc := sts.Spec.VolumeClaimTemplates[0]
	if pvc.Name != WorkspaceVolumeName {
		t.Errorf("PVC name: expected %q, got %q", WorkspaceVolumeName, pvc.Name)
	}
	expectedSize := resource.MustParse(DefaultSessionStorageSize)
	if !pvc.Spec.Resources.Requests[corev1.ResourceStorage].Equal(expectedSize) {
		t.Errorf("PVC storage: expected %v, got %v", expectedSize, pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	}

	// Check main container uses session runner.
	mainContainer := sts.Spec.Template.Spec.Containers[0]
	if mainContainer.Name != AgentTypeClaudeCode {
		t.Errorf("Main container name: expected %q, got %q", AgentTypeClaudeCode, mainContainer.Name)
	}
	if mainContainer.Image != ClaudeCodeImage {
		t.Errorf("Main container image: expected %q, got %q", ClaudeCodeImage, mainContainer.Image)
	}
	expectedCmd := SessionRunnerMountPath + "/kelos-session-runner"
	if len(mainContainer.Command) != 1 || mainContainer.Command[0] != expectedCmd {
		t.Errorf("Main container command: expected [%s], got %v", expectedCmd, mainContainer.Command)
	}

	// Check session runner injection init container.
	if len(sts.Spec.Template.Spec.InitContainers) < 1 {
		t.Fatal("Expected at least 1 init container")
	}
	injector := sts.Spec.Template.Spec.InitContainers[0]
	if injector.Name != "inject-session-runner" {
		t.Errorf("First init container name: expected 'inject-session-runner', got %q", injector.Name)
	}

	// Check git-clone init container.
	if len(sts.Spec.Template.Spec.InitContainers) < 2 {
		t.Fatal("Expected at least 2 init containers (injector + git-clone)")
	}
	gitClone := sts.Spec.Template.Spec.InitContainers[1]
	if gitClone.Name != "git-clone" {
		t.Errorf("Second init container name: expected 'git-clone', got %q", gitClone.Name)
	}

	// Check env vars include session-specific vars.
	envMap := make(map[string]string)
	for _, env := range mainContainer.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}
	if envMap["KELOS_EXECUTION_MODE"] != "persistent" {
		t.Errorf("KELOS_EXECUTION_MODE: expected 'persistent', got %q", envMap["KELOS_EXECUTION_MODE"])
	}
	if envMap["KELOS_AGENT_TYPE"] != AgentTypeClaudeCode {
		t.Errorf("KELOS_AGENT_TYPE: expected %q, got %q", AgentTypeClaudeCode, envMap["KELOS_AGENT_TYPE"])
	}
	if envMap["KELOS_TASKSPAWNER"] != "test-spawner" {
		t.Errorf("KELOS_TASKSPAWNER: expected 'test-spawner', got %q", envMap["KELOS_TASKSPAWNER"])
	}
	if envMap["KELOS_BASE_BRANCH"] != "main" {
		t.Errorf("KELOS_BASE_BRANCH: expected 'main', got %q", envMap["KELOS_BASE_BRANCH"])
	}

	// Check pod security context.
	psc := sts.Spec.Template.Spec.SecurityContext
	if psc == nil || psc.FSGroup == nil || *psc.FSGroup != AgentUID {
		t.Error("Expected pod security context with FSGroup = AgentUID")
	}
}

func TestSessionStatefulSetBuilder_CustomStorage(t *testing.T) {
	builder := NewSessionStatefulSetBuilder()

	storageSize := resource.MustParse("50Gi")
	storageClass := "fast-ssd"
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "big-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				StorageSize:      &storageSize,
				StorageClassName: &storageClass,
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: AgentTypeClaudeCode,
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "secret"},
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
	}

	sts, _, err := builder.Build(SessionStatefulSetInput{
		TaskSpawner: ts,
		Workspace:   workspace,
	})
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	pvc := sts.Spec.VolumeClaimTemplates[0]
	if !pvc.Spec.Resources.Requests[corev1.ResourceStorage].Equal(storageSize) {
		t.Errorf("PVC storage: expected %v, got %v", storageSize, pvc.Spec.Resources.Requests[corev1.ResourceStorage])
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != storageClass {
		t.Errorf("PVC storage class: expected %q, got %v", storageClass, pvc.Spec.StorageClassName)
	}
}

func TestSessionStatefulSetBuilder_SessionEnvVars(t *testing.T) {
	builder := NewSessionStatefulSetBuilder()

	idleTimeout := metav1.Duration{Duration: 900000000000} // 15m
	maxTasks := int32(10)
	maxDuration := metav1.Duration{Duration: 14400000000000} // 4h
	gitReset := false
	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			SessionConfig: &kelosv1alpha1.SessionConfig{
				IdleTimeout:        &idleTimeout,
				MaxTasksPerSession: &maxTasks,
				MaxSessionDuration: &maxDuration,
				WorkspaceReset: &kelosv1alpha1.WorkspaceResetConfig{
					Git:                 &gitReset,
					PreserveDirectories: []string{"node_modules", ".venv"},
				},
			},
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type:  AgentTypeClaudeCode,
				Model: "claude-sonnet-4-20250514",
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "secret"},
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
	}

	sts, _, err := builder.Build(SessionStatefulSetInput{
		TaskSpawner: ts,
		Workspace:   workspace,
	})
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	mainContainer := sts.Spec.Template.Spec.Containers[0]
	envMap := make(map[string]string)
	for _, env := range mainContainer.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}

	if envMap["KELOS_IDLE_TIMEOUT"] != "15m0s" {
		t.Errorf("KELOS_IDLE_TIMEOUT: expected '15m0s', got %q", envMap["KELOS_IDLE_TIMEOUT"])
	}
	if envMap["KELOS_MAX_TASKS_PER_SESSION"] != "10" {
		t.Errorf("KELOS_MAX_TASKS_PER_SESSION: expected '10', got %q", envMap["KELOS_MAX_TASKS_PER_SESSION"])
	}
	if envMap["KELOS_MAX_SESSION_DURATION"] != "4h0m0s" {
		t.Errorf("KELOS_MAX_SESSION_DURATION: expected '4h0m0s', got %q", envMap["KELOS_MAX_SESSION_DURATION"])
	}
	if envMap["KELOS_WORKSPACE_RESET_GIT"] != "false" {
		t.Errorf("KELOS_WORKSPACE_RESET_GIT: expected 'false', got %q", envMap["KELOS_WORKSPACE_RESET_GIT"])
	}
	if envMap["KELOS_WORKSPACE_RESET_PRESERVE_DIRS"] != `["node_modules",".venv"]` {
		t.Errorf("KELOS_WORKSPACE_RESET_PRESERVE_DIRS: expected '[\"node_modules\",\".venv\"]', got %q", envMap["KELOS_WORKSPACE_RESET_PRESERVE_DIRS"])
	}
	if envMap["KELOS_MODEL"] != "claude-sonnet-4-20250514" {
		t.Errorf("KELOS_MODEL: expected 'claude-sonnet-4-20250514', got %q", envMap["KELOS_MODEL"])
	}
}

func TestSessionStatefulSetBuilder_WithPlugins(t *testing.T) {
	builder := NewSessionStatefulSetBuilder()

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "plugin-spawner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: AgentTypeClaudeCode,
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "secret"},
				},
				WorkspaceRef:   &kelosv1alpha1.WorkspaceReference{Name: "ws"},
				AgentConfigRef: &kelosv1alpha1.AgentConfigReference{Name: "config"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
	}

	agentConfig := &kelosv1alpha1.AgentConfigSpec{
		AgentsMD: "# Custom instructions",
		Plugins: []kelosv1alpha1.PluginSpec{
			{
				Name: "my-plugin",
				Skills: []kelosv1alpha1.SkillDefinition{
					{Name: "review", Content: "Review skill"},
				},
			},
		},
	}

	sts, _, err := builder.Build(SessionStatefulSetInput{
		TaskSpawner: ts,
		Workspace:   workspace,
		AgentConfig: agentConfig,
	})
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	// Should have plugin volume.
	hasPluginVolume := false
	for _, vol := range sts.Spec.Template.Spec.Volumes {
		if vol.Name == PluginVolumeName {
			hasPluginVolume = true
			break
		}
	}
	if !hasPluginVolume {
		t.Error("Expected plugin volume")
	}

	// Should have plugin-setup init container.
	hasPluginSetup := false
	for _, ic := range sts.Spec.Template.Spec.InitContainers {
		if ic.Name == "plugin-setup" {
			hasPluginSetup = true
			break
		}
	}
	if !hasPluginSetup {
		t.Error("Expected plugin-setup init container")
	}

	// Main container should have KELOS_AGENTS_MD and KELOS_PLUGIN_DIR.
	mainContainer := sts.Spec.Template.Spec.Containers[0]
	envMap := make(map[string]string)
	for _, env := range mainContainer.Env {
		if env.Value != "" {
			envMap[env.Name] = env.Value
		}
	}
	if envMap["KELOS_AGENTS_MD"] != "# Custom instructions" {
		t.Errorf("KELOS_AGENTS_MD: expected '# Custom instructions', got %q", envMap["KELOS_AGENTS_MD"])
	}
	if envMap["KELOS_PLUGIN_DIR"] != PluginMountPath {
		t.Errorf("KELOS_PLUGIN_DIR: expected %q, got %q", PluginMountPath, envMap["KELOS_PLUGIN_DIR"])
	}
}

func TestSessionStatefulSetBuilder_CustomSessionRunnerImage(t *testing.T) {
	builder := &SessionStatefulSetBuilder{
		SessionRunnerImage:           "my-registry.example.com/kelos-session-runner:v1.2.3",
		SessionRunnerImagePullPolicy: corev1.PullAlways,
	}

	ts := &kelosv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-runner",
			Namespace: "default",
		},
		Spec: kelosv1alpha1.TaskSpawnerSpec{
			ExecutionMode: kelosv1alpha1.ExecutionModePersistent,
			TaskTemplate: kelosv1alpha1.TaskTemplate{
				Type: AgentTypeClaudeCode,
				Credentials: kelosv1alpha1.Credentials{
					Type:      kelosv1alpha1.CredentialTypeAPIKey,
					SecretRef: &kelosv1alpha1.SecretReference{Name: "secret"},
				},
				WorkspaceRef: &kelosv1alpha1.WorkspaceReference{Name: "ws"},
			},
		},
	}

	workspace := &kelosv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/test/repo.git",
	}

	sts, _, err := builder.Build(SessionStatefulSetInput{
		TaskSpawner: ts,
		Workspace:   workspace,
	})
	if err != nil {
		t.Fatalf("Build() returned error: %v", err)
	}

	injector := sts.Spec.Template.Spec.InitContainers[0]
	if injector.Image != "my-registry.example.com/kelos-session-runner:v1.2.3" {
		t.Errorf("Injector image: expected custom image, got %q", injector.Image)
	}
	if injector.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("Injector pull policy: expected Always, got %q", injector.ImagePullPolicy)
	}
}

func TestSessionStatefulSetName(t *testing.T) {
	if got := sessionStatefulSetName("my-spawner"); got != "session-my-spawner" {
		t.Errorf("sessionStatefulSetName: expected 'session-my-spawner', got %q", got)
	}
}
