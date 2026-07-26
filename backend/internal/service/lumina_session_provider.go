package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
)

const (
	luminaSessionLockTTL          = 60 * time.Second
	luminaSessionLockWaitTimeout  = 3 * time.Second
	luminaSessionLockPollInterval = 50 * time.Millisecond
	luminaSessionValidationTTL    = 5 * time.Minute
)

var (
	ErrLuminaConfiguredProxyMissing = errors.New("lumina configured proxy is missing")
	ErrLuminaSessionUnavailable     = errors.New("lumina session is unavailable")
	ErrLuminaAccountIdentityChanged = errors.New("lumina account identity changed")
)

// luminaSessionValidation records that an account's stored session was
// verified against the upstream console API at a point in time. The entry is
// bound to the exact cookie jar and credential version it validated, so any
// persisted credential change (CAS rotations bump _token_version; manual
// edits replace the cookie) implicitly invalidates it on the next call.
type luminaSessionValidation struct {
	cookies      []lumina.StoredCookie
	sharkWebID   string
	tokenVersion int64
	expiresAt    time.Time
}

type LuminaSessionProvider struct {
	accountRepo AccountRepository
	lockCache   GeminiTokenCache

	localLocks        sync.Map
	validatedSessions sync.Map

	// currentUser is the upstream session check; tests may stub it to avoid
	// hitting the console API.
	currentUser func(ctx context.Context, client *lumina.Client) (*lumina.CurrentUser, error)
}

func NewLuminaSessionProvider(accountRepo AccountRepository, lockCache GeminiTokenCache) *LuminaSessionProvider {
	return &LuminaSessionProvider{accountRepo: accountRepo, lockCache: lockCache}
}

func (p *LuminaSessionProvider) ClientForAccount(account *Account) (*lumina.Client, error) {
	if account == nil || !account.IsLuminaCookie() {
		return nil, errors.New("not a lumina cookie account")
	}
	proxyURL, err := luminaAccountProxyURL(account)
	if err != nil {
		return nil, err
	}
	cookies, err := lumina.DecodeStoredCookies(account.Credentials["cookie"])
	if err != nil {
		return nil, fmt.Errorf("decode lumina cookie jar: %w", err)
	}
	return lumina.NewClient(lumina.ClientOptions{
		ProxyURL:   proxyURL,
		Cookies:    cookies,
		SharkWebID: credentialString(account.Credentials, "shark_web_id"),
	})
}

func (p *LuminaSessionProvider) EnsureAuthenticated(ctx context.Context, account *Account, allowInactive bool) (*lumina.Client, *Account, error) {
	if account == nil || !account.IsLuminaCookie() {
		return nil, nil, errors.New("not a lumina cookie account")
	}
	if !allowInactive && !account.IsActive() {
		return nil, account, ErrLuminaSessionUnavailable
	}
	client, err := p.ClientForAccount(account)
	if err != nil {
		return nil, account, err
	}
	// Skip the upstream session check while a recent validation still covers
	// this exact credential snapshot; gateway operations would otherwise
	// double every console API call.
	if p.sessionStillValid(account) {
		return client, account, nil
	}
	user, err := p.fetchCurrentUser(ctx, client)
	if err == nil && user.Authenticated() {
		if err := validateLuminaIdentity(account, user); err != nil {
			return nil, account, err
		}
		_ = p.PersistRotatedCookies(ctx, account, client)
		p.markSessionValidated(account)
		return client, account, nil
	}
	if err != nil && !lumina.IsAuthenticationError(err) {
		return nil, account, err
	}
	p.invalidateSession(account.ID)
	return p.refreshSession(ctx, account, allowInactive, false)
}

func (p *LuminaSessionProvider) RefreshAfterAuthFailure(ctx context.Context, account *Account) (*lumina.Client, *Account, error) {
	return p.refreshSession(ctx, account, false, true)
}

