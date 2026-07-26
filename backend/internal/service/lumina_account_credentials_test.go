package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
	"github.com/stretchr/testify/require"
)

func TestValidateLuminaAccountCredentialsAcceptsCookieOrPasswordFallback(t *testing.T) {
	cookieCredentials := map[string]any{
		"cookie": []lumina.StoredCookie{{
			Name:   "sessionid",
			Value:  "session-value",
			Domain: ".byteplus.com",
			Path:   "/",
		}},
		"model_mapping": map[string]any{"seedance-2.0-pro": "Doubao-Seedance-2.0-pro"},
	}
	require.NoError(t, ValidateLuminaAccountCredentials(PlatformLumina, AccountTypeCookie, cookieCredentials))

	passwordCredentials := map[string]any{
		"email":    "owner@example.com",
		"password": " plaintext-by-user-request ",
	}
	require.NoError(t, ValidateLuminaAccountCredentials(PlatformLumina, AccountTypeCookie, passwordCredentials))
	require.Equal(t, " plaintext-by-user-request ", credentialSecretString(passwordCredentials, "password"))
}

func TestValidateLuminaAccountCredentialsRejectsInvalidPairingsAndSecrets(t *testing.T) {
	tests := []struct {
		name        string
		platform    string
		accountType string
		credentials map[string]any
	}{
		{
			name:        "Lumina requires cookie account type",
			platform:    PlatformLumina,
			accountType: AccountTypeOAuth,
			credentials: map[string]any{"email": "owner@example.com", "password": "secret"},
		},
		{
			name:        "cookie type is reserved for Lumina",
			platform:    PlatformOpenAI,
			accountType: AccountTypeCookie,
			credentials: map[string]any{"email": "owner@example.com", "password": "secret"},
		},
		{
			name:        "password fallback requires email",
			platform:    PlatformLumina,
			accountType: AccountTypeCookie,
			credentials: map[string]any{"password": "secret"},
		},
		{
			name:        "cookie domain must be BytePlus",
			platform:    PlatformLumina,
			accountType: AccountTypeCookie,
			credentials: map[string]any{"cookie": []lumina.StoredCookie{{Name: "sid", Value: "secret", Domain: "example.com"}}},
		},
		{
			name:        "model mapping values must be strings",
			platform:    PlatformLumina,
			accountType: AccountTypeCookie,
			credentials: map[string]any{
				"email":         "owner@example.com",
				"password":      "secret",
				"model_mapping": map[string]any{"seedance": 2},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, ValidateLuminaAccountCredentials(test.platform, test.accountType, test.credentials))
		})
	}
}
