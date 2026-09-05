package webhook

import (
	"testing"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestProviderForCoversEverySource(t *testing.T) {
	for _, source := range []WebhookSource{GitHubSource, LinearSource, GitLabSource, GenericSource} {
		if _, err := providerFor(source); err != nil {
			t.Errorf("providerFor(%s) error = %v", source, err)
		}
	}
	if _, err := providerFor("bitbucket"); err == nil {
		t.Error("providerFor(unknown) must fail so an unregistered source cannot be served")
	}
}

func TestProviderGatewayRefSelectsOwnSpec(t *testing.T) {
	ref := &kelos.GatewayReference{Name: "gw"}
	spawners := map[WebhookSource]*kelos.TaskSpawner{
		GitHubSource:  {Spec: kelos.TaskSpawnerSpec{When: kelos.When{GitHubWebhook: &kelos.GitHubWebhook{GatewayRef: ref}}}},
		LinearSource:  {Spec: kelos.TaskSpawnerSpec{When: kelos.When{LinearWebhook: &kelos.LinearWebhook{GatewayRef: ref}}}},
		GitLabSource:  {Spec: kelos.TaskSpawnerSpec{When: kelos.When{GitLabWebhook: &kelos.GitLabWebhook{GatewayRef: ref}}}},
		GenericSource: {Spec: kelos.TaskSpawnerSpec{When: kelos.When{GenericWebhook: &kelos.GenericWebhook{GatewayRef: ref}}}},
	}
	for source, provider := range webhookProviders {
		for spawnerSource, spawner := range spawners {
			got, ok := provider.gatewayRef(spawner)
			if spawnerSource == source {
				if !ok || got != ref {
					t.Errorf("%s provider must claim its own spawner with its gatewayRef, got (%v, %v)", source, got, ok)
				}
			} else if ok {
				t.Errorf("%s provider must not claim a %s spawner", source, spawnerSource)
			}
		}
	}
}
