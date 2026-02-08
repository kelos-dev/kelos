package controller

import (
	"encoding/json"
	"testing"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildMCPConfig_HTTPServer(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "my-api",
			Transport: axonv1alpha1.MCPTransportHTTP,
			Target:    "https://api.example.com/mcp",
		},
	}

	config, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]map[string]mcpConfigEntry
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	entry, ok := parsed["mcpServers"]["my-api"]
	if !ok {
		t.Fatal("expected 'my-api' entry in mcpServers")
	}
	if entry.Type != "http" {
		t.Errorf("Type = %q, want %q", entry.Type, "http")
	}
	if entry.URL != "https://api.example.com/mcp" {
		t.Errorf("URL = %q, want %q", entry.URL, "https://api.example.com/mcp")
	}
	if entry.Command != "" {
		t.Errorf("Command = %q, want empty", entry.Command)
	}
}

func TestBuildMCPConfig_SSEServer(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "sse-api",
			Transport: axonv1alpha1.MCPTransportSSE,
			Target:    "https://api.example.com/sse",
		},
	}

	config, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]map[string]mcpConfigEntry
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	entry, ok := parsed["mcpServers"]["sse-api"]
	if !ok {
		t.Fatal("expected 'sse-api' entry in mcpServers")
	}
	if entry.Type != "sse" {
		t.Errorf("Type = %q, want %q", entry.Type, "sse")
	}
	if entry.URL != "https://api.example.com/sse" {
		t.Errorf("URL = %q, want %q", entry.URL, "https://api.example.com/sse")
	}
}

func TestBuildMCPConfig_StdioServer(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "local-tool",
			Transport: axonv1alpha1.MCPTransportStdio,
			Target:    "npx",
			Args:      []string{"-y", "@example/server"},
		},
	}

	config, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]map[string]mcpConfigEntry
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	entry, ok := parsed["mcpServers"]["local-tool"]
	if !ok {
		t.Fatal("expected 'local-tool' entry in mcpServers")
	}
	if entry.Command != "npx" {
		t.Errorf("Command = %q, want %q", entry.Command, "npx")
	}
	if len(entry.Args) != 2 || entry.Args[0] != "-y" || entry.Args[1] != "@example/server" {
		t.Errorf("Args = %v, want [-y @example/server]", entry.Args)
	}
	if entry.URL != "" {
		t.Errorf("URL = %q, want empty", entry.URL)
	}
}

func TestBuildMCPConfig_WithEnvAndHeaders(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "auth-api",
			Transport: axonv1alpha1.MCPTransportHTTP,
			Target:    "https://api.example.com/mcp",
			Env: map[string]string{
				"API_KEY": "secret-key",
			},
			Headers: map[string]string{
				"Authorization": "Bearer token123",
			},
		},
	}

	config, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]map[string]mcpConfigEntry
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	entry := parsed["mcpServers"]["auth-api"]
	if entry.Env["API_KEY"] != "secret-key" {
		t.Errorf("Env[API_KEY] = %q, want %q", entry.Env["API_KEY"], "secret-key")
	}
	if entry.Headers["Authorization"] != "Bearer token123" {
		t.Errorf("Headers[Authorization] = %q, want %q", entry.Headers["Authorization"], "Bearer token123")
	}
}

func TestBuildMCPConfig_MultipleServers(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "api-1",
			Transport: axonv1alpha1.MCPTransportHTTP,
			Target:    "https://api1.example.com/mcp",
		},
		{
			Name:      "api-2",
			Transport: axonv1alpha1.MCPTransportStdio,
			Target:    "my-tool",
		},
	}

	config, err := buildMCPConfig(servers)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed map[string]map[string]mcpConfigEntry
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(parsed["mcpServers"]) != 2 {
		t.Errorf("expected 2 MCP servers, got %d", len(parsed["mcpServers"]))
	}
	if _, ok := parsed["mcpServers"]["api-1"]; !ok {
		t.Error("expected 'api-1' entry")
	}
	if _, ok := parsed["mcpServers"]["api-2"]; !ok {
		t.Error("expected 'api-2' entry")
	}
}

func TestBuildMCPConfig_DuplicateNames(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "my-api",
			Transport: axonv1alpha1.MCPTransportHTTP,
			Target:    "https://api1.example.com/mcp",
		},
		{
			Name:      "my-api",
			Transport: axonv1alpha1.MCPTransportHTTP,
			Target:    "https://api2.example.com/mcp",
		},
	}

	_, err := buildMCPConfig(servers)
	if err == nil {
		t.Fatal("expected error for duplicate MCP server names")
	}
}

func TestBuildMCPConfig_UnsupportedTransport(t *testing.T) {
	servers := []axonv1alpha1.MCPServer{
		{
			Name:      "bad",
			Transport: "grpc",
			Target:    "localhost:50051",
		},
	}

	_, err := buildMCPConfig(servers)
	if err == nil {
		t.Fatal("expected error for unsupported transport")
	}
}

