package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"k8s.io/client-go/kubernetes/fake"
)

func newFakeAuthenticator(authenticated bool, allowed bool, username string, groups []string) *KubeAuthenticator {
	fakeClient := fake.NewSimpleClientset()

	fakeClient.PrependReactor("create", "tokenreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
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

	return NewKubeAuthenticatorWithClient(fakeClient)
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
