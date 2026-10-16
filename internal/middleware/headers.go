package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"mcp-incident-commander/internal/mcp"
)

// HTTP istek sınırları ve içerik tipi sabitleri.
const (
	MaxRequestBodyBytes = 1 << 20 // 1 MiB maksimum JSON-RPC gövde boyutu
	ContentTypeJSON     = "application/json"
)

// SEP-2243 L7 başlık doğrulama hata mesajı sabitleri.
const (
	errMsgMethodNotAllowed       = "only HTTP POST is supported for Stateless MCP requests"
	errMsgSessionHeaderForbidden = "Mcp-Session-Id header is strictly forbidden in Stateless MCP 2026-07-28"
	errMsgMissingMCPMethodHeader = "missing required SEP-2243 header: Mcp-Method"
	errMsgMissingMCPNameHeader   = "missing required SEP-2243 header for tools/call: Mcp-Name"
	errMsgUnexpectedMCPName      = "Mcp-Name header is only permitted when Mcp-Method is tools/call"
	errMsgInvalidJSONBody        = "failed to parse JSON-RPC body during SEP-2243 header validation"
)

// toolNameProbe sadece tools/call gövdesindeki araç adını hızlıca doğrulamak için kullanılır.
type toolNameProbe struct {
	Name string `json:"name"`
}

// ValidateSEP2243Headers Mcp-Method ve Mcp-Name başlıklarını doğrular ve JSON gövdesiyle tutarlılığını denetler.
func ValidateSEP2243Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeHeaderValidationError(w, http.StatusMethodNotAllowed, nil, mcp.ErrCodeInvalidRequest, errMsgMethodNotAllowed)
			return
		}

		// Stateless MCP 2026-07-28 kuralı: Mcp-Session-Id başlığı kesinlikle yasaktır.
		if strings.TrimSpace(r.Header.Get(mcp.HeaderMCPSessionID)) != "" {
			writeHeaderValidationError(w, http.StatusBadRequest, nil, mcp.ErrCodeStatefulHandshakeForbidden, errMsgSessionHeaderForbidden)
			return
		}

		headerMethod := strings.TrimSpace(r.Header.Get(mcp.HeaderMCPMethod))
		if headerMethod == "" {
			writeHeaderValidationError(w, http.StatusBadRequest, nil, mcp.ErrCodeInvalidRequest, errMsgMissingMCPMethodHeader)
			return
		}

		headerToolName := strings.TrimSpace(r.Header.Get(mcp.HeaderMCPName))
		if headerMethod == mcp.MethodToolsCall && headerToolName == "" {
			writeHeaderValidationError(w, http.StatusBadRequest, nil, mcp.ErrCodeInvalidRequest, errMsgMissingMCPNameHeader)
			return
		}
		if headerMethod != mcp.MethodToolsCall && headerToolName != "" {
			writeHeaderValidationError(w, http.StatusBadRequest, nil, mcp.ErrCodeInvalidRequest, errMsgUnexpectedMCPName)
			return
		}

		bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBodyBytes))
		if err != nil {
			writeHeaderValidationError(w, http.StatusBadRequest, nil, mcp.ErrCodeParseError, errMsgInvalidJSONBody)
			return
		}
		_ = r.Body.Close()

		// Sonraki handler'ların gövdeyi tekrar okuyabilmesi için r.Body'yi geri yükle.
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		var rpcReq mcp.JSONRPCRequest
		if err := json.Unmarshal(bodyBytes, &rpcReq); err != nil {
			writeHeaderValidationError(w, http.StatusBadRequest, nil, mcp.ErrCodeParseError, errMsgInvalidJSONBody)
			return
		}

		if strings.TrimSpace(rpcReq.Method) != headerMethod {
			msg := fmt.Sprintf("SEP-2243 header mismatch: Mcp-Method header %q does not match JSON-RPC method %q", headerMethod, rpcReq.Method)
			writeHeaderValidationError(w, http.StatusBadRequest, rpcReq.ID, mcp.ErrCodeInvalidRequest, msg)
			return
		}

		if headerMethod == mcp.MethodToolsCall {
			var probe toolNameProbe
			if len(rpcReq.Params) == 0 || json.Unmarshal(rpcReq.Params, &probe) != nil {
				writeHeaderValidationError(w, http.StatusBadRequest, rpcReq.ID, mcp.ErrCodeInvalidParams, errMsgInvalidJSONBody)
				return
			}
			if strings.TrimSpace(probe.Name) != headerToolName {
				msg := fmt.Sprintf("SEP-2243 header mismatch: Mcp-Name header %q does not match params.name %q", headerToolName, probe.Name)
				writeHeaderValidationError(w, http.StatusBadRequest, rpcReq.ID, mcp.ErrCodeInvalidRequest, msg)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// writeHeaderValidationError başlık doğrulama hatalarını JSON-RPC 2.0 formatında yazar.
func writeHeaderValidationError(w http.ResponseWriter, statusCode int, id any, rpcCode int, message string) {
	w.Header().Set("Content-Type", ContentTypeJSON)
	w.WriteHeader(statusCode)
	resp := mcp.NewJSONRPCErrorResponse(id, rpcCode, message, nil)
	_ = json.NewEncoder(w).Encode(resp)
}
