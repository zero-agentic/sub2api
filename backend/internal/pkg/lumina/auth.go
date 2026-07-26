package lumina

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var publicDataPattern = regexp.MustCompile(`window\.__VOLC_PUBLIC_DATA__=(\{.*\});</script>`)

type LoginResult struct {
	UserID            string
	LoginCredentialID string
	CurState          string
}

type AuthChallengeError struct {
	Code      string
	Operation string
	Message   string
}

func (e *AuthChallengeError) Error() string {
	return "BytePlus login requires interactive authorization"
}

type AuthCredentialError struct {
	Code    string
	Message string
}

func (e *AuthCredentialError) Error() string {
	return "BytePlus login credentials were rejected"
}

type AuthProtocolError struct {
	Message string
}

func (e *AuthProtocolError) Error() string {
	return "BytePlus login protocol changed: " + e.Message
}

type passportResponse struct {
	EventName string `json:"EventName"`
	Result    struct {
		CurState          string `json:"CurState"`
		LoginCredentialID string `json:"LoginCredentialId"`
		NextOperation     struct {
			Name   string         `json:"Name"`
			Params map[string]any `json:"Params"`
		} `json:"NextOperation"`
		CurSession struct {
			Account struct {
				ID json.Number `json:"Id"`
			} `json:"Account"`
		} `json:"CurSession"`
		VerifyData any `json:"verify_data"`
	} `json:"Result"`
	ResponseMetadata struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
}

type loginPageData struct {
	Timestamp int64
	JWKURL    string
}