func TestBuildClaudeCodeJob_WithMCPServers(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do something",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			MCPServers: []axonv1alpha1.MCPServer{
				{
					Name:      "my-api",
					Transport: axonv1alpha1.MCPTransportHTTP,
					Target:    "https://api.example.com/mcp",
				},
			},
		},
	}

	job, err := builder.Build(task, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify MCP config init container
	if len(job.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1 init container, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	initContainer := job.Spec.Template.Spec.InitContainers[0]
	if initContainer.Name != "mcp-config" {
		t.Errorf("init container name = %q, want %q", initContainer.Name, "mcp-config")
	}

	// Verify MCP config volume
	foundMCPVolume := false
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == MCPConfigVolumeName {
			foundMCPVolume = true
			if v.EmptyDir == nil {
				t.Error("expected EmptyDir volume source for MCP config")
			}
		}
	}
	if !foundMCPVolume {
		t.Error("expected MCP config volume")
	}

	// Verify main container has MCP volume mount
	mainContainer := job.Spec.Template.Spec.Containers[0]
	foundMCPMount := false
	for _, vm := range mainContainer.VolumeMounts {
		if vm.Name == MCPConfigVolumeName {
			foundMCPMount = true
			if vm.MountPath != MCPConfigMountPath {
				t.Errorf("MCP mount path = %q, want %q", vm.MountPath, MCPConfigMountPath)
			}
		}
	}
	if !foundMCPMount {
		t.Error("expected MCP config volume mount on main container")
	}

	// Verify --mcp-config flag in args
	foundMCPFlag := false
	for i, arg := range mainContainer.Args {
		if arg == "--mcp-config" {
			foundMCPFlag = true
			if i+1 >= len(mainContainer.Args) || mainContainer.Args[i+1] != MCPConfigFilePath {
				t.Errorf("expected --mcp-config %s", MCPConfigFilePath)
			}
		}
	}
	if !foundMCPFlag {
		t.Error("expected --mcp-config flag in container args")
	}

	// Verify init container runs as claude user
	if initContainer.SecurityContext == nil || initContainer.SecurityContext.RunAsUser == nil {
		t.Fatal("expected security context with RunAsUser on init container")
	}
	if *initContainer.SecurityContext.RunAsUser != ClaudeCodeUID {
		t.Errorf("RunAsUser = %d, want %d", *initContainer.SecurityContext.RunAsUser, ClaudeCodeUID)
	}
}

func TestBuildClaudeCodeJob_WithMCPServersAndWorkspace(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-mcp-ws-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do something",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
			MCPServers: []axonv1alpha1.MCPServer{
				{
					Name:      "my-api",
					Transport: axonv1alpha1.MCPTransportHTTP,
					Target:    "https://api.example.com/mcp",
				},
			},
		},
	}

	workspace := &axonv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	}

	job, err := builder.Build(task, workspace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 2 init containers: mcp-config and git-clone
	if len(job.Spec.Template.Spec.InitContainers) != 2 {
		t.Fatalf("expected 2 init containers, got %d", len(job.Spec.Template.Spec.InitContainers))
	}
	if job.Spec.Template.Spec.InitContainers[0].Name != "mcp-config" {
		t.Errorf("first init container name = %q, want %q", job.Spec.Template.Spec.InitContainers[0].Name, "mcp-config")
	}
	if job.Spec.Template.Spec.InitContainers[1].Name != "git-clone" {
		t.Errorf("second init container name = %q, want %q", job.Spec.Template.Spec.InitContainers[1].Name, "git-clone")
	}

	// Should have 2 volumes: mcp-config and workspace
	if len(job.Spec.Template.Spec.Volumes) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(job.Spec.Template.Spec.Volumes))
	}

	// Main container should have 2 volume mounts
	mainContainer := job.Spec.Template.Spec.Containers[0]
	if len(mainContainer.VolumeMounts) != 2 {
		t.Fatalf("expected 2 volume mounts, got %d", len(mainContainer.VolumeMounts))
	}
}

func TestBuildClaudeCodeJob_WithoutMCPServers(t *testing.T) {
	builder := NewJobBuilder()
	task := &axonv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-no-mcp-task",
			Namespace: "default",
		},
		Spec: axonv1alpha1.TaskSpec{
			Type:   AgentTypeClaudeCode,
			Prompt: "Do something",
			Credentials: axonv1alpha1.Credentials{
				Type:      axonv1alpha1.CredentialTypeAPIKey,
				SecretRef: axonv1alpha1.SecretReference{Name: "my-secret"},
			},
		},
	}

	job, err := builder.Build(task, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No init containers
	if len(job.Spec.Template.Spec.InitContainers) != 0 {
		t.Errorf("expected 0 init containers, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// No volumes
	if len(job.Spec.Template.Spec.Volumes) != 0 {
		t.Errorf("expected 0 volumes, got %d", len(job.Spec.Template.Spec.Volumes))
	}

	// No --mcp-config flag
	mainContainer := job.Spec.Template.Spec.Containers[0]
	for _, arg := range mainContainer.Args {
		if arg == "--mcp-config" {
			t.Error("unexpected --mcp-config flag without MCP servers")
		}
	}
}
