package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

func (repository *SQLiteRepository) Append(ctx context.Context, event Event, metadataJSON []byte) (int64, error) {
	return appendEvent(ctx, repository.db, event, metadataJSON)
}

// AppendInTransaction validates and appends an event to the caller's SQLite
// transaction. It is intentionally narrow: security repositories use it only
// when their state change and audit row must commit together.
func (repository *SQLiteRepository) AppendInTransaction(ctx context.Context, tx *sql.Tx, event Event) (int64, error) {
	prepared, metadataJSON, err := prepareEvent(event, time.Now)
	if err != nil {
		return 0, err
	}
	id, err := appendEvent(ctx, tx, prepared, metadataJSON)
	if err != nil {
		return 0, errors.Join(ErrPersistence, err)
	}
	return id, nil
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func appendEvent(ctx context.Context, executor sqlExecutor, event Event, metadataJSON []byte) (int64, error) {
	result, err := executor.ExecContext(ctx, `
		INSERT INTO audit_events (
			occurred_at, actor_user_id, actor_session_id, event_type, outcome,
			resource_type, resource_id, request_id, remote_ip, metadata_json,
			resource_path, destination_path
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		event.OccurredAt.UTC().Unix(),
		nullableInt64(event.ActorUserID),
		nullableInt64(event.ActorSessionID),
		event.Type,
		event.Outcome,
		nullableString(event.ResourceType),
		nullableString(event.ResourceID),
		nullableString(event.RequestID),
		nullableString(event.RemoteIP),
		nullableBytes(metadataJSON),
		nullableString(event.ResourcePath),
		nullableString(event.DestinationPath),
	)
	if err != nil {
		return 0, fmt.Errorf("append audit event: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read audit event ID: %w", err)
	}
	return id, nil
}

func (repository *SQLiteRepository) List(ctx context.Context, filter ListFilter) ([]StoredEvent, error) {
	var actorUserID any
	if filter.ActorUserID != nil {
		actorUserID = *filter.ActorUserID
	}

	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, occurred_at, actor_user_id, actor_session_id, event_type, outcome,
		       resource_type, resource_id, request_id, remote_ip, metadata_json,
		       resource_path, destination_path
		FROM audit_events
		WHERE (? = '' OR event_type = ?)
		  AND (? IS NULL OR actor_user_id = ?)
		  AND (? = '' OR outcome = ?)
		  AND (? = 0 OR id < ?)
		ORDER BY id DESC
		LIMIT ?
	`,
		filter.EventType, filter.EventType,
		actorUserID, actorUserID,
		filter.Outcome, filter.Outcome,
		filter.BeforeID, filter.BeforeID,
		filter.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	events := make([]StoredEvent, 0, filter.Limit)
	for rows.Next() {
		var event StoredEvent
		var occurredAt int64
		var actorUserIDValue, actorSessionIDValue sql.NullInt64
		var resourceType, resourceID, requestID, remoteIP, metadataJSON, resourcePath, destinationPath sql.NullString
		if err := rows.Scan(
			&event.ID,
			&occurredAt,
			&actorUserIDValue,
			&actorSessionIDValue,
			&event.Type,
			&event.Outcome,
			&resourceType,
			&resourceID,
			&requestID,
			&remoteIP,
			&metadataJSON,
			&resourcePath,
			&destinationPath,
		); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}

		event.OccurredAt = time.Unix(occurredAt, 0).UTC()
		event.ActorUserID = int64Pointer(actorUserIDValue)
		event.ActorSessionID = int64Pointer(actorSessionIDValue)
		event.ResourceType = resourceType.String
		event.ResourceID = resourceID.String
		event.RequestID = requestID.String
		event.RemoteIP = remoteIP.String
		event.ResourcePath = resourcePath.String
		event.DestinationPath = destinationPath.String
		if metadataJSON.Valid {
			event.MetadataJSON = json.RawMessage(metadataJSON.String)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return events, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func int64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}
