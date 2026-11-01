package conversion

import (
	"k8s.io/apimachinery/pkg/runtime"
	webhookconversion "sigs.k8s.io/controller-runtime/pkg/webhook/conversion"
)

// WebhookRegistration describes one CRD kind served by the shared conversion
// webhook.
type WebhookRegistration struct {
	Object    runtime.Object
	Converter func(*runtime.Scheme) (webhookconversion.Converter, error)
}

func WebhookRegistrations() []WebhookRegistration {
	return nil
}
