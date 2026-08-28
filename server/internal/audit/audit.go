// Package audit records and lists append-only security and administrative events.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"

	EventOwnerBootstrap        = "auth.owner_bootstrap"
	EventOwnerCredentialsReset = "auth.owner_credentials_reset"
	EventLoginSuccess          = "auth.login_success"
	EventLoginFailure          = "auth.login_failure"
	EventLoginRateLimited      = "auth.login_rate_limited"
	EventLogout                = "auth.logout"
	EventSessionRevoked        = "auth.session_revoked"
	EventFolderCreated         = "files.folder_created"
	EventFileDownloaded        = "files.download_requested"
	EventFileMoved             = "files.moved"
	EventFileTrashed           = "files.trashed"
	EventFileRestored          = "files.restored"
	EventUploadCreated         = "uploads.created"
	EventUploadCompleted       = "uploads.completed"
	EventUploadCancelled       = "uploads.cancelled"
	EventUploadSecurityFailed  = "uploads.security_failure"

	DefaultPageSize = 50
	MaximumPageSize = 200

	maxMetadataJSONBytes = 4096
	maxMetadataEntries   = 16
	maxMetadataValueSize = 256
)

var (
	ErrInvalidEvent    = errors.New("invalid audit event")
	ErrUnsafeMetadata  = errors.New("unsafe audit metadata")
	ErrInvalidList     = errors.New("invalid audit list request")
	ErrOwnerRoleNeeded = errors.New("owner role required")
	ErrPersistence     = errors.New("audit event persistence failed")
)

type Event struct {
	ID              int64
	OccurredAt      time.Time
	ActorUserID     *int64
	ActorSessionID  *int64
	Type            string
	Outcome         string
	ResourceType    string
	ResourceID      string
	ResourcePath    string
	DestinationPath string
	RequestID       string
	RemoteIP        string
	Metadata        map[string]string
}

type StoredEvent struct {
	ID              int64
	OccurredAt      time.Time
	ActorUserID     *int64
	ActorSessionID  *int64
	Type            string
	Outcome         string
	ResourceType    string
	ResourceID      string
	ResourcePath    string
	DestinationPath string
	RequestID       string
	RemoteIP        string
	MetadataJSON    json.RawMessage
}

type ListFilter struct {
	EventType   string
	ActorUserID *int64
	Outcome     string
	BeforeID    int64
	Limit       int
}

type Page struct {
	Events     []StoredEvent
	NextCursor int64
}

type Repository interface {
	Append(ctx context.Context, event Event, metadataJSON []byte) (int64, error)
	List(ctx context.Context, filter ListFilter) ([]StoredEvent, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (service *Service) Record(ctx context.Context, event Event) error {
	event, metadataJSON, err := prepareEvent(event, service.now)
	if err != nil {
		return err
	}
	_, err = service.repository.Append(ctx, event, metadataJSON)
	if err != nil {
		return errors.Join(ErrPersistence, err)
	}
	return nil
}

func prepareEvent(event Event, now func() time.Time) (Event, []byte, error) {
	if err := validateEvent(event); err != nil {
		return Event{}, nil, err
	}
	metadataJSON, err := encodeMetadata(event.Metadata)
	if err != nil {
		return Event{}, nil, err
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	return event, metadataJSON, nil
}

func (service *Service) List(ctx context.Context, actorRole string, filter ListFilter) (Page, error) {
	if actorRole != "owner" {
		return Page{}, ErrOwnerRoleNeeded
	}
	if filter.Limit == 0 {
		filter.Limit = DefaultPageSize
	}
	if filter.Limit < 1 || filter.Limit > MaximumPageSize || filter.BeforeID < 0 {
		return Page{}, ErrInvalidList
	}
	if filter.Outcome != "" && filter.Outcome != OutcomeSuccess && filter.Outcome != OutcomeFailure && filter.Outcome != OutcomeDenied {
		return Page{}, ErrInvalidList
	}
	if len(filter.EventType) > 128 || (filter.ActorUserID != nil && *filter.ActorUserID <= 0) {
		return Page{}, ErrInvalidList
	}

	requestedLimit := filter.Limit
	filter.Limit++
	events, err := service.repository.List(ctx, filter)
	if err != nil {
		return Page{}, err
	}

	page := Page{Events: events}
	if len(page.Events) > requestedLimit {
		page.Events = page.Events[:requestedLimit]
		page.NextCursor = page.Events[len(page.Events)-1].ID
	}
	return page, nil
}

func validateEvent(event Event) error {
	if len(event.Type) < 1 || len(event.Type) > 128 {
		return fmt.Errorf("%w: event type", ErrInvalidEvent)
	}
	if event.Outcome != OutcomeSuccess && event.Outcome != OutcomeFailure && event.Outcome != OutcomeDenied {
		return fmt.Errorf("%w: outcome", ErrInvalidEvent)
	}
	if len(event.ResourceType) > 64 || len(event.ResourceID) > 256 || len(event.RequestID) > 128 || len(event.RemoteIP) > 64 {
		return fmt.Errorf("%w: field length", ErrInvalidEvent)
	}
	for _, logicalPath := range []string{event.ResourcePath, event.DestinationPath} {
		if logicalPath == "" {
			continue
		}
		if _, err := storage.ParsePath(logicalPath, false); err != nil {
			return fmt.Errorf("%w: logical resource path", ErrInvalidEvent)
		}
	}
	return nil
}

func encodeMetadata(metadata map[string]string) ([]byte, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	if len(metadata) > maxMetadataEntries {
		return nil, ErrUnsafeMetadata
	}

	for key, value := range metadata {
		if key != "reason_code" || !allowedReasonCode(value) || len(value) > maxMetadataValueSize {
			return nil, ErrUnsafeMetadata
		}
	}

	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode audit metadata: %w", err)
	}
	if len(encoded) > maxMetadataJSONBytes {
		return nil, ErrUnsafeMetadata
	}
	return encoded, nil
}

func allowedReasonCode(value string) bool {
	switch value {
	case "account_rate_limit", "chunk_checksum", "chunk_length", "chunk_retry_conflict", "expired", "ip_rate_limit", "part_size", "reconciled", "whole_file_integrity":
		return true
	default:
		return false
	}
}
