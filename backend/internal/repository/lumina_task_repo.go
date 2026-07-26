package repository

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/luminatask"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type luminaTaskRepository struct {
	client *dbent.Client
}

func NewLuminaTaskRepository(client *dbent.Client) service.LuminaTaskRepository {
	return &luminaTaskRepository{client: client}
}

func (r *luminaTaskRepository) CreateLuminaTask(ctx context.Context, params service.CreateLuminaTaskParams) (*service.LuminaTask, error) {
	if params.TaskID == "" {
		taskID, err := service.NewLuminaTaskID()
		if err != nil {
			return nil, err
		}
		params.TaskID = taskID
	}
	if params.Status == "" {
		params.Status = service.LuminaTaskStatusQueued
	}
	created, err := r.client.LuminaTask.Create().
		SetTaskID(params.TaskID).
		SetUserID(params.UserID).
		SetAPIKeyID(params.APIKeyID).
		SetGroupID(params.GroupID).
		SetAccountID(params.AccountID).
		SetUpstreamTaskID(params.UpstreamTaskID).
		SetTaskType(params.TaskType).
		SetModel(params.Model).
		SetStatus(params.Status).
		SetRequestPayload(cloneLuminaPayload(params.RequestPayload)).
		SetResponsePayload(cloneLuminaPayload(params.ResponsePayload)).
		Save(ctx)
	if err != nil {
		if dbent.IsConstraintError(err) {
			return nil, service.ErrLuminaTaskExists
		}
		return nil, err
	}
	return luminaTaskFromEnt(created), nil
}

func (r *luminaTaskRepository) GetLuminaTaskForOwner(ctx context.Context, userID, apiKeyID int64, taskID string) (*service.LuminaTask, error) {
	task, err := r.client.LuminaTask.Query().Where(
		luminatask.TaskID(taskID),
		luminatask.UserID(userID),
		luminatask.APIKeyID(apiKeyID),
		luminatask.UserDeletedAtIsNil(),
	).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrLuminaTaskNotFound
		}
		return nil, err
	}
	return luminaTaskFromEnt(task), nil
}

func (r *luminaTaskRepository) ListLuminaTasksForOwner(ctx context.Context, userID, apiKeyID int64, filter service.LuminaTaskFilter) ([]*service.LuminaTask, int, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 20
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	query := r.client.LuminaTask.Query().Where(
		luminatask.UserID(userID),
		luminatask.APIKeyID(apiKeyID),
		luminatask.UserDeletedAtIsNil(),
	)
	if filter.Status != "" {
		query.Where(luminatask.Status(filter.Status))
	}
	if filter.Model != "" {
		query.Where(luminatask.Model(filter.Model))
	}
	if len(filter.TaskIDs) > 0 {
		query.Where(luminatask.TaskIDIn(filter.TaskIDs...))
	}
	if !filter.CreatedAfter.IsZero() {
		query.Where(luminatask.CreatedAtGTE(filter.CreatedAfter))
	}
	if !filter.CreatedBefore.IsZero() {
		query.Where(luminatask.CreatedAtLT(filter.CreatedBefore))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	tasks, err := query.
		Order(luminatask.ByCreatedAt(sql.OrderDesc()), luminatask.ByID(sql.OrderDesc())).
		Limit(limit).
		Offset(filter.Offset).
		All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]*service.LuminaTask, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, luminaTaskFromEnt(task))
	}
	return result, total, nil
}

func (r *luminaTaskRepository) ListActiveLuminaTasks(ctx context.Context, limit int) ([]*service.LuminaTask, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	tasks, err := r.client.LuminaTask.Query().Where(
		luminatask.StatusNotIn(service.TerminalLuminaTaskStatuses()...),
		luminatask.UserDeletedAtIsNil(),
	).
		Order(luminatask.ByUpdatedAt(sql.OrderAsc()), luminatask.ByID(sql.OrderAsc())).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*service.LuminaTask, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, luminaTaskFromEnt(task))
	}
	return result, nil
}

