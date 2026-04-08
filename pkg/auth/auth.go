package auth

import (
	"context"
	"fmt"

	log "github.com/ViaQ/logerr/v2/log/static"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KubeAuthenticator validates bearer tokens and checks authorization
// using the Kubernetes TokenReview and SubjectAccessReview APIs.
type KubeAuthenticator struct {
	clientset kubernetes.Interface
}

// NewKubeAuthenticator creates a KubeAuthenticator using in-cluster configuration.
// The in-cluster config automatically handles SA token rotation and API server CA.
func NewKubeAuthenticator() (*KubeAuthenticator, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &KubeAuthenticator{clientset: clientset}, nil
}

// NewKubeAuthenticatorWithClient creates a KubeAuthenticator with the provided clientset.
// This is primarily used for testing.
func NewKubeAuthenticatorWithClient(clientset kubernetes.Interface) *KubeAuthenticator {
	return &KubeAuthenticator{clientset: clientset}
}

// Authenticate validates a bearer token by submitting a TokenReview to the Kubernetes API.
// Returns the authenticated user info on success, or an error if the token is invalid.
func (a *KubeAuthenticator) Authenticate(ctx context.Context, token string) (*authenticationv1.TokenReviewStatus, error) {
	review, err := a.clientset.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token: token,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("token review failed: %w", err)
	}

	if !review.Status.Authenticated {
		return nil, fmt.Errorf("token is not authenticated")
	}

	log.V(3).Info("authenticated request", "user", review.Status.User.Username)
	return &review.Status, nil
}

// Authorize checks whether the given user is allowed to perform the specified action
// on a non-resource URL by submitting a SubjectAccessReview to the Kubernetes API.
func (a *KubeAuthenticator) Authorize(ctx context.Context, username string, groups []string, verb string, path string) (bool, string, error) {
	review, err := a.clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   username,
			Groups: groups,
			NonResourceAttributes: &authorizationv1.NonResourceAttributes{
				Path: path,
				Verb: verb,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return false, "", fmt.Errorf("subject access review failed: %w", err)
	}

	return review.Status.Allowed, review.Status.Reason, nil
}
