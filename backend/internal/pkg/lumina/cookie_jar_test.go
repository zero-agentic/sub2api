package lumina

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCookieJarExcludesExpiredCookiesAndKeepsSessionCookies(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	jar := NewCookieJar([]StoredCookie{
		{Name: "expired", Value: "old", Domain: "byteplus.com", Path: "/", Expires: now.Add(-time.Second)},
		{Name: "persistent", Value: "valid", Domain: "byteplus.com", Path: "/", Expires: now.Add(time.Hour)},
		{Name: "session", Value: "server-controlled", Domain: "byteplus.com", Path: "/"},
	})
	jar.now = func() time.Time { return now }
	target, err := url.Parse("https://ai.byteplus.com/lumina/zh/model/image")
	require.NoError(t, err)

	cookies := jar.Cookies(target)
	names := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		names = append(names, cookie.Name)
	}
	require.ElementsMatch(t, []string{"persistent", "session"}, names)
	require.Len(t, jar.Export(), 2)
}

func TestCookieJarPersistsServerRotation(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	jar := NewCookieJar([]StoredCookie{{Name: "sessionid", Value: "old", Domain: "console.byteplus.com", Path: "/"}})
	jar.now = func() time.Time { return now }
	target, err := url.Parse("https://console.byteplus.com/auth/login/")
	require.NoError(t, err)

	jar.SetCookies(target, []*http.Cookie{{
		Name:     "sessionid",
		Value:    "rotated",
		Path:     "/",
		Expires:  now.Add(24 * time.Hour),
		Secure:   true,
		HttpOnly: true,
	}})

	exported := jar.Export()
	require.Len(t, exported, 1)
	require.Equal(t, "rotated", exported[0].Value)
	require.Equal(t, now.Add(24*time.Hour), exported[0].Expires)
	require.True(t, exported[0].HTTPOnly)
}

func TestCookieJarConvertsMaxAgeToPersistentAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	current := now
	jar := NewCookieJar(nil)
	jar.now = func() time.Time { return current }
	target, err := url.Parse("https://console.byteplus.com/auth/login/")
	require.NoError(t, err)

	jar.SetCookies(target, []*http.Cookie{{
		Name:    "sessionid",
		Value:   "short-lived",
		Path:    "/",
		Expires: now.Add(-time.Hour),
		MaxAge:  60,
	}})

	exported := jar.Export()
	require.Len(t, exported, 1)
	require.Equal(t, now.Add(time.Minute), exported[0].Expires)
	require.Zero(t, exported[0].MaxAge)

	current = now.Add(61 * time.Second)
	require.Empty(t, jar.Cookies(target))
	require.Empty(t, jar.Export())
}

func TestCookieJarCanonicalizesImportedRelativeMaxAge(t *testing.T) {
	before := time.Now()
	jar := NewCookieJar([]StoredCookie{{
		Name:   "sessionid",
		Value:  "imported",
		Domain: "byteplus.com",
		Path:   "/",
		MaxAge: 120,
	}})
	after := time.Now()

	exported := jar.Export()
	require.Len(t, exported, 1)
	require.False(t, exported[0].Expires.Before(before.Add(120*time.Second)))
	require.False(t, exported[0].Expires.After(after.Add(120*time.Second)))
	require.Zero(t, exported[0].MaxAge)
}

func TestCookieJarRejectsCrossDomainSetCookie(t *testing.T) {
	jar := NewCookieJar(nil)
	target, err := url.Parse("https://ai.byteplus.com/")
	require.NoError(t, err)

	jar.SetCookies(target, []*http.Cookie{{Name: "foreign", Value: "secret", Domain: "example.com", Path: "/"}})
	require.Empty(t, jar.Export())
}
