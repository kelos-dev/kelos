package cli

import (
	"testing"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func TestParseMCPServerFlags_HTTP(t *testing.T) {
	flags := []string{"my-api=http:https://api.example.com/mcp"}
	servers, err := parseMCPServerFlags(flags)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Name != "my-api" {
		t.Errorf("Name = %q, want %q", servers[0].Name, "my-api")
	}
	if servers[0].Transport != axonv1alpha1.MCPTransportHTTP {
		t.Errorf("Transport = %q, want %q", servers[0].Transport, axonv1alpha1.MCPTransportHTTP)
	}
	if servers[0].Target != "https://api.example.com/mcp" {
		t.Errorf("Target = %q, want %q", servers[0].Target, "https://api.example.com/mcp")
	}
}

func TestParseMCPServerFlags_SSE(t *testing.T) {
	flags := []string{"my-sse=sse:https://api.example.com/sse"}
	servers, err := parseMCPServerFlags(flags)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Transport != axonv1alpha1.MCPTransportSSE {
		t.Errorf("Transport = %q, want %q", servers[0].Transport, axonv1alpha1.MCPTransportSSE)
	}
	if servers[0].Target != "https://api.example.com/sse" {
		t.Errorf("Target = %q, want %q", servers[0].Target, "https://api.example.com/sse")
	}
}

func TestParseMCPServerFlags_Stdio(t *testing.T) {
	flags := []string{"tool=stdio:npx -y @example/server"}
	servers, err := parseMCPServerFlags(flags)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	if servers[0].Name != "tool" {
		t.Errorf("Name = %q, want %q", servers[0].Name, "tool")
	}
	if servers[0].Transport != axonv1alpha1.MCPTransportStdio {
		t.Errorf("Transport = %q, want %q", servers[0].Transport, axonv1alpha1.MCPTransportStdio)
	}
	if servers[0].Target != "npx -y @example/server" {
		t.Errorf("Target = %q, want %q", servers[0].Target, "npx -y @example/server")
	}
}

func TestParseMCPServerFlags_Multiple(t *testing.T) {
	flags := []string{
		"api-1=http:https://api1.example.com/mcp",
		"tool=stdio:my-tool",
	}
	servers, err := parseMCPServerFlags(flags)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(servers))
	}
}

func TestParseMCPServerFlags_Empty(t *testing.T) {
	servers, err := parseMCPServerFlags(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if servers != nil {
		t.Errorf("expected nil, got %v", servers)
	}
}

func TestParseMCPServerFlags_InvalidFormat_NoEquals(t *testing.T) {
	flags := []string{"invalid-format"}
	_, err := parseMCPServerFlags(flags)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestParseMCPServerFlags_InvalidFormat_NoColon(t *testing.T) {
	flags := []string{"name=invalid"}
	_, err := parseMCPServerFlags(flags)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestParseMCPServerFlags_InvalidTransport(t *testing.T) {
	flags := []string{"name=grpc:localhost:50051"}
	_, err := parseMCPServerFlags(flags)
	if err == nil {
		t.Fatal("expected error for invalid transport")
	}
}

func TestParseMCPServerFlags_EmptyTarget(t *testing.T) {
	flags := []string{"name=http:"}
	_, err := parseMCPServerFlags(flags)
	if err == nil {
		t.Fatal("expected error for empty target")
	}
}

func TestParseMCPServerFlags_EmptyName(t *testing.T) {
	flags := []string{"=http:https://example.com"}
	_, err := parseMCPServerFlags(flags)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestParseMCPServerFlags_URLWithPort(t *testing.T) {
	flags := []string{"api=http:https://api.example.com:8080/mcp"}
	servers, err := parseMCPServerFlags(flags)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if servers[0].Target != "https://api.example.com:8080/mcp" {
		t.Errorf("Target = %q, want %q", servers[0].Target, "https://api.example.com:8080/mcp")
	}
}