func (p *LuminaSessionProvider) PersistRotatedCookies(ctx context.Context, account *Account, client *lumina.Client) error {
	if p == nil || p.accountRepo == nil || account == nil || client == nil {
		return nil
	}
	credentials := shallowCopyMap(account.Credentials)
	cookies := client.StoredCookies()
	if luminaCredentialCookiesEqual(credentials["cookie"], cookies) &&
		credentialString(credentials, "shark_web_id") == client.SharkWebID() {
		return nil
	}
	credentials["cookie"] = cookies
	credentials["shark_web_id"] = client.SharkWebID()
	credentials["_token_version"] = time.Now().UnixMilli()
	if err := p.persistIfUnchanged(ctx, account, credentials); err != nil {
		return err
	}
	account.Credentials = credentials
	return nil
}

func (p *LuminaSessionProvider) refreshSession(ctx context.Context, account *Account, allowInactive bool, forcePasswordLogin bool) (*lumina.Client, *Account, error) {
	if p == nil || p.accountRepo == nil {
		return nil, account, ErrLuminaSessionUnavailable
	}
	lock := p.localLock(account.ID)
	if err := lock.Lock(ctx); err != nil {
		return nil, account, err
	}
	defer lock.Unlock()

	p.invalidateSession(account.ID)
	cacheKey := "lumina:" + strconv.FormatInt(account.ID, 10)
	if p.lockCache != nil {
		acquired, lockErr := p.lockCache.AcquireRefreshLock(ctx, cacheKey, luminaSessionLockTTL)
		if lockErr == nil && !acquired {
			return p.waitForSessionRefresh(ctx, account, allowInactive)
		}
		if lockErr == nil {
			defer p.releaseRefreshLock(cacheKey)
		}
	}

	fresh, err := p.accountRepo.GetByID(ctx, account.ID)
	if err != nil || fresh == nil || !fresh.IsLuminaCookie() {
		return nil, fresh, ErrLuminaSessionUnavailable
	}
	if !allowInactive && !fresh.IsActive() {
		return nil, fresh, ErrLuminaSessionUnavailable
	}
	client, err := p.ClientForAccount(fresh)
	if err != nil {
		return nil, fresh, err
	}
	if !forcePasswordLogin {
		user, currentErr := p.fetchCurrentUser(ctx, client)
		if currentErr == nil && user.Authenticated() {
			if err := validateLuminaIdentity(fresh, user); err != nil {
				return nil, fresh, err
			}
			_ = p.PersistRotatedCookies(ctx, fresh, client)
			p.markSessionValidated(fresh)
			return client, fresh, nil
		}
		if currentErr != nil && !lumina.IsAuthenticationError(currentErr) {
			return nil, fresh, currentErr
		}
	}

	email := credentialString(fresh.Credentials, "email")
	password := credentialSecretString(fresh.Credentials, "password")
	if email == "" || password == "" {
		authErr := &lumina.AuthCredentialError{Code: "MissingCredentials"}
		p.markAuthError(ctx, fresh, authErr)
		return nil, fresh, authErr
	}
	if _, err := client.AuthenticateWithPassword(ctx, email, password); err != nil {
		if lumina.IsInteractiveAuthError(err) || lumina.IsCredentialAuthError(err) {
			p.markAuthError(ctx, fresh, err)
		}
		return nil, fresh, err
	}
	user, err := p.fetchCurrentUser(ctx, client)
	if err != nil || !user.Authenticated() {
		return nil, fresh, ErrLuminaSessionUnavailable
	}
	if err := validateLuminaIdentity(fresh, user); err != nil {
		p.markAuthError(ctx, fresh, err)
		return nil, fresh, err
	}
	credentials := shallowCopyMap(fresh.Credentials)
	credentials["cookie"] = client.StoredCookies()
	credentials["shark_web_id"] = client.SharkWebID()
	credentials["lumina_user_id"] = user.UserID
	credentials["last_authenticated_at"] = time.Now().UTC().Format(time.RFC3339)
	credentials["_token_version"] = time.Now().UnixMilli()
	if err := p.persistIfUnchanged(ctx, fresh, credentials); err != nil {
		latest, readErr := p.accountRepo.GetByID(ctx, fresh.ID)
		if readErr != nil || latest == nil {
			return nil, fresh, err
		}
		latestClient, clientErr := p.ClientForAccount(latest)
		if clientErr != nil {
			return nil, latest, clientErr
		}
		latestUser, userErr := p.fetchCurrentUser(ctx, latestClient)
		if userErr != nil || !latestUser.Authenticated() || validateLuminaIdentity(latest, latestUser) != nil {
			return nil, latest, err
		}
		p.markSessionValidated(latest)
		return latestClient, latest, nil
	}
	fresh.Credentials = credentials
	p.markSessionValidated(fresh)
	return client, fresh, nil
}

