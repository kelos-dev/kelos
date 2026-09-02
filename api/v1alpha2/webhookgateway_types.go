package v1alpha2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// WebhookGatewayPhase represents the authentication state of a WebhookGateway.
type WebhookGatewayPhase string

const (
	// WebhookGatewayPhaseAuthenticated means inbound deliveries are HMAC-verified
	// against the gateway's secret.
	WebhookGatewayPhaseAuthenticated WebhookGatewayPhase = "Authenticated"
	// WebhookGatewayPhaseSecretMissing means a required Secret is not configured
	// or not yet present.
	WebhookGatewayPhaseSecretMissing WebhookGatewayPhase = "SecretMissing"
	// WebhookGatewayPhaseUnauthenticated means inbound deliveries are accepted
	// without verification. Generic gateways are always unauthenticated because
	// no per-provider signature scheme is configured.
	WebhookGatewayPhaseUnauthenticated WebhookGatewayPhase = "Unauthenticated"
)

// WebhookGatewaySpec defines the desired state of a WebhookGateway. Exactly one
// of GitHub, Linear, GitLab, or Generic must be set; the field that is present
// selects the webhook source and carries its provider-specific configuration.
// +kubebuilder:validation:XValidation:rule="(has(self.github)?1:0)+(has(self.linear)?1:0)+(has(self.gitlab)?1:0)+(has(self.generic)?1:0) == 1",message="exactly one of github, linear, gitlab, or generic must be set"
type WebhookGatewaySpec struct {
	// GitHub configures a gateway for GitHub webhook deliveries.
	// +optional
	GitHub *GitHubGateway `json:"github,omitempty"`

	// Linear configures a gateway for Linear webhook deliveries.
	// +optional
	Linear *LinearGateway `json:"linear,omitempty"`

	// GitLab configures a gateway for GitLab webhook deliveries.
	// +optional
	GitLab *GitLabGateway `json:"gitlab,omitempty"`

	// Generic configures a gateway for arbitrary HTTP POST deliveries.
	// +optional
	Generic *GenericGateway `json:"generic,omitempty"`
}

// GitHubGateway configures a GitHub WebhookGateway with inbound HMAC
// verification and optional outbound GitHub API credentials.
type GitHubGateway struct {
	// SecretRef references a Secret holding the HMAC secret under the
	// "webhook-secret" key.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// APIBaseURL is the GitHub API base URL used for outbound API calls and
	// GitHub App token minting. When empty, "https://api.github.com" is used.
	// +optional
	APIBaseURL string `json:"apiBaseURL,omitempty"`

	// CredentialsRef references a Secret holding a personal access token under
	// GITHUB_TOKEN or GitHub App credentials under appID, installationID, and
	// privateKey.
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`
}

// LinearGateway configures a Linear WebhookGateway.
type LinearGateway struct {
	// SecretRef references a Secret holding the HMAC secret under the
	// "webhook-secret" key.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`
}

// GitLabGateway configures a GitLab WebhookGateway. GitLab authenticates
// deliveries with a shared secret token sent in the X-Gitlab-Token header
// rather than an HMAC signature.
type GitLabGateway struct {
	// SecretRef references a Secret holding the webhook secret token under the
	// "webhook-secret" key.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// APIBaseURL is the GitLab instance URL used for status reporting (for
	// example "https://gitlab.example.com" or an in-cluster service URL). When
	// empty, the instance URL is taken from the originating webhook payload.
	// +kubebuilder:validation:Pattern="^https?://.+"
	// +optional
	APIBaseURL string `json:"apiBaseURL,omitempty"`

	// CredentialsRef references a Secret holding a GitLab access token under
	// the GITLAB_TOKEN key. Required for status reporting on Tasks created
	// through this gateway.
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`
}

// GenericGateway configures a generic WebhookGateway. Generic deliveries are
// accepted without signature verification, so access must be restricted at the
// network layer.
type GenericGateway struct{}

// WebhookGatewayStatus defines the observed state of a WebhookGateway.
type WebhookGatewayStatus struct {
	// Path is the inbound path this gateway listens on, derived as
	// /webhook/<namespace>/<name>. It is relative to the externally configured
	// webhook host.
	// +optional
	Path string `json:"path,omitempty"`

	// Phase summarizes the gateway's authentication state.
	// +optional
	Phase WebhookGatewayPhase `json:"phase,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Path",type=string,JSONPath=`.status.path`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WebhookGateway is a per-channel authentication and routing boundary for
// webhook-driven TaskSpawners and SessionSpawners. It owns one inbound path and
// fans out to spawners in its namespace that reference it through gatewayRef.
type WebhookGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebhookGatewaySpec   `json:"spec,omitempty"`
	Status WebhookGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WebhookGatewayList contains a list of WebhookGateway resources.
type WebhookGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebhookGateway `json:"items"`
}

// GatewayReference refers to a WebhookGateway in the same namespace as the
// referencing spawner.
type GatewayReference struct {
	// Name is the name of the WebhookGateway resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&WebhookGateway{}, &WebhookGatewayList{})
}
