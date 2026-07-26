package service

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/stretchr/testify/require"
)

type luminaSessionFakeRepo struct {
	AccountRepository

	mu            sync.Mutex
	account       *Account
	getByIDCalls  int
	updateCalls   int
	setErrorCalls int
	lastErrorMsg  string
}

func (r *luminaSessionFakeRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getByIDCalls++
	if r.account == nil {
		return nil, ErrAccountNotFound
	}
	account := *r.account
	account.Credentials = shallowCopyMap(r.account.Credentials)
	return &account, nil
}

func (r *luminaSessionFakeRepo) UpdateCredentialsIfUnchanged(
	_ context.Context,
	id int64,
	platform string,
	accountType string,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	credentials map[string]any,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id || r.account.Platform != platform || r.account.Type != accountType ||
		!reflect.DeepEqual(r.account.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(r.account.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.updateCalls++
	r.account.Credentials = shallowCopyMap(credentials)
	return true, nil
}

func (r *luminaSessionFakeRepo) SetAuthErrorIfCredentialsUnchanged(
	_ context.Context,
	id int64,
	platform string,
	accountType string,
	expectedCredentials map[string]any,
	expectedProxyID *int64,
	errorMsg string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id || r.account.Platform != platform || r.account.Type != accountType ||
		r.account.Status != StatusActive ||
		!reflect.DeepEqual(r.account.Credentials, expectedCredentials) ||
		!reflect.DeepEqual(r.account.ProxyID, expectedProxyID) {
		return false, nil
	}
	r.setErrorCalls++
	r.lastErrorMsg = errorMsg
	r.account.Status = StatusError
	r.account.Schedulable = false
	r.account.ErrorMessage = errorMsg
	return true, nil
}

func (r *luminaSessionFakeRepo) calls() (getByID int, update int, setError int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getByIDCalls, r.updateCalls, r.setErrorCalls
}

type luminaSessionFakeLockCache struct {
	GeminiTokenCache
	held bool
}

func (c *luminaSessionFakeLockCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return !c.held, nil
}

func (c *luminaSessionFakeLockCache) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

func newLuminaSessionTestAccount(version int64) *Account {
	return &Account{
		ID:          42,
		Platform:    PlatformLumina,
		Type:        AccountTypeCookie,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"cookie": []any{map[string]any{
				"name": "sessionid", "value": "abc", "domain": "byteplus.com", "path": "/",
			}},
			"shark_web_id":   "shark-1",
			"lumina_user_id": "u-1",
			"_token_version": version,
		},
	}
}

func authenticatedLuminaUser(context.Context, *lumina.Client) (*lumina.CurrentUser, error) {
	return &lumina.CurrentUser{UserID: "u-1", Email: "user@example.com"}, nil
}

func TestLuminaSessionProviderEnsureAuthenticatedCachesSessionValidation(t *testing.T) {
	repo := &luminaSessionFakeRepo{account: newLuminaSessionTestAccount(1)}
	provider := NewLuminaSessionProvider(repo, nil)
	validationCalls := 0
	provider.currentUser = func(ctx context.Context, client *lumina.Client) (*lumina.CurrentUser, error) {
		validationCalls++
		return authenticatedLuminaUser(ctx, client)
	}
	account := newLuminaSessionTestAccount(1)

	client, got, err := provider.EnsureAuthenticated(context.Background(), account, false)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Same(t, account, got)
	require.Equal(t, 1, validationCalls)
	require.True(t, provider.sessionStillValid(account))

	// A second gateway operation within the TTL must not re-hit the console API.
	client, got, err = provider.EnsureAuthenticated(context.Background(), account, false)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Same(t, account, got)
	require.Equal(t, 1, validationCalls, "cached validation must skip the upstream session check")

	getByIDCalls, updateCalls, _ := repo.calls()
	require.Zero(t, getByIDCalls)
	require.Zero(t, updateCalls, "unchanged cookies must not trigger a credential CAS write")
}

func TestLuminaSessionProviderSessionValidationBoundToCredentialSnapshot(t *testing.T) {
	repo := &luminaSessionFakeRepo{account: newLuminaSessionTestAccount(1)}
	provider := NewLuminaSessionProvider(repo, nil)
	validationCalls := 0
	provider.currentUser = func(ctx context.Context, client *lumina.Client) (*lumina.CurrentUser, error) {
		validationCalls++
		return authenticatedLuminaUser(ctx, client)
	}

	_, _, err := provider.EnsureAuthenticated(context.Background(), newLuminaSessionTestAccount(1), false)
	require.NoError(t, err)
	require.Equal(t, 1, validationCalls)

	// A CAS rotation bumps _token_version, so the cached validation no longer applies.
	rotated := newLuminaSessionTestAccount(2)
	_, _, err = provider.EnsureAuthenticated(context.Background(), rotated, false)
	require.NoError(t, err)
	require.Equal(t, 2, validationCalls, "credential rotation must invalidate the cached validation")
	require.True(t, provider.sessionStillValid(rotated))

	// A cookie jar held in its in-memory persisted form ([]StoredCookie) must
	// still match the validation recorded for the database-loaded form.
	reloaded := newLuminaSessionTestAccount(2)
	reloaded.Credentials["cookie"] = []lumina.StoredCookie{
		{Name: "sessionid", Value: "abc", Domain: "byteplus.com", Path: "/"},
	}
	require.True(t, provider.sessionStillValid(reloaded))

	// A manual cookie edit without a version bump must also revalidate.
	edited := newLuminaSessionTestAccount(2)
	edited.Credentials["cookie"] = []any{map[string]any{
		"name": "sessionid", "value": "replaced", "domain": "byteplus.com", "path": "/",
	}}
	require.False(t, provider.sessionStillValid(edited))
	_, _, err = provider.EnsureAuthenticated(context.Background(), edited, false)
	require.NoError(t, err)
	require.Equal(t, 3, validationCalls)

	// Expired entries never match.
	cookies, err := lumina.DecodeStoredCookies(edited.Credentials["cookie"])
	require.NoError(t, err)
	provider.validatedSessions.Store(edited.ID, luminaSessionValidation{
		cookies:      cookies,
		sharkWebID:   "shark-1",
		tokenVersion: 2,
		expiresAt:    time.Now().Add(-time.Minute),
	})
	require.False(t, provider.sessionStillValid(edited))
}

func TestLuminaSessionProviderAuthErrorInvalidatesCacheAndMarksAccount(t *testing.T) {
	account := newLuminaSessionTestAccount(1)
	delete(account.Credentials, "lumina_user_id")
	repo := &luminaSessionFakeRepo{account: account}
	provider := NewLuminaSessionProvider(repo, nil)
	authErr := &lumina.APIError{Status: http.StatusUnauthorized, Code: "Unauthorized", Message: "not logged in"}
	provider.currentUser = func(context.Context, *lumina.Client) (*lumina.CurrentUser, error) {
		return nil, authErr
	}

	_, _, err := provider.EnsureAuthenticated(context.Background(), newLuminaSessionTestAccount(1), false)
	require.Error(t, err)
	var credentialErr *lumina.AuthCredentialError
	require.ErrorAs(t, err, &credentialErr, "account without email/password must surface a credential error")

	_, _, setErrorCalls := repo.calls()
	require.Equal(t, 1, setErrorCalls)
	repo.mu.Lock()
	require.Contains(t, repo.lastErrorMsg, "MissingCredentials")
	require.Equal(t, StatusError, repo.account.Status)
	repo.mu.Unlock()
	require.False(t, provider.sessionStillValid(account), "auth failure must invalidate the cached validation")
}

func TestLuminaSessionProviderWaitForSessionRefreshAcceptsUnchangedCredentials(t *testing.T) {
	// Another instance holds the refresh lock and revalidates the stored
	// cookies without rotating them, so _token_version never changes.
	repo := &luminaSessionFakeRepo{account: newLuminaSessionTestAccount(1)}
	provider := NewLuminaSessionProvider(repo, &luminaSessionFakeLockCache{held: true})
	validationCalls := 0
	provider.currentUser = func(ctx context.Context, client *lumina.Client) (*lumina.CurrentUser, error) {
		validationCalls++
		return authenticatedLuminaUser(ctx, client)
	}

	started := time.Now()
	client, fresh, err := provider.RefreshAfterAuthFailure(context.Background(), newLuminaSessionTestAccount(1))
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, fresh)
	require.GreaterOrEqual(t, time.Since(started), luminaSessionLockWaitTimeout,
		"the waiter should poll for the full window before verifying the session itself")
	require.Equal(t, 1, validationCalls, "only the final self-verification should hit the upstream API")
	require.True(t, provider.sessionStillValid(fresh))
}

