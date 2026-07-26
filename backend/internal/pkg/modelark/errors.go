package modelark

import (
	"net/http"
	"strings"
)

const (
	ErrorTypeBadRequest          = "BadRequest"
	ErrorTypeUnauthorized        = "Unauthorized"
	ErrorTypeForbidden           = "Forbidden"
	ErrorTypeNotFound            = "NotFound"
	ErrorTypeTooManyRequests     = "TooManyRequests"
	ErrorTypeInternalServerError = "InternalServerError"
)

func ErrorTypeForHTTPStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return ErrorTypeBadRequest
	case http.StatusUnauthorized:
		return ErrorTypeUnauthorized
	case http.StatusForbidden:
		return ErrorTypeForbidden
	case http.StatusNotFound:
		return ErrorTypeNotFound
	case http.StatusTooManyRequests:
		return ErrorTypeTooManyRequests
	default:
		return ErrorTypeInternalServerError
	}
}

func NewErrorResponse(status int, code, message, param string) ErrorResponse {
	return ErrorResponse{Error: ErrorDetail{
		Code:    code,
		Message: message,
		Param:   param,
		Type:    ErrorTypeForHTTPStatus(status),
	}}
}

// NormalizeError maps an internal HTTP status and reason code to the official
// ModelArk status/code pair. It is the single mapping shared by the gateway
// middleware (AbortWithError) and the ModelArk handler error renderer, so the
// two layers can never drift apart. Quota/balance failures are reported as
// 429 QuotaExceeded regardless of the internal status; a plain 429 is an
// account-level rate limit, not server overload.
func NormalizeError(status int, internalCode string) (int, string) {
	lowerCode := strings.ToLower(internalCode)
	switch {
	case strings.Contains(lowerCode, "quota") || strings.Contains(lowerCode, "balance"):
		return http.StatusTooManyRequests, "QuotaExceeded"
	case status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge:
		return status, "InvalidParameter"
	case status == http.StatusUnauthorized:
		return status, "AuthenticationError"
	case status == http.StatusForbidden:
		return status, "InvalidAccountStatus"
	case status == http.StatusNotFound:
		return status, "NotFound"
	case status == http.StatusTooManyRequests:
		return status, "AccountRateLimitExceeded"
	default:
		return http.StatusInternalServerError, "InternalServiceError"
	}
}
