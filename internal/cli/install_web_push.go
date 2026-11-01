package cli

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type consoleWebPushInstallConfig struct {
	keyNamespace     string
	storageNamespace string
	secretName       string
}

func consoleWebPushInstallSettings(manifest []byte) (*consoleWebPushInstallConfig, error) {
	objects, err := parseManifests(manifest)
	if err != nil {
		return nil, err
	}
	for _, object := range objects {
		if object.GetKind() != "Deployment" || object.GetName() != "kelos-console-server" {
			continue
		}
		var deployment appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &deployment); err != nil {
			return nil, fmt.Errorf("reading Console Deployment %s/%s: %w", object.GetNamespace(), object.GetName(), err)
		}
		config := consoleWebPushInstallConfig{keyNamespace: deployment.Namespace}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name != "console-server" {
				continue
			}
			for _, arg := range container.Args {
				if namespace, found := strings.CutPrefix(arg, "--web-push-namespace="); found {
					config.storageNamespace = namespace
				}
			}
		}
		if config.storageNamespace == "" {
			return nil, nil
		}
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == "web-push-keys" && volume.Secret != nil {
				config.secretName = volume.Secret.SecretName
			}
		}
		if config.keyNamespace == "" || config.secretName == "" || config.storageNamespace == config.keyNamespace {
			return nil, fmt.Errorf("Console Deployment %s/%s requires a signing Secret and a separate Web Push storage namespace", deployment.Namespace, deployment.Name)
		}
		return &config, nil
	}
	return nil, nil
}

func prepareConsoleWebPush(ctx context.Context, dyn dynamic.Interface, manifest []byte) error {
	config, err := consoleWebPushInstallSettings(manifest)
	if err != nil || config == nil {
		return err
	}
	secrets := dyn.Resource(secretGVR).Namespace(config.keyNamespace)
	secret, err := secrets.Get(ctx, config.secretName, metav1.GetOptions{})
	missing := apierrors.IsNotFound(err)
	if err != nil && !missing {
		return fmt.Errorf("getting Web Push Secret %s/%s: %w", config.keyNamespace, config.secretName, err)
	}
	if !missing {
		if err := validateConsoleWebPushSecret(secret); err != nil {
			return err
		}
	}
	for _, namespace := range []string{config.keyNamespace, config.storageNamespace} {
		if err := ensureConsoleWebPushNamespace(ctx, dyn, namespace); err != nil {
			return err
		}
	}
	if !missing {
		return nil
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("generating Web Push keys for Secret %s/%s: %w", config.keyNamespace, config.secretName, err)
	}
	secret = &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
		"metadata": map[string]interface{}{"name": config.secretName, "namespace": config.keyNamespace},
		"data": map[string]interface{}{
			"public-key":  base64.StdEncoding.EncodeToString([]byte(publicKey)),
			"private-key": base64.StdEncoding.EncodeToString([]byte(privateKey)),
		},
	}}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{FieldManager: fieldManager}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Web Push Secret %s/%s: %w", config.keyNamespace, config.secretName, err)
		}
		// Concurrent installers must use the key pair that was stored first.
		secret, err = secrets.Get(ctx, config.secretName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("getting Web Push Secret %s/%s: %w", config.keyNamespace, config.secretName, err)
		}
		return validateConsoleWebPushSecret(secret)
	}
	return nil
}

func ensureConsoleWebPushNamespace(ctx context.Context, dyn dynamic.Interface, name string) error {
	namespaces := dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	if _, err := namespaces.Get(ctx, name, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting Namespace %s for Web Push: %w", name, err)
	}
	namespace := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": name},
	}}
	if _, err := namespaces.Create(ctx, namespace, metav1.CreateOptions{FieldManager: fieldManager}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Namespace %s for Web Push: %w", name, err)
	}
	return nil
}

func validateConsoleWebPushSecret(secret *unstructured.Unstructured) error {
	invalid := fmt.Errorf("Web Push Secret %s/%s must contain a matching VAPID public-key and private-key pair", secret.GetNamespace(), secret.GetName())
	privateData, privateFound := secretDataValue(secret, "private-key")
	publicData, publicFound := secretDataValue(secret, "public-key")
	if !privateFound || !publicFound {
		return invalid
	}
	privateText, err := base64.StdEncoding.DecodeString(privateData)
	if err != nil {
		return invalid
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(string(privateText), "\r\n"))
	if err != nil {
		return invalid
	}
	privateKey, err := ecdh.P256().NewPrivateKey(privateBytes)
	if err != nil {
		return invalid
	}
	publicText, err := base64.StdEncoding.DecodeString(publicData)
	if err != nil {
		return invalid
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(string(publicText), "\r\n"))
	if err != nil || !bytes.Equal(privateKey.PublicKey().Bytes(), publicBytes) {
		return invalid
	}
	return nil
}