func (c *Client) AuthenticateWithPassword(ctx context.Context, identity, password string) (*LoginResult, error) {
	identity = strings.TrimSpace(identity)
	if identity == "" || password == "" {
		return nil, &AuthCredentialError{Code: "MissingCredentials", Message: "email and password are required"}
	}
	pageData, err := c.initializeLogin(ctx)
	if err != nil {
		return nil, err
	}
	if c.sharkWebID == "" {
		c.sharkWebID, err = generateSharkWebID()
		if err != nil {
			return nil, err
		}
	}
	credential, err := c.passportRequest(ctx, pageData, "/api/passport/login/getLoginCredential", map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	if credential.Result.CurState != "Blank" {
		return nil, &AuthProtocolError{Message: "unexpected initial state " + credential.Result.CurState}
	}

	loginPassword := password
	extraHeaders := make(http.Header)
	if pageData.JWKURL != "" {
		encrypted, keyID, encryptErr := c.encryptPassword(ctx, pageData.JWKURL, password)
		if encryptErr != nil {
			return nil, encryptErr
		}
		loginPassword = encrypted
		extraHeaders.Set("EncryptedKeyword", keyID)
		extraHeaders.Set("EncryptedFields", "Password")
	}
	response, err := c.passportRequest(ctx, pageData, "/api/passport/login/mixtureLogin", map[string]any{
		"Identity":  identity,
		"Password":  loginPassword,
		"EventName": "AuthAccountWithPassword",
	}, extraHeaders)
	if err != nil {
		return nil, err
	}
	if operation := strings.TrimSpace(response.Result.NextOperation.Name); operation != "" {
		return nil, &AuthChallengeError{Code: "NextOperation", Operation: operation}
	}
	if response.Result.LoginCredentialID == "" {
		return nil, &AuthProtocolError{Message: "successful response has no LoginCredentialId"}
	}
	return &LoginResult{
		UserID:            response.Result.CurSession.Account.ID.String(),
		LoginCredentialID: response.Result.LoginCredentialID,
		CurState:          response.Result.CurState,
	}, nil
}

func IsInteractiveAuthError(err error) bool {
	var challenge *AuthChallengeError
	return errors.As(err, &challenge)
}

func IsCredentialAuthError(err error) bool {
	var credential *AuthCredentialError
	return errors.As(err, &credential)
}

func (c *Client) initializeLogin(ctx context.Context) (*loginPageData, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ConsoleOrigin+"/auth/login/", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	request.Header.Set("User-Agent", c.userAgent)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &APIError{Status: response.StatusCode, Code: "LoginPageUnavailable"}
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBody {
		return nil, errors.New("BytePlus login page exceeds size limit")
	}
	match := publicDataPattern.FindSubmatch(payload)
	if len(match) != 2 {
		return nil, &AuthProtocolError{Message: "public login data was not found"}
	}
	var publicData struct {
		Timestamp int64 `json:"st"`
		Init      struct {
			Console struct {
				JWKURL string `json:"jwk_url"`
			} `json:"consoleConfig"`
		} `json:"INIT_CONFIG"`
	}
	if err := json.Unmarshal(match[1], &publicData); err != nil {
		return nil, &AuthProtocolError{Message: "invalid public login data"}
	}
	if publicData.Timestamp <= 0 || c.jar.Value(consoleURL, "csrfToken") == "" {
		return nil, &AuthProtocolError{Message: "login timestamp or CSRF token is missing"}
	}
	return &loginPageData{Timestamp: publicData.Timestamp, JWKURL: strings.TrimSpace(publicData.Init.Console.JWKURL)}, nil
}

func (c *Client) passportRequest(ctx context.Context, page *loginPageData, path string, body map[string]any, extraHeaders http.Header) (*passportResponse, error) {
	if page == nil {
		return nil, errors.New("BytePlus login page data is nil")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, ConsoleOrigin+path, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	signature, err := authenticationSign(page.Timestamp)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", "en-US,en;q=0.9")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", ConsoleOrigin)
	request.Header.Set("Referer", ConsoleOrigin+"/auth/login/")
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("X-Authentication-Sign", signature)
	request.Header.Set("X-CSRF-Token", c.jar.Value(consoleURL, "csrfToken"))
	request.Header.Set("X-Shark-Web-Id", c.sharkWebID)
	for key, values := range extraHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(responsePayload) > maxResponseBody {
		return nil, errors.New("BytePlus login response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, parseAPIError(response.StatusCode, responsePayload)
	}
	var decoded passportResponse
	decoder := json.NewDecoder(strings.NewReader(string(responsePayload)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, &AuthProtocolError{Message: "invalid passport response"}
	}
	if decoded.ResponseMetadata.Error != nil {
		return nil, classifyPassportError(decoded.ResponseMetadata.Error.Code, decoded.ResponseMetadata.Error.Message, decoded.Result.NextOperation.Name)
	}
	if decoded.Result.VerifyData != nil {
		return nil, &AuthChallengeError{Code: "ErrorNeedCaptcha", Message: "CAPTCHA is required"}
	}
	return &decoded, nil
}

func classifyPassportError(code, message, operation string) error {
	switch code {
	case "ErrorNeedCaptcha":
		return &AuthChallengeError{Code: code, Operation: operation, Message: message}
	case "PasswordExpired", "RepeatedlyInvalidPassword", "LoginLocked", "ErrAccountOnHold",
		"ErrorNoPermissionSite", "NoPermissionSite", "ErrorCrossSite":
		return &AuthChallengeError{Code: code, Operation: operation, Message: message}
	case "InvalidIdentityOrPassword", "AccountNotExist":
		return &AuthCredentialError{Code: code, Message: message}
	default:
		return &APIError{Status: http.StatusOK, Code: code, Message: message}
	}
}

func authenticationSign(timestamp int64) (string, error) {
	salt, err := randomAlphaNumeric(16)
	if err != nil {
		return "", err
	}
	timestampText := strconv.FormatInt(timestamp, 10)
	inner := url.Values{
		"radomSalt": {salt},
		"timeStamp": {reverseASCII(timestampText)},
	}.Encode()
	sign := reverseASCII(base64.StdEncoding.EncodeToString([]byte(inner)))
	content, err := json.Marshal(map[string]any{"radomSalt": salt, "timeStamp": timestamp})
	if err != nil {
		return "", err
	}
	return url.Values{"content": {string(content)}, "sign": {sign}}.Encode(), nil
}

func generateSharkWebID() (string, error) {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	value := make([]byte, 36)
	for index := range value {
		switch index {
		case 8, 13, 18, 23:
			value[index] = '_'
		case 14:
			value[index] = '4'
		default:
			selected, err := randomIndex(len(chars))
			if err != nil {
				return "", err
			}
			if index == 19 {
				selected = (selected & 3) | 8
			}
			value[index] = chars[selected]
		}
	}
	return "verify_" + strconv.FormatInt(timeNowUnixMilli(), 36) + "_" + string(value), nil
}

func randomAlphaNumeric(length int) (string, error) {
	const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	value := make([]byte, length)
	for index := range value {
		selected, err := randomIndex(len(chars))
		if err != nil {
			return "", err
		}
		value[index] = chars[selected]
	}
	return string(value), nil
}

func randomIndex(max int) (int, error) {
	if max <= 0 {
		return 0, errors.New("invalid random range")
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func reverseASCII(value string) string {
	data := []byte(value)
	for left, right := 0, len(data)-1; left < right; left, right = left+1, right-1 {
		data[left], data[right] = data[right], data[left]
	}
	return string(data)
}

func timeNowUnixMilli() int64 {
	return timeNow().UnixMilli()
}

var timeNow = func() time.Time {
	return time.Now()
}

func (c *Client) encryptPassword(ctx context.Context, rawJWKURL, password string) (string, string, error) {
	target, err := url.Parse(rawJWKURL)
	if err != nil {
		return "", "", &AuthProtocolError{Message: "invalid JWK URL"}
	}
	if !target.IsAbs() {
		target = consoleURL.ResolveReference(target)
	}
	if target.Scheme != "https" || !trustedHost(target.Hostname()) {
		return "", "", &AuthProtocolError{Message: "untrusted JWK URL"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-CSRF-Token", c.jar.Value(consoleURL, "csrfToken"))
	response, err := c.http.Do(request)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", &APIError{Status: response.StatusCode, Code: "JWKUnavailable"}
	}
	var keys struct {
		Keys []struct {
			KeyID    string `json:"kid"`
			Modulus  string `json:"n"`
			Exponent string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBody)).Decode(&keys); err != nil || len(keys.Keys) == 0 {
		return "", "", &AuthProtocolError{Message: "invalid JWK response"}
	}
	modulus, err := base64.RawURLEncoding.DecodeString(keys.Keys[0].Modulus)
	if err != nil {
		return "", "", &AuthProtocolError{Message: "invalid JWK modulus"}
	}
	exponentBytes, err := base64.RawURLEncoding.DecodeString(keys.Keys[0].Exponent)
	if err != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
		return "", "", &AuthProtocolError{Message: "invalid JWK exponent"}
	}
	var padded [4]byte
	copy(padded[4-len(exponentBytes):], exponentBytes)
	publicKey := &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(binary.BigEndian.Uint32(padded[:]))}
	encrypted, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, []byte(password))
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), keys.Keys[0].KeyID, nil
}