func TestLuminaSessionProviderWaitForSessionRefreshReturnsEarlyOnRotation(t *testing.T) {
	// The refresher persisted rotated credentials (version bump), so the
	// waiter must finish on the first poll instead of waiting 3s.
	repo := &luminaSessionFakeRepo{account: newLuminaSessionTestAccount(2)}
	provider := NewLuminaSessionProvider(repo, &luminaSessionFakeLockCache{held: true})
	provider.currentUser = authenticatedLuminaUser

	started := time.Now()
	client, fresh, err := provider.RefreshAfterAuthFailure(context.Background(), newLuminaSessionTestAccount(1))
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, int64(2), fresh.GetCredentialAsInt64("_token_version"))
	require.Less(t, time.Since(started), luminaSessionLockWaitTimeout)
}

// TestLuminaCredentialCookiesEqualNormalizesMonotonicTime verifies that
// luminaCredentialCookiesEqual compares two cookie snapshots correctly even
// when one side carries a monotonic clock reading (fresh from time.Now) while
// the other was reconstructed from a JSON round-trip (monotonic stripped).
// Both sides must be treated as equal when the wall-clock values match.
func TestLuminaCredentialCookiesEqualNormalizesMonotonicTime(t *testing.T) {
	// Simulate the "jar" side: a []StoredCookie freshly built by the client,
	// with an Expires carrying a monotonic clock reading.
	jarCookies := []lumina.StoredCookie{
		{
			Name:    "sessionid",
			Value:   "abc123",
			Domain:  "byteplus.com",
			Path:    "/",
			Expires: time.Now().Add(time.Hour),
		},
	}

	// Simulate the "stored" side: the same cookies serialized to the database
	// (JSON round-trip strips the monotonic component) and loaded back as the
	// raw []any form that credentials["cookie"] holds.
	raw, err := json.Marshal(jarCookies)
	require.NoError(t, err)
	var storedRaw []any
	require.NoError(t, json.Unmarshal(raw, &storedRaw))

	// The function must report equal because the wall-clock values match after
	// both sides are normalized through a JSON round-trip.
	require.True(t, luminaCredentialCookiesEqual(storedRaw, jarCookies),
		"jar cookies and stored cookies with same wall-clock Expires must be equal")

	// Sanity-check: a different value must not be equal.
	differentCookies := []lumina.StoredCookie{
		{
			Name:    "sessionid",
			Value:   "different_value",
			Domain:  "byteplus.com",
			Path:    "/",
			Expires: time.Now().Add(time.Hour),
		},
	}
	require.False(t, luminaCredentialCookiesEqual(storedRaw, differentCookies),
		"cookies with different values must not be equal")
}