func (r *luminaTaskRepository) UpdateLuminaTask(ctx context.Context, taskID string, params service.UpdateLuminaTaskParams) (*service.LuminaTask, error) {
	// Guard against resurrecting a finalized task: a stale refresh (e.g. the
	// reconciler racing a user poll that already applied the terminal
	// transition and billed) must never move a terminal status back to a
	// non-terminal one, or the guarded transition could fire a second time.
	update := applyLuminaTaskUpdate(r.client.LuminaTask.Update().Where(
		luminatask.TaskID(taskID),
		luminatask.StatusNotIn(service.TerminalLuminaTaskStatuses()...),
	), params)
	if _, err := update.Save(ctx); err != nil {
		return nil, err
	}
	// Zero affected rows means the task is missing or was concurrently
	// finalized; the stored row (when present) is the authoritative state.
	return r.getLuminaTaskByTaskID(ctx, taskID)
}

func (r *luminaTaskRepository) TransitionLuminaTaskToTerminal(ctx context.Context, taskID string, params service.UpdateLuminaTaskParams) (*service.LuminaTask, bool, error) {
	if !service.IsTerminalLuminaTaskStatus(params.Status) {
		return nil, false, fmt.Errorf("lumina task transition target %q is not terminal", params.Status)
	}
	update := applyLuminaTaskUpdate(r.client.LuminaTask.Update().Where(
		luminatask.TaskID(taskID),
		luminatask.StatusNotIn(service.TerminalLuminaTaskStatuses()...),
	), params)
	affected, err := update.Save(ctx)
	if err != nil {
		return nil, false, err
	}
	task, getErr := r.getLuminaTaskByTaskID(ctx, taskID)
	if getErr != nil {
		return nil, false, getErr
	}
	return task, affected > 0, nil
}

func (r *luminaTaskRepository) getLuminaTaskByTaskID(ctx context.Context, taskID string) (*service.LuminaTask, error) {
	task, err := r.client.LuminaTask.Query().Where(luminatask.TaskID(taskID)).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrLuminaTaskNotFound
		}
		return nil, err
	}
	return luminaTaskFromEnt(task), nil
}

func applyLuminaTaskUpdate(update *dbent.LuminaTaskUpdate, params service.UpdateLuminaTaskParams) *dbent.LuminaTaskUpdate {
	update.SetUpdatedAt(time.Now())
	if params.Status != "" {
		update.SetStatus(params.Status)
	}
	if params.ResponsePayload != nil {
		update.SetResponsePayload(cloneLuminaPayload(params.ResponsePayload))
	}
	if params.ErrorCode != nil {
		update.SetErrorCode(*params.ErrorCode)
	} else {
		update.ClearErrorCode()
	}
	if params.ErrorMessage != nil {
		update.SetErrorMessage(*params.ErrorMessage)
	} else {
		update.ClearErrorMessage()
	}
	if params.CompletedAt != nil {
		update.SetCompletedAt(*params.CompletedAt)
	}
	return update
}

func (r *luminaTaskRepository) MarkLuminaTaskDeleted(ctx context.Context, userID, apiKeyID int64, taskID string, deletedAt time.Time) error {
	affected, err := r.client.LuminaTask.Update().Where(
		luminatask.TaskID(taskID),
		luminatask.UserID(userID),
		luminatask.APIKeyID(apiKeyID),
		luminatask.UserDeletedAtIsNil(),
	).SetUserDeletedAt(deletedAt).SetUpdatedAt(deletedAt).Save(ctx)
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrLuminaTaskNotFound
	}
	return nil
}

func luminaTaskFromEnt(task *dbent.LuminaTask) *service.LuminaTask {
	if task == nil {
		return nil
	}
	return &service.LuminaTask{
		ID:              task.ID,
		TaskID:          task.TaskID,
		UserID:          task.UserID,
		APIKeyID:        task.APIKeyID,
		GroupID:         task.GroupID,
		AccountID:       task.AccountID,
		UpstreamTaskID:  task.UpstreamTaskID,
		TaskType:        task.TaskType,
		Model:           task.Model,
		Status:          task.Status,
		RequestPayload:  cloneLuminaPayload(task.RequestPayload),
		ResponsePayload: cloneLuminaPayload(task.ResponsePayload),
		ErrorCode:       task.ErrorCode,
		ErrorMessage:    task.ErrorMessage,
		CreatedAt:       task.CreatedAt,
		UpdatedAt:       task.UpdatedAt,
		CompletedAt:     task.CompletedAt,
		UserDeletedAt:   task.UserDeletedAt,
	}
}

func cloneLuminaPayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

var _ service.LuminaTaskRepository = (*luminaTaskRepository)(nil)