func (p *LuminaSessionProvider) waitForSessionRefresh(ctx context.Context, account *Account, allowInactive bool) (*lumina.Client, *Account, error) {
	waitCtx, cancel := context.WithTimeout(ctx, luminaSessionLockWaitTimeout)
	defer cancel()
	initialVersion := account.GetCredentialAsInt64("_token_version")
	ticker := time.NewTicker(luminaSessionLockPollInterval)
	defer ticker.Stop()
	for {
		latest, err := p.accountRepo.GetByID(waitCtx, account.ID)
		if err == nil && latest != nil && latest.GetCredentialAsInt64("_token_version") != initialVersion {
			if client, refreshed, ok := p.validateRefreshedSession(waitCtx, latest, allowInactive); ok {
				return client, refreshed, nil
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return nil, account, ctx.Err()
			}
			// The refresher may have revalidated the stored cookies without a
			// credential rotation (no _token_version bump); verify the latest
			// snapshot once ourselves before declaring the session unavailable.
			latest, err := p.accountRepo.GetByID(ctx, account.ID)
			if err == nil {
				if client, refreshed, ok := p.validateRefreshedSession(ctx, latest, allowInactive); ok {
					return client, refreshed, nil
				}
			}
			return nil, account, ErrLuminaSessionUnavailable
		case <-ticker.C:
		}
	}
}

// validateRefreshedSession confirms a reloaded account snapshot still holds an
// authenticated session with the expected identity.
func (p *LuminaSessionProvider) validateRefreshedSession(ctx context.Context, latest *Account, allowInactive bool) (*lumina.Client, *Account, bool) {
	if latest == nil || !latest.IsLuminaCookie() || (!allowInactive && !latest.IsActive()) {
		return nil, latest, false
	}
	client, err := p.ClientForAccount(latest)
	if err != nil {
		return nil, latest, false
	}
	user, err := p.fetchCurrentUser(ctx, client)
	if err != nil || !user.Authenticated() || validateLuminaIdentity(latest, user) != nil {
		return nil, latest, false
	}
	_ = p.PersistRotatedCookies(ctx, latest, client)
	p.markSessionValidated(latest)
	return client, latest, true
}

func (p *LuminaSessionProvider) persistIfUnchanged(ctx context.Context, account *Account, credentials map[string]any) error {
	repository, ok := p.accountRepo.(CredentialCASRepository)
	if !ok {
		return errors.New("lumina credential CAS repository is not configured")
	}
	applied, err := repository.UpdateCredentialsIfUnchanged(
		ctx, account.ID, PlatformLumina, AccountTypeCookie, account.Credentials, account.ProxyID, credentials)
	if err != nil {
		return err
	}
	if !applied {
		return ErrLuminaAccountIdentityChanged
	}
	return nil
}

func (p *LuminaSessionProvider) markAuthError(ctx context.Context, account *Account, authErr error) {
	repository, ok := p.accountRepo.(CredentialCASRepository)
	if !ok || account == nil {
		return
	}
	p.invalidateSession(account.ID)
	message := "Lumina session requires manual reauthorization"
	var challenge *lumina.AuthChallengeError
	var credential *lumina.AuthCredentialError
	switch {
	case errors.As(authErr, &challenge) && challenge.Code != "":
		message += " (" + challenge.Code + ")"
	case errors.As(authErr, &credential) && credential.Code != "":
		message += " (" + credential.Code + ")"
	case errors.Is(authErr, ErrLuminaAccountIdentityChanged):
		message += " (identity_mismatch)"
	}
	_, _ = repository.SetAuthErrorIfCredentialsUnchanged(
		ctx, account.ID, PlatformLumina, AccountTypeCookie, account.Credentials, account.ProxyID, message)
}

