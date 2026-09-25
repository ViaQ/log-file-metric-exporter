package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"k8s.io/client-go/kubernetes/fake"
)

type fakeAuthSetup struct {
	authenticator *KubeAuthenticator
	client        *fake.Clientset
}

func newFakeAuth(authenticated bool, allowed bool, username string, groups []string) fakeAuthSetup {
	fakeClient := fake.NewSimpleClientset()

	fakeClient.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction := action.(k8stesting.CreateAction)
		review := createAction.GetObject().(*authenticationv1.TokenReview)

		if len(review.Spec.Audiences) > 0 && review.Spec.Audiences[0] != "log-file-metric-exporter" {
			return true, &authenticationv1.TokenReview{
				Status: authenticationv1.TokenReviewStatus{
					Authenticated: false,
				},
			}, nil
		}

		return true, &authenticationv1.TokenReview{
			Status: authenticationv1.TokenReviewStatus{
				Authenticated: authenticated,
				User: authenticationv1.UserInfo{
					Username: username,
					Groups:   groups,
				},
			},
		}, nil
	})

	fakeClient.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{
				Allowed: allowed,
				Reason:  "test",
			},
		}, nil
	})

	return fakeAuthSetup{
		authenticator: NewKubeAuthenticatorWithClient(fakeClient),
		client:        fakeClient,
	}
}

func newFakeAuthenticator(authenticated bool, allowed bool, username string, groups []string) *KubeAuthenticator {
	return newFakeAuth(authenticated, allowed, username, groups).authenticator
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("metrics"))
	})
}

func TestAuthMiddleware_Success(t *testing.T) {
	authenticator := newFakeAuthenticator(true, true, "system:serviceaccount:openshift-monitoring:prometheus-k8s", []string{"system:authenticated"})
	handler := AuthMiddleware(authenticator, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "metrics" {
		t.Errorf("expected 'metrics', got %q", w.Body.String())
	}
}

func TestAuthMiddleware_MissingAuthHeader(t *testing.T) {
	authenticator := newFakeAuthenticator(true, true, "user", nil)
	handler := AuthMiddleware(authenticator, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_InvalidAuthScheme(t *testing.T) {
	authenticator := newFakeAuthenticator(true, true, "user", nil)
	handler := AuthMiddleware(authenticator, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_EmptyBearerToken(t *testing.T) {
	authenticator := newFakeAuthenticator(true, true, "user", nil)
	handler := AuthMiddleware(authenticator, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer ")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_UnauthenticatedToken(t *testing.T) {
	authenticator := newFakeAuthenticator(false, false, "", nil)
	handler := AuthMiddleware(authenticator, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMiddleware_Unauthorized(t *testing.T) {
	authenticator := newFakeAuthenticator(true, false, "system:serviceaccount:default:test", []string{"system:authenticated"})
	handler := AuthMiddleware(authenticator, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer valid-but-unauthorized-token")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestAuthenticate_AudienceScoping(t *testing.T) {
	authenticator := newFakeAuthenticator(true, true, "test-user", []string{"test-group"})

	// Test that audience is included in TokenReview
	ctx := context.Background()
	status, err := authenticator.Authenticate(ctx, "test-token")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !status.Authenticated {
		t.Error("expected authenticated to be true")
	}
	if status.User.Username != "test-user" {
		t.Errorf("expected username 'test-user', got %q", status.User.Username)
	}
}

func countActions(client *fake.Clientset, verb, resource string) int {
	count := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == verb && a.GetResource().Resource == resource {
			count++
		}
	}
	return count
}

func TestCachedAuthenticator_TokenReviewCalledOnce(t *testing.T) {
	setup := newFakeAuth(true, true, "test-user", []string{"test-group"})
	cached := NewCachedAuthenticator(setup.authenticator, 100*time.Millisecond)

	ctx := context.Background()

	status1, err := cached.Authenticate(ctx, "test-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status1.Authenticated {
		t.Error("expected authenticated to be true")
	}

	// Second call with same token should hit the cache, not the API
	status2, err := cached.Authenticate(ctx, "test-token")
	if err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if !status2.Authenticated {
		t.Error("expected authenticated to be true on cached call")
	}

	if n := countActions(setup.client, "create", "tokenreviews"); n != 1 {
		t.Errorf("expected 1 TokenReview call, got %d", n)
	}

	// Wait for cache to expire, then call again
	time.Sleep(150 * time.Millisecond)

	_, err = cached.Authenticate(ctx, "test-token")
	if err != nil {
		t.Fatalf("unexpected error after cache expiry: %v", err)
	}

	if n := countActions(setup.client, "create", "tokenreviews"); n != 2 {
		t.Errorf("expected 2 TokenReview calls after expiry, got %d", n)
	}
}

func TestCachedAuthenticator_NegativeCaching(t *testing.T) {
	setup := newFakeAuth(false, false, "", nil)
	cached := NewCachedAuthenticator(setup.authenticator, 100*time.Millisecond)

	ctx := context.Background()

	_, err1 := cached.Authenticate(ctx, "bad-token")
	if err1 == nil {
		t.Fatal("expected error for invalid token")
	}

	// Second call should be served from cache
	_, err2 := cached.Authenticate(ctx, "bad-token")
	if err2 == nil {
		t.Fatal("expected error for cached invalid token")
	}

	if n := countActions(setup.client, "create", "tokenreviews"); n != 1 {
		t.Errorf("expected 1 TokenReview call (negative cached), got %d", n)
	}
}

func TestCachedAuthenticator_SARCalledOnce(t *testing.T) {
	setup := newFakeAuth(true, true, "test-user", []string{"test-group"})
	cached := NewCachedAuthenticator(setup.authenticator, 100*time.Millisecond)

	ctx := context.Background()

	allowed1, reason1, err := cached.Authorize(ctx, "test-user", []string{"test-group"}, "get", "/metrics")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed1 {
		t.Error("expected allowed to be true")
	}
	if reason1 != "test" {
		t.Errorf("expected reason 'test', got %q", reason1)
	}

	// Second call should be cached
	allowed2, reason2, err := cached.Authorize(ctx, "test-user", []string{"test-group"}, "get", "/metrics")
	if err != nil {
		t.Fatalf("unexpected error on cached call: %v", err)
	}
	if !allowed2 {
		t.Error("expected allowed to be true on cached call")
	}
	if reason2 != "test" {
		t.Errorf("expected reason 'test' on cached call, got %q", reason2)
	}

	if n := countActions(setup.client, "create", "subjectaccessreviews"); n != 1 {
		t.Errorf("expected 1 SAR call, got %d", n)
	}

	// Different user should miss cache
	cached.Authorize(ctx, "other-user", []string{"test-group"}, "get", "/metrics")

	if n := countActions(setup.client, "create", "subjectaccessreviews"); n != 2 {
		t.Errorf("expected 2 SAR calls with different user, got %d", n)
	}
}
