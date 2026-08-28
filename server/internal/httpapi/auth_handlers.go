package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/auth"
)

const (
	maximumConcurrentLoginRequests = 64
	loginBodyReadTimeout           = 15 * time.Second
)

type userResponse struct {
	ID       int64     `json:"id"`
	Username string    `json:"username"`
	Role     auth.Role `json:"role"`
}

type sessionResponse struct {
	ID         int64  `json:"id"`
	ClientName string `json:"client_name"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	RevokedAt  *int64 `json:"revoked_at,omitempty"`
	LastSeenAt int64  `json:"last_seen_at"`
	Current    bool   `json:"current"`
}

func (server *server) login(w http.ResponseWriter, request *http.Request) {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(loginBodyReadTimeout)); err == nil {
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	} else if !errors.Is(err, http.ErrNotSupported) {
		server.logger.ErrorContext(request.Context(), "login read deadline unavailable", "request_id", requestID(request), "error_type", "read_deadline")
		w.Header().Set("Retry-After", "1")
		writeError(w, request, http.StatusServiceUnavailable, "server_busy", "The server could not accept another login request.")
		return
	}

	select {
	case server.loginAdmissions <- struct{}{}:
		defer func() { <-server.loginAdmissions }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, request, http.StatusServiceUnavailable, "server_busy", "The server is at its current resource limit. Try again shortly.")
		return
	}

	var body struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		ClientName string `json:"client_name"`
	}
	if err := decodeJSONPayload(w, request, &body); err != nil {
		writeDecodeError(w, request, err)
		return
	}

	result, err := server.auth.Login(request.Context(), auth.LoginInput{
		Username:   body.Username,
		Password:   body.Password,
		ClientName: body.ClientName,
		RemoteIP:   remoteIP(request),
		RequestID:  requestID(request),
	})
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Token   string          `json:"token"`
		User    userResponse    `json:"user"`
		Session sessionResponse `json:"session"`
	}{
		Token:   result.Token.Value(),
		User:    toUserResponse(result.User),
		Session: toSessionResponse(result.Session, result.Session.ID),
	})
}

func (server *server) logout(w http.ResponseWriter, request *http.Request) {
	if err := server.auth.Logout(request.Context(), identity(request)); err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (server *server) me(w http.ResponseWriter, request *http.Request) {
	currentIdentity := identity(request)
	writeJSON(w, http.StatusOK, struct {
		User    userResponse    `json:"user"`
		Session sessionResponse `json:"session"`
	}{
		User:    toUserResponse(currentIdentity.User),
		Session: toSessionResponse(currentIdentity.Session, currentIdentity.Session.ID),
	})
}

func (server *server) sessions(w http.ResponseWriter, request *http.Request) {
	currentIdentity := identity(request)
	sessions, err := server.auth.ListSessions(request.Context(), currentIdentity)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	response := make([]sessionResponse, 0, len(sessions))
	for _, session := range sessions {
		response = append(response, toSessionResponse(session, currentIdentity.Session.ID))
	}
	writeJSON(w, http.StatusOK, struct {
		Sessions []sessionResponse `json:"sessions"`
	}{Sessions: response})
}

func (server *server) revokeSession(w http.ResponseWriter, request *http.Request) {
	sessionID, err := parseCanonicalInt64(request.PathValue("id"), false)
	if err != nil {
		writeError(w, request, http.StatusNotFound, "not_found", "The requested resource was not found.")
		return
	}
	if err := server.auth.RevokeSession(request.Context(), identity(request), sessionID); err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toUserResponse(user auth.User) userResponse {
	return userResponse{ID: user.ID, Username: user.Username, Role: user.Role}
}

func toSessionResponse(session auth.Session, currentSessionID int64) sessionResponse {
	return sessionResponse{
		ID:         session.ID,
		ClientName: session.ClientName,
		CreatedAt:  session.CreatedAt.UTC().Unix(),
		ExpiresAt:  session.ExpiresAt.UTC().Unix(),
		RevokedAt:  unixTimePointer(session.RevokedAt),
		LastSeenAt: session.LastSeenAt.UTC().Unix(),
		Current:    session.ID == currentSessionID,
	}
}

func unixTimePointer(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	timestamp := value.UTC().Unix()
	return &timestamp
}
