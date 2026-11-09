package api

import "net/http"

// Error types and codes for /v1 responses. Every /v1 error body has the
// shape {"error": {"type": "...", "code": "...", "message": "..."}}.
// `code` is the stable, machine-readable value integrators branch on;
// `message` is for humans and may change.
const (
	errTypeInvalidRequest = "invalid_request_error"
	errTypeAuthentication = "authentication_error"
	errTypePermission     = "permission_error"
	errTypeNotFound       = "not_found_error"
	errTypeServer         = "server_error"

	codeInvalidRequest     = "invalid_request"
	codeUnauthorized       = "unauthorized"
	codeForbidden          = "forbidden"
	codeAgentNotFound      = "agent_not_found"
	codeProjectNotFound    = "project_not_found"
	codeQuotaNotFound      = "quota_not_found"
	codeAgentQuotaExceeded = "agent_quota_exceeded"
	codeNotConfigured      = "not_configured"
	codePaymentRequired    = "payment_required"
	codeInternal           = "internal_error"
	// codeNoPendingApproval: nothing waits for a local-agent decision.
	codeNoPendingApproval = "no_pending_approval"
	// codeAgentError: the agent's turn failed, e.g. its model API
	// returned an error.
	codeAgentError = "agent_error"
)

// writeAPIError writes the unified /v1 error body.
func writeAPIError(w http.ResponseWriter, status int, errType, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"type":    errType,
			"code":    code,
			"message": message,
		},
	})
}

func writeBadRequest(w http.ResponseWriter, message string) {
	writeAPIError(w, http.StatusBadRequest, errTypeInvalidRequest, codeInvalidRequest, message)
}

func writeAgentNotFound(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, errTypeNotFound, codeAgentNotFound, "agent not found")
}

func writeServerError(w http.ResponseWriter, err error) {
	writeAPIError(w, http.StatusInternalServerError, errTypeServer, codeInternal, err.Error())
}
