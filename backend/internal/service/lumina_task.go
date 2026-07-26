package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

const (
	LuminaTaskTypeImage = "image"
	LuminaTaskTypeVideo = "video"

	LuminaTaskStatusQueued    = "queued"
	LuminaTaskStatusRunning   = "running"
	LuminaTaskStatusSucceeded = "succeeded"
	LuminaTaskStatusFailed    = "failed"
	LuminaTaskStatusCancelled = "cancelled"
	LuminaTaskStatusExpired   = "expired"
)

var (
	ErrLuminaTaskNotFound = errors.New("lumina task not found")
	ErrLuminaTaskExists   = errors.New("lumina task already exists")
)

type LuminaTask struct {
	ID              int64
	TaskID          string
	UserID          int64
	APIKeyID        int64
	GroupID         int64
	AccountID       int64
	UpstreamTaskID  string
	TaskType        string
	Model           string
	Status          string
	RequestPayload  map[string]any
	ResponsePayload map[string]any
	ErrorCode       *string
	ErrorMessage    *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
	UserDeletedAt   *time.Time
}

type CreateLuminaTaskParams struct {
	TaskID          string
	UserID          int64
	APIKeyID        int64
	GroupID         int64
	AccountID       int64
	UpstreamTaskID  string
	TaskType        string
	Model           string
	Status          string
	RequestPayload  map[string]any
	ResponsePayload map[string]any
}

type UpdateLuminaTaskParams struct {
	Status          string
	ResponsePayload map[string]any
	ErrorCode       *string
	ErrorMessage    *string
	CompletedAt     *time.Time
}

type LuminaTaskFilter struct {
	Status        string
	Model         string
	TaskIDs       []string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Limit         int
	Offset        int
}

type LuminaTaskRepository interface {
	CreateLuminaTask(ctx context.Context, params CreateLuminaTaskParams) (*LuminaTask, error)
	GetLuminaTaskForOwner(ctx context.Context, userID, apiKeyID int64, taskID string) (*LuminaTask, error)
	ListLuminaTasksForOwner(ctx context.Context, userID, apiKeyID int64, filter LuminaTaskFilter) ([]*LuminaTask, int, error)
	// ListActiveLuminaTasks returns non-terminal tasks across all owners for the
	// background reconciler, oldest update first.
	ListActiveLuminaTasks(ctx context.Context, limit int) ([]*LuminaTask, error)
	// UpdateLuminaTask applies a non-terminal refresh. It never overwrites a
	// task that already reached a terminal status (a stale refresh racing the
	// guarded transition would otherwise resurrect it and re-trigger terminal
	// side effects such as billing); in that case the stored terminal row is
	// returned unchanged.
	UpdateLuminaTask(ctx context.Context, taskID string, params UpdateLuminaTaskParams) (*LuminaTask, error)
	// TransitionLuminaTaskToTerminal applies a terminal status transition only
	// when the stored status is still non-terminal. applied is false when
	// another path already finalized the task; callers must treat side effects
	// such as billing as conditional on applied.
	TransitionLuminaTaskToTerminal(ctx context.Context, taskID string, params UpdateLuminaTaskParams) (task *LuminaTask, applied bool, err error)
	MarkLuminaTaskDeleted(ctx context.Context, userID, apiKeyID int64, taskID string, deletedAt time.Time) error
}

func NewLuminaTaskID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "cgt-" + hex.EncodeToString(value[:]), nil
}

// terminalLuminaTaskStatuses is the single authoritative list of terminal
// statuses; IsTerminalLuminaTaskStatus and persistence guards derive from it.
var terminalLuminaTaskStatuses = []string{
	LuminaTaskStatusSucceeded,
	LuminaTaskStatusFailed,
	LuminaTaskStatusCancelled,
	LuminaTaskStatusExpired,
}

func IsTerminalLuminaTaskStatus(status string) bool {
	for _, terminal := range terminalLuminaTaskStatuses {
		if status == terminal {
			return true
		}
	}
	return false
}

// TerminalLuminaTaskStatuses lists every terminal status for persistence queries.
func TerminalLuminaTaskStatuses() []string {
	result := make([]string, len(terminalLuminaTaskStatuses))
	copy(result, terminalLuminaTaskStatuses)
	return result
}
