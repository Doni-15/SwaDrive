package files

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

const (
	DefaultListLimit        = 100
	MaximumListLimit        = 500
	DefaultSearchLimit      = 50
	MaximumSearchLimit      = 200
	MaximumSearchQueryBytes = 256
	MaximumCursorBytes      = 12 << 10
	ReindexBatchSize        = 100

	KindFile      Kind = "file"
	KindDirectory Kind = "directory"
)

var (
	ErrInvalidPageCursor = errors.New("invalid file page cursor")
	ErrIndexInconsistent = errors.New("file metadata index is inconsistent")
	ErrInvalidSearch     = errors.New("invalid search request")
)

type Kind string

type Entry struct {
	ID             int64
	GenerationID   int64
	Path           string
	ParentPath     string
	Name           string
	NormalizedName string
	NormalizedPath string
	Kind           Kind
	Size           int64
	ModifiedAt     time.Time
	IndexedAt      time.Time
	TrashEntryID   string
	WholeSHA256    []byte
}

func (entry Entry) IsDirectory() bool {
	return entry.Kind == KindDirectory
}

type Page struct {
	Entries    []Entry
	NextCursor string
}

type SearchPage struct {
	Entries    []Entry
	NextCursor string
}

type Generation struct {
	ID          int64
	Status      string
	StartedAt   time.Time
	CompletedAt *time.Time
}

type indexCursor struct {
	Mode      string `json:"m"`
	Scope     string `json:"q"`
	Primary   string `json:"p"`
	Secondary string `json:"s"`
	Tertiary  string `json:"t,omitempty"`
}

type FileIndexRepository interface {
	CheckHealthy(ctx context.Context) error
	List(ctx context.Context, parentPath string, limit int, cursor indexCursor) ([]Entry, error)
	Metadata(ctx context.Context, logicalPath string) (Entry, error)
	Search(ctx context.Context, normalizedQuery string, limit int, cursor indexCursor) ([]Entry, error)
	BeginMutation(ctx context.Context, reason string, now time.Time) error
	ClearMutation(ctx context.Context, reason string, now time.Time) error
	CreateWithAuditAndRepair(ctx context.Context, entry Entry, event audit.Event, repairReason string) error
	MoveSubtreeWithAuditAndRepair(ctx context.Context, source, destination storage.Path, event audit.Event, repairReason string) error
	MarkUnhealthy(ctx context.Context, reason string, now time.Time) error
}

func NewEntry(logicalPath storage.Path, kind Kind, size int64, modifiedAt, indexedAt time.Time, trashEntryID string, wholeSHA256 []byte) (Entry, error) {
	value := logicalPath.String()
	if value == "" || (kind != KindFile && kind != KindDirectory) || size < 0 || len(wholeSHA256) != 0 && len(wholeSHA256) != 32 {
		return Entry{}, storage.ErrInvalidPath
	}
	name := path.Base(value)
	parent := path.Dir(value)
	if parent == "." {
		parent = ""
	}
	normalizedName := normalizeSearchValue(name)
	normalizedPath := normalizeSearchValue(value)
	if len(normalizedName) > storage.MaximumComponentBytes || len(normalizedPath) > storage.MaximumPathBytes {
		return Entry{}, storage.ErrInvalidPath
	}
	return Entry{
		Path:           value,
		ParentPath:     parent,
		Name:           name,
		NormalizedName: normalizedName,
		NormalizedPath: normalizedPath,
		Kind:           kind,
		Size:           size,
		ModifiedAt:     modifiedAt.UTC(),
		IndexedAt:      indexedAt.UTC(),
		TrashEntryID:   trashEntryID,
		WholeSHA256:    append([]byte(nil), wholeSHA256...),
	}, nil
}

func normalizeSearchValue(value string) string {
	return strings.ToLower(value)
}

func encodeCursor(cursor indexCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeCursor(value, mode, scope string) (indexCursor, error) {
	if value == "" {
		return indexCursor{Mode: mode, Scope: scope}, nil
	}
	if len(value) > MaximumCursorBytes {
		return indexCursor{}, ErrInvalidPageCursor
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) > MaximumCursorBytes {
		return indexCursor{}, ErrInvalidPageCursor
	}
	var cursor indexCursor
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Mode != mode || cursor.Scope != scope {
		return indexCursor{}, ErrInvalidPageCursor
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return indexCursor{}, ErrInvalidPageCursor
	}
	if cursor.Primary == "" || cursor.Secondary == "" {
		return indexCursor{}, ErrInvalidPageCursor
	}
	switch mode {
	case "list":
		logicalPath, parseErr := storage.ParsePath(cursor.Tertiary, false)
		if parseErr != nil || path.Base(logicalPath.String()) != cursor.Secondary || normalizeSearchValue(cursor.Secondary) != cursor.Primary {
			return indexCursor{}, ErrInvalidPageCursor
		}
	case "search":
		logicalPath, parseErr := storage.ParsePath(cursor.Secondary, false)
		if parseErr != nil || normalizeSearchValue(logicalPath.String()) != cursor.Primary || cursor.Tertiary != "" {
			return indexCursor{}, ErrInvalidPageCursor
		}
	default:
		return indexCursor{}, ErrInvalidPageCursor
	}
	return cursor, nil
}
