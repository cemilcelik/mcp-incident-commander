package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"mcp-incident-commander/internal/mcp"
)

// Yetkilendirme başlığı ve hata sabitleri.
const (
	HeaderAuthorization = "Authorization"
	BearerPrefix        = "Bearer "

	ErrCodeUnauthorized = -32004

	errMsgMissingAuthHeader = "missing Authorization header"
	errMsgInvalidAuthScheme = "Authorization header must use Bearer scheme"
	errMsgInvalidToken      = "invalid or expired JWT bearer token"
	errMsgMissingClaims     = "JWT token is missing required tenant_id or engineer_id claims"
)

// Standart Go hataları.
var (
	ErrEmptySigningSecret = errors.New("jwt signing secret cannot be empty")
	ErrEmptyIdentityClaim = errors.New("tenant_id and engineer_id cannot be empty")
)

// authContextKey context.Context içinde kimlik bilgisini çarpışmasız saklamak için kullanılan özel anahtar tipidir.
type authContextKey struct{}

// Identity doğrulanmış JWT içerisinden çıkarılan kiracı ve mühendis kimliğini temsil eder.
type Identity struct {
	TenantID   string `json:"tenant_id"`
	EngineerID string `json:"engineer_id"`
}

// IncidentClaims JWT payload içerisinde taşınan özel alanları ve standart kayıtlı alanları temsil eder.
type IncidentClaims struct {
	TenantID   string `json:"tenant_id"`
	EngineerID string `json:"engineer_id"`
	jwt.RegisteredClaims
}

// WithIdentity verilen Identity bilgisini context.Context içine güvenli şekilde gömer.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, authContextKey{}, id)
}

// IdentityFromContext context.Context içinden doğrulanmış Identity bilgisini okur.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(authContextKey{}).(Identity)
	if !ok || strings.TrimSpace(id.TenantID) == "" || strings.TrimSpace(id.EngineerID) == "" {
		return Identity{}, false
	}
	return id, true
}

// GenerateToken belirtilen kiracı ve mühendis için HS256 imzalı bir Stateless JWT üretir.
func GenerateToken(signingSecret []byte, tenantID, engineerID string, ttl time.Duration) (string, error) {
	if len(signingSecret) == 0 {
		return "", ErrEmptySigningSecret
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(engineerID) == "" {
		return "", ErrEmptyIdentityClaim
	}

	now := time.Now().UTC()
	claims := IncidentClaims{
		TenantID:   tenantID,
		EngineerID: engineerID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(signingSecret)
}

// NewJWTAuthMiddleware Authorization: Bearer <token> başlığını doğrulayan HTTP middleware döndürür.
func NewJWTAuthMiddleware(signingSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(signingSecret) == 0 {
				writeAuthError(w, http.StatusInternalServerError, "server authentication configuration error")
				return
			}

			authHeader := strings.TrimSpace(r.Header.Get(HeaderAuthorization))
			if authHeader == "" {
				writeAuthError(w, http.StatusUnauthorized, errMsgMissingAuthHeader)
				return
			}

			if !strings.HasPrefix(authHeader, BearerPrefix) {
				writeAuthError(w, http.StatusUnauthorized, errMsgInvalidAuthScheme)
				return
			}

			rawToken := strings.TrimSpace(strings.TrimPrefix(authHeader, BearerPrefix))
			if rawToken == "" {
				writeAuthError(w, http.StatusUnauthorized, errMsgInvalidToken)
				return
			}

			claims := &IncidentClaims{}
			parsedToken, err := jwt.ParseWithClaims(rawToken, claims, func(token *jwt.Token) (any, error) {
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
				}
				return signingSecret, nil
			})
			if err != nil || !parsedToken.Valid {
				writeAuthError(w, http.StatusUnauthorized, errMsgInvalidToken)
				return
			}

			tenantID := strings.TrimSpace(claims.TenantID)
			engineerID := strings.TrimSpace(claims.EngineerID)
			if tenantID == "" || engineerID == "" {
				writeAuthError(w, http.StatusUnauthorized, errMsgMissingClaims)
				return
			}

			ctx := WithIdentity(r.Context(), Identity{
				TenantID:   tenantID,
				EngineerID: engineerID,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeAuthError kimlik doğrulama hatalarını JSON-RPC 2.0 zarfında 401/500 olarak yazar (hassas token verisi loglanmaz/dönülmez).
func writeAuthError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(statusCode)
	resp := mcp.NewJSONRPCErrorResponse(nil, ErrCodeUnauthorized, message, nil)
	_ = json.NewEncoder(w).Encode(resp)
}
