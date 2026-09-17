package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ConversationSortField string

const (
	ConversationSortCreatedAt ConversationSortField = "createdAt"
	ConversationSortUpdatedAt ConversationSortField = "updatedAt"
)

type SortDirection string

const (
	SortDirectionAscending  SortDirection = "asc"
	SortDirectionDescending SortDirection = "desc"
)

// ConversationSort controls the deterministic order used by conversation lists.
// Explicit is true when the caller supplied either sort option. Explicit sorts use
// an opaque cursor that captures the timestamp at the time the page was read.
type ConversationSort struct {
	Field     ConversationSortField
	Direction SortDirection
	Explicit  bool
}

func DefaultConversationSort() ConversationSort {
	return ConversationSort{
		Field:     ConversationSortCreatedAt,
		Direction: SortDirectionDescending,
	}
}

func NormalizeConversationSort(sort ConversationSort) ConversationSort {
	if sort.Field == "" {
		sort.Field = ConversationSortCreatedAt
	}
	if sort.Direction == "" {
		sort.Direction = SortDirectionDescending
	}
	return sort
}

func ParseConversationSort(field, direction *string) (ConversationSort, error) {
	sort := DefaultConversationSort()
	sort.Explicit = field != nil || direction != nil
	if field != nil {
		switch strings.TrimSpace(*field) {
		case string(ConversationSortCreatedAt):
			sort.Field = ConversationSortCreatedAt
		case string(ConversationSortUpdatedAt):
			sort.Field = ConversationSortUpdatedAt
		default:
			return ConversationSort{}, fmt.Errorf("invalid conversation sort %q; expected createdAt or updatedAt", *field)
		}
	}
	if direction != nil {
		switch strings.TrimSpace(*direction) {
		case string(SortDirectionAscending):
			sort.Direction = SortDirectionAscending
		case string(SortDirectionDescending):
			sort.Direction = SortDirectionDescending
		default:
			return ConversationSort{}, fmt.Errorf("invalid sort direction %q; expected asc or desc", *direction)
		}
	}
	return sort, nil
}

type ConversationCursor struct {
	Value time.Time
	ID    string
}

type encodedConversationCursor struct {
	Version   int                   `json:"v"`
	Field     ConversationSortField `json:"field"`
	Direction SortDirection         `json:"direction"`
	Value     time.Time             `json:"value"`
	ID        string                `json:"id"`
}

const conversationCursorPrefix = "cs1."

// ParseConversationCursor returns an opaque cursor anchor or reports that the
// token is a legacy conversation ID that the store must resolve.
func ParseConversationCursor(token string, sort ConversationSort) (cursor *ConversationCursor, legacy bool, err error) {
	sort = NormalizeConversationSort(sort)
	if !sort.Explicit {
		return nil, true, nil
	}
	if !strings.HasPrefix(token, conversationCursorPrefix) {
		return nil, false, fmt.Errorf("invalid afterCursor for an explicitly sorted conversation list")
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, conversationCursorPrefix))
	if err != nil {
		return nil, false, fmt.Errorf("invalid afterCursor: malformed conversation sort cursor")
	}
	var decoded encodedConversationCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false, fmt.Errorf("invalid afterCursor: malformed conversation sort cursor")
	}
	if decoded.Version != 1 || decoded.ID == "" || decoded.Value.IsZero() {
		return nil, false, fmt.Errorf("invalid afterCursor: incomplete conversation sort cursor")
	}
	if decoded.Field != sort.Field || decoded.Direction != sort.Direction {
		return nil, false, fmt.Errorf("afterCursor sort does not match the requested sort and direction")
	}
	return &ConversationCursor{Value: decoded.Value, ID: decoded.ID}, false, nil
}

func EncodeConversationCursor(summary ConversationSummary, sort ConversationSort) (string, error) {
	sort = NormalizeConversationSort(sort)
	if !sort.Explicit {
		return summary.ID, nil
	}
	value := summary.CreatedAt
	if sort.Field == ConversationSortUpdatedAt {
		value = summary.UpdatedAt
	}
	payload, err := json.Marshal(encodedConversationCursor{
		Version:   1,
		Field:     sort.Field,
		Direction: sort.Direction,
		Value:     value,
		ID:        summary.ID,
	})
	if err != nil {
		return "", fmt.Errorf("encode conversation cursor: %w", err)
	}
	return conversationCursorPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}
