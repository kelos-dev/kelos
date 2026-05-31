package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebhookGatewayPhase represents the authentication state of a WebhookGateway.
type WebhookGatewayPhase string

const (
	// WebhookGatewayPhaseAuthenticated means inbound deliveries are HMAC-verified
	// against the gateway's secret.
	WebhookGatewayPhaseAuthenticated WebhookGatewayPhase = "Authenticated"
	// WebhookGatewayPhaseSecretMissing means a required Secret (the HMAC secret
	// or, for github, the API credentials) is not configured or not yet present.
	WebhookGatewayPhaseSecretMissing WebhookGatewayPhase = "SecretMissing"
	// WebhookGatewayPhaseUnauthenticated means inbound deliveries are accepted
	// without verification. Generic gateways are always unauthenticated in this
	// version because no per-provider signature scheme is configured.
	WebhookGatewayPhaseUnauthenticated WebhookGatewayPhase = "Unauthenticated"
)

// WebhookGatewaySpec defines the desired state of a WebhookGateway. Exactly one
// of GitHub, Linear, or Generic must be set; the field that is present selects
// the webhook source and carries its provider-specific configuration.
// +kubebuilder:validation:XValidation:rule="(has(self.github)?1:0)+(has(self.linear)?1:0)+(has(self.generic)?1:0) == 1",message="exactly one of github, linear, or generic must be set"
type WebhookGatewaySpec struct {
	// GitHub configures a gateway for GitHub webhook deliveries.
	// +optional
	GitHub *GitHubGateway `json:"github,omitempty"`

	// Linear configures a gateway for Linear webhook deliveries.
	// +optional
	Linear *LinearGateway `json:"linear,omitempty"`

	// Generic configures a gateway for arbitrary HTTP POST deliveries.
	// +optional
	Generic *GenericGateway `json:"generic,omitempty"`
}

// GitHubGateway configures a github WebhookGateway: the inbound HMAC secret and
// the outbound GitHub API backend (base URL + credentials), which together let
// one gateway server serve github.com plus multiple GitHub Enterprise instances.
type GitHubGateway struct {
	// SecretRef references a Secret holding the HMAC secret (under a
	// "webhook-secret" key) used to verify inbound deliveries.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`

	// APIBaseURL is the GitHub API base URL used for outbound API calls (pull
	// request file enrichment and status reporting, and GitHub App token
	// minting), e.g. "https://ghe.example.com/api/v3" for a GitHub Enterprise
	// instance. When empty, "https://api.github.com" is used.
	// +optional
	APIBaseURL string `json:"apiBaseURL,omitempty"`

	// CredentialsRef references a Secret holding GitHub API credentials used for
	// outbound API calls: a personal access token under the GITHUB_TOKEN key, or
	// GitHub App credentials (appID, installationID, privateKey).
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`
}

// LinearGateway configures a linear WebhookGateway.
type LinearGateway struct {
	// SecretRef references a Secret holding the HMAC secret (under a
	// "webhook-secret" key) used to verify inbound deliveries.
	// +kubebuilder:validation:Required
	SecretRef SecretReference `json:"secretRef"`
}

// GenericGateway configures a generic WebhookGateway. It has no fields yet:
// generic deliveries are accepted without signature verification, so access must
// be restricted at the network layer. A per-provider verification scheme is a
// planned follow-up and will add its configuration here.
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

// WebhookGateway is the Schema for the webhookgateways API. It is a per-channel
// authentication and routing boundary for webhook-driven TaskSpawners: it owns
// one inbound path (/webhook/<namespace>/<name>) and, for github/linear, the
// secret used to verify deliveries, then fans out to TaskSpawners in its own
// namespace that reference it via gatewayRef.
type WebhookGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebhookGatewaySpec   `json:"spec,omitempty"`
	Status WebhookGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WebhookGatewayList contains a list of WebhookGateway.
type WebhookGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebhookGateway `json:"items"`
}

// GatewayReference refers to a WebhookGateway resource by name in the same
// namespace as the referencing TaskSpawner.
type GatewayReference struct {
	// Name is the name of the WebhookGateway resource.
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&WebhookGateway{}, &WebhookGatewayList{})
}
