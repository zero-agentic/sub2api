package service

import (
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
)

func ValidateLuminaAccountCredentials(platform, accountType string, credentials map[string]any) error {
	isLumina := strings.EqualFold(strings.TrimSpace(platform), PlatformLumina)
	isCookie := strings.EqualFold(strings.TrimSpace(accountType), AccountTypeCookie)
	if !isLumina && !isCookie {
		return nil
	}
	if !isLumina || !isCookie {
		return infraerrors.BadRequest(
			"LUMINA_ACCOUNT_TYPE_INVALID",
			"Lumina accounts must use the cookie account type, and cookie accounts are reserved for Lumina",
		)
	}
	if credentials == nil {
		return infraerrors.BadRequest("LUMINA_CREDENTIALS_REQUIRED", "Lumina credentials are required")
	}

	cookies, err := lumina.DecodeStoredCookies(credentials["cookie"])
	if err != nil {
		return infraerrors.BadRequest("LUMINA_COOKIE_INVALID", "Lumina cookie must be a JSON cookie array")
	}
	hasCookie := false
	for _, cookie := range cookies {
		if !isTrustedLuminaCookieDomain(cookie.Domain) {
			return infraerrors.BadRequest(
				"LUMINA_COOKIE_DOMAIN_INVALID",
				fmt.Sprintf("Lumina cookie %q does not belong to byteplus.com", cookie.Name),
			)
		}
		if strings.TrimSpace(cookie.Name) != "" && cookie.Value != "" {
			hasCookie = true
		}
	}

	email := strings.TrimSpace(credentialString(credentials, "email"))
	password := credentialSecretString(credentials, "password")
	if (email == "") != (password == "") {
		return infraerrors.BadRequest(
			"LUMINA_PASSWORD_CREDENTIALS_INCOMPLETE",
			"Lumina email and password must be configured together",
		)
	}
	if !hasCookie && (email == "" || password == "") {
		return infraerrors.BadRequest(
			"LUMINA_AUTH_CREDENTIALS_REQUIRED",
			"Configure a Lumina cookie jar or both email and password",
		)
	}
	if raw, exists := credentials["model_mapping"]; exists && raw != nil && !isLuminaStringMapping(raw) {
		return infraerrors.BadRequest(
			"LUMINA_MODEL_MAPPING_INVALID",
			"Lumina model_mapping must be an object with string keys and string values",
		)
	}
	return nil
}

func isLuminaStringMapping(raw any) bool {
	switch mapping := raw.(type) {
	case map[string]any:
		for _, value := range mapping {
			if _, ok := value.(string); !ok {
				return false
			}
		}
		return true
	case map[string]string:
		return true
	default:
		return false
	}
}

func isTrustedLuminaCookieDomain(domain string) bool {
	domain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(domain), "."))
	return domain == "byteplus.com" || strings.HasSuffix(domain, ".byteplus.com")
}