// sessionStillValid reports whether this account's session was validated
// recently enough to skip the upstream check. Entries only match the exact
// cookie jar, shark web id and credential version they were recorded for.
func (p *LuminaSessionProvider) sessionStillValid(account *Account) bool {
	value, ok := p.validatedSessions.Load(account.ID)
	if !ok {
		return false
	}
	validation, ok := value.(luminaSessionValidation)
	if !ok {
		return false
	}
	return time.Now().Before(validation.expiresAt) &&
		validation.tokenVersion == account.GetCredentialAsInt64("_token_version") &&
		validation.sharkWebID == credentialString(account.Credentials, "shark_web_id") &&
		luminaCredentialCookiesEqual(account.Credentials["cookie"], validation.cookies)
}

func (p *LuminaSessionProvider) markSessionValidated(account *Account) {
	if account == nil {
		return
	}
	cookies, err := lumina.DecodeStoredCookies(account.Credentials["cookie"])
	if err != nil {
		return
	}
	p.validatedSessions.Store(account.ID, luminaSessionValidation{
		cookies:      cookies,
		sharkWebID:   credentialString(account.Credentials, "shark_web_id"),
		tokenVersion: account.GetCredentialAsInt64("_token_version"),
		expiresAt:    time.Now().Add(luminaSessionValidationTTL),
	})
}

func (p *LuminaSessionProvider) invalidateSession(accountID int64) {
	p.validatedSessions.Delete(accountID)
}

func (p *LuminaSessionProvider) fetchCurrentUser(ctx context.Context, client *lumina.Client) (*lumina.CurrentUser, error) {
	if p.currentUser != nil {
		return p.currentUser(ctx, client)
	}
	return client.CurrentUser(ctx)
}

func (p *LuminaSessionProvider) releaseRefreshLock(cacheKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.lockCache.ReleaseRefreshLock(ctx, cacheKey)
}

func (p *LuminaSessionProvider) localLock(accountID int64) *contextMutex {
	key := strconv.FormatInt(accountID, 10)
	value, _ := p.localLocks.LoadOrStore(key, newContextMutex())
	return value.(*contextMutex)
}

func luminaAccountProxyURL(account *Account) (string, error) {
	if account == nil {
		return "", errors.New("lumina account is nil")
	}
	if account.ProxyID != nil && account.Proxy == nil {
		return "", ErrLuminaConfiguredProxyMissing
	}
	if account.Proxy == nil {
		return "", nil
	}
	return account.Proxy.URL(), nil
}

func validateLuminaIdentity(account *Account, user *lumina.CurrentUser) error {
	if account == nil || user == nil || !user.Authenticated() {
		return ErrLuminaSessionUnavailable
	}
	expectedUserID := credentialString(account.Credentials, "lumina_user_id")
	if expectedUserID != "" && expectedUserID != user.UserID {
		return ErrLuminaAccountIdentityChanged
	}
	expectedEmail := strings.TrimSpace(credentialString(account.Credentials, "email"))
	if expectedUserID == "" && expectedEmail != "" && user.Email != "" && !strings.EqualFold(expectedEmail, strings.TrimSpace(user.Email)) {
		return ErrLuminaAccountIdentityChanged
	}
	return nil
}

func credentialString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	value, _ := credentials[key].(string)
	return strings.TrimSpace(value)
}

func credentialSecretString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	value, _ := credentials[key].(string)
	return value
}

func luminaCredentialCookiesEqual(current any, cookies []lumina.StoredCookie) bool {
	decoded, err := lumina.DecodeStoredCookies(current)
	if err != nil {
		return false
	}
	// The jar side carries time.Time values fresh from time.Now() (monotonic
	// reading, local zone) while the stored side was JSON-round-tripped;
	// reflect.DeepEqual would report identical wall-clock times as different.
	// Round-tripping both sides normalizes the representation.
	normalized, err := lumina.DecodeStoredCookies(cookies)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(decoded, normalized)
}
