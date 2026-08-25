package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
)

type auditEventResponse struct {
	ID              int64           `json:"id"`
	OccurredAt      int64           `json:"occurred_at"`
	ActorUserID     *int64          `json:"actor_user_id,omitempty"`
	ActorSessionID  *int64          `json:"actor_session_id,omitempty"`
	EventType       string          `json:"event_type"`
	Outcome         string          `json:"outcome"`
	ResourceType    string          `json:"resource_type,omitempty"`
	ResourceID      string          `json:"resource_id,omitempty"`
	ResourcePath    string          `json:"resource_path,omitempty"`
	DestinationPath string          `json:"destination_path,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	RemoteIP        string          `json:"remote_ip,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

func (server *server) auditEvents(w http.ResponseWriter, request *http.Request) {
	if !hasOnlyQueryParameters(request, "event_type", "outcome", "actor_user_id", "cursor", "limit") {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The audit filter is invalid.")
		return
	}
	query := request.URL.Query()
	filter := audit.ListFilter{
		EventType: query.Get("event_type"),
		Outcome:   query.Get("outcome"),
	}
	if value := query.Get("actor_user_id"); value != "" {
		actorUserID, err := parseCanonicalInt64(value, false)
		if err != nil {
			writeError(w, request, http.StatusBadRequest, "invalid_request", "The audit filter is invalid.")
			return
		}
		filter.ActorUserID = &actorUserID
	}
	if value := query.Get("cursor"); value != "" {
		cursor, err := parseCanonicalInt64(value, false)
		if err != nil {
			writeError(w, request, http.StatusBadRequest, "invalid_request", "The audit cursor is invalid.")
			return
		}
		filter.BeforeID = cursor
	}
	if value := query.Get("limit"); value != "" {
		limit, err := parseCanonicalInt64(value, false)
		if err != nil {
			writeError(w, request, http.StatusBadRequest, "invalid_request", "The audit limit is invalid.")
			return
		}
		filter.Limit = int(limit)
	}

	page, err := server.audit.List(request.Context(), string(identity(request).User.Role), filter)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	events := make([]auditEventResponse, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, auditEventResponse{
			ID:              event.ID,
			OccurredAt:      event.OccurredAt.UTC().Unix(),
			ActorUserID:     event.ActorUserID,
			ActorSessionID:  event.ActorSessionID,
			EventType:       event.Type,
			Outcome:         event.Outcome,
			ResourceType:    event.ResourceType,
			ResourceID:      event.ResourceID,
			ResourcePath:    event.ResourcePath,
			DestinationPath: event.DestinationPath,
			RequestID:       event.RequestID,
			RemoteIP:        event.RemoteIP,
			Metadata:        event.MetadataJSON,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Events     []auditEventResponse `json:"events"`
		NextCursor int64                `json:"next_cursor,omitempty"`
	}{Events: events, NextCursor: page.NextCursor})
}
