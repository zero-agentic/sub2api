package lumina

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type StoredCookie struct {
	Name        string        `json:"name"`
	Value       string        `json:"value"`
	Domain      string        `json:"domain"`
	Path        string        `json:"path"`
	Expires     time.Time     `json:"expires,omitempty"`
	MaxAge      int           `json:"max_age,omitempty"`
	Secure      bool          `json:"secure,omitempty"`
	HTTPOnly    bool          `json:"http_only,omitempty"`
	SameSite    http.SameSite `json:"same_site,omitempty"`
	Partitioned bool          `json:"partitioned,omitempty"`
	HostOnly    bool          `json:"host_only,omitempty"`
}

type CookieJar struct {
	mu      sync.RWMutex
	cookies map[string]StoredCookie
	now     func() time.Time
}

func NewCookieJar(cookies []StoredCookie) *CookieJar {
	now := time.Now()
	jar := &CookieJar{
		cookies: make(map[string]StoredCookie, len(cookies)),
		now:     time.Now,
	}
	for _, cookie := range cookies {
		cookie = normalizeStoredCookie(cookie)
		cookie = canonicalizeMaxAge(cookie, now)
		if cookie.Name == "" || cookie.Domain == "" {
			continue
		}
		jar.cookies[cookieKey(cookie)] = cookie
	}
	return jar
}

func DecodeStoredCookies(value any) ([]StoredCookie, error) {
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cookies []StoredCookie
	if err := json.Unmarshal(payload, &cookies); err != nil {
		return nil, err
	}
	return cookies, nil
}

func (j *CookieJar) Cookies(target *url.URL) []*http.Cookie {
	if j == nil || target == nil {
		return nil
	}
	host := strings.ToLower(target.Hostname())
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	now := j.currentTime()

	j.mu.RLock()
	matched := make([]StoredCookie, 0, len(j.cookies))
	for _, cookie := range j.cookies {
		if cookieExpired(cookie, now) || !cookieMatchesHost(cookie, host) || !cookieMatchesPath(cookie.Path, path) {
			continue
		}
		if cookie.Secure && target.Scheme != "https" {
			continue
		}
		matched = append(matched, cookie)
	}
	j.mu.RUnlock()

	sort.SliceStable(matched, func(i, k int) bool {
		return len(matched[i].Path) > len(matched[k].Path)
	})
	result := make([]*http.Cookie, 0, len(matched))
	for _, cookie := range matched {
		result = append(result, &http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	return result
}

func (j *CookieJar) SetCookies(target *url.URL, cookies []*http.Cookie) {
	if j == nil || target == nil {
		return
	}
	host := strings.ToLower(target.Hostname())
	defaultPath := defaultCookiePath(target.Path)
	now := j.currentTime()

	j.mu.Lock()
	defer j.mu.Unlock()
	for _, incoming := range cookies {
		if incoming == nil || strings.TrimSpace(incoming.Name) == "" {
			continue
		}
		domain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(incoming.Domain), "."))
		hostOnly := domain == ""
		if hostOnly {
			domain = host
		}
		if domain != host && !strings.HasSuffix(host, "."+domain) {
			continue
		}
		path := incoming.Path
		if path == "" || path[0] != '/' {
			path = defaultPath
		}
		stored := StoredCookie{
			Name:        incoming.Name,
			Value:       incoming.Value,
			Domain:      domain,
			Path:        path,
			Expires:     incoming.Expires,
			MaxAge:      incoming.MaxAge,
			Secure:      incoming.Secure,
			HTTPOnly:    incoming.HttpOnly,
			SameSite:    incoming.SameSite,
			Partitioned: incoming.Partitioned,
			HostOnly:    hostOnly,
		}
		if incoming.MaxAge > 0 {
			stored.Expires = now.Add(time.Duration(incoming.MaxAge) * time.Second)
			stored.MaxAge = 0
		}
		stored = canonicalizeMaxAge(stored, now)
		key := cookieKey(stored)
		if incoming.MaxAge < 0 || (incoming.MaxAge == 0 && !incoming.Expires.IsZero() && !incoming.Expires.After(now)) {
			delete(j.cookies, key)
			continue
		}
		j.cookies[key] = stored
	}
}

func (j *CookieJar) Export() []StoredCookie {
	if j == nil {
		return nil
	}
	now := j.currentTime()
	j.mu.RLock()
	result := make([]StoredCookie, 0, len(j.cookies))
	for _, cookie := range j.cookies {
		if !cookieExpired(cookie, now) {
			result = append(result, cookie)
		}
	}
	j.mu.RUnlock()
	sort.Slice(result, func(i, k int) bool {
		if result[i].Domain != result[k].Domain {
			return result[i].Domain < result[k].Domain
		}
		if result[i].Path != result[k].Path {
			return result[i].Path < result[k].Path
		}
		return result[i].Name < result[k].Name
	})
	return result
}

func (j *CookieJar) Value(target *url.URL, name string) string {
	for _, cookie := range j.Cookies(target) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (j *CookieJar) currentTime() time.Time {
	if j != nil && j.now != nil {
		return j.now()
	}
	return time.Now()
}

func normalizeStoredCookie(cookie StoredCookie) StoredCookie {
	cookie.Domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cookie.Domain), "."))
	if cookie.Path == "" || cookie.Path[0] != '/' {
		cookie.Path = "/"
	}
	return cookie
}

func canonicalizeMaxAge(cookie StoredCookie, now time.Time) StoredCookie {
	if cookie.MaxAge > 0 && cookie.Expires.IsZero() {
		cookie.Expires = now.Add(time.Duration(cookie.MaxAge) * time.Second)
		cookie.MaxAge = 0
	}
	return cookie
}

func cookieKey(cookie StoredCookie) string {
	return cookie.Domain + "\x00" + cookie.Path + "\x00" + cookie.Name
}

func cookieExpired(cookie StoredCookie, now time.Time) bool {
	return cookie.MaxAge < 0 || (!cookie.Expires.IsZero() && !cookie.Expires.After(now))
}

func cookieMatchesHost(cookie StoredCookie, host string) bool {
	if cookie.HostOnly {
		return host == cookie.Domain
	}
	return host == cookie.Domain || strings.HasSuffix(host, "."+cookie.Domain)
}

func cookieMatchesPath(cookiePath, requestPath string) bool {
	if cookiePath == "/" || cookiePath == requestPath {
		return true
	}
	if !strings.HasPrefix(requestPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || (len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/')
}

func defaultCookiePath(requestPath string) string {
	if requestPath == "" || requestPath[0] != '/' || requestPath == "/" {
		return "/"
	}
	index := strings.LastIndex(requestPath, "/")
	if index <= 0 {
		return "/"
	}
	return requestPath[:index]
}
