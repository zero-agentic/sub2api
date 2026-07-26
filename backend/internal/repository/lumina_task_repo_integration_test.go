//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/suite"
)

type LuminaTaskRepoSuite struct {
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	repo   service.LuminaTaskRepository
}

func (s *LuminaTaskRepoSuite) SetupTest() {
	s.ctx = context.Background()
	tx := testEntTx(s.T())
	s.client = tx.Client()
	s.repo = NewLuminaTaskRepository(s.client)
}

func TestLuminaTaskRepoSuite(t *testing.T) {
	suite.Run(t, new(LuminaTaskRepoSuite))
}

// mustCreateTask seeds the FK chain (user → group → api key → account) and one
// lumina task in the requested status.
func (s *LuminaTaskRepoSuite) mustCreateTask(status string) *service.LuminaTask {
	s.T().Helper()

	user, err := s.client.User.Create().
		SetEmail("lumina-task-" + status + "@test.com").
		SetPasswordHash("test-password-hash").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		Save(s.ctx)
	s.Require().NoError(err, "create user")

	group, err := s.client.Group.Create().
		SetName("lumina-task-group-" + status).
		SetStatus(service.StatusActive).
		Save(s.ctx)
	s.Require().NoError(err, "create group")

	apiKey, err := s.client.APIKey.Create().
		SetUserID(user.ID).
		SetKey("sk-lumina-task-" + status).
		SetName("lumina-task-key").
		SetGroupID(group.ID).
		Save(s.ctx)
	s.Require().NoError(err, "create api key")

	account, err := s.client.Account.Create().
		SetName("lumina-task-account-" + status).
		SetPlatform(service.PlatformLumina).
		SetType(service.AccountTypeCookie).
		SetStatus(service.StatusActive).
		Save(s.ctx)
	s.Require().NoError(err, "create account")

	task, err := s.repo.CreateLuminaTask(s.ctx, service.CreateLuminaTaskParams{
		UserID:         user.ID,
		APIKeyID:       apiKey.ID,
		GroupID:        group.ID,
		AccountID:      account.ID,
		UpstreamTaskID: "upstream-" + status,
		TaskType:       service.LuminaTaskTypeVideo,
		Model:          "seedance-2-0-pro",
		Status:         status,
	})
	s.Require().NoError(err, "create lumina task")
	return task
}

func (s *LuminaTaskRepoSuite) TestUpdateAppliesToNonTerminalTask() {
	task := s.mustCreateTask(service.LuminaTaskStatusQueued)

	code := "InternalServiceError"
	message := "child task failed"
	updated, err := s.repo.UpdateLuminaTask(s.ctx, task.TaskID, service.UpdateLuminaTaskParams{
		Status:       service.LuminaTaskStatusRunning,
		ErrorCode:    &code,
		ErrorMessage: &message,
	})
	s.Require().NoError(err)
	s.Require().Equal(service.LuminaTaskStatusRunning, updated.Status)
	s.Require().NotNil(updated.ErrorCode)
	s.Require().Equal(code, *updated.ErrorCode)
}

func (s *LuminaTaskRepoSuite) TestUpdateNeverResurrectsTerminalTask() {
	task := s.mustCreateTask(service.LuminaTaskStatusQueued)

	completedAt := time.Now()
	finalized, applied, err := s.repo.TransitionLuminaTaskToTerminal(s.ctx, task.TaskID, service.UpdateLuminaTaskParams{
		Status:      service.LuminaTaskStatusSucceeded,
		CompletedAt: &completedAt,
	})
	s.Require().NoError(err)
	s.Require().True(applied, "first terminal transition must apply")
	s.Require().Equal(service.LuminaTaskStatusSucceeded, finalized.Status)

	// A stale refresh racing the terminal transition must not move the task
	// back to running; the stored terminal row is returned unchanged.
	stored, err := s.repo.UpdateLuminaTask(s.ctx, task.TaskID, service.UpdateLuminaTaskParams{
		Status: service.LuminaTaskStatusRunning,
	})
	s.Require().NoError(err)
	s.Require().Equal(service.LuminaTaskStatusSucceeded, stored.Status)
	s.Require().NotNil(stored.CompletedAt)
}

func (s *LuminaTaskRepoSuite) TestTerminalTransitionAppliesExactlyOnce() {
	task := s.mustCreateTask(service.LuminaTaskStatusRunning)

	completedAt := time.Now()
	params := service.UpdateLuminaTaskParams{
		Status:      service.LuminaTaskStatusSucceeded,
		CompletedAt: &completedAt,
	}

	_, applied, err := s.repo.TransitionLuminaTaskToTerminal(s.ctx, task.TaskID, params)
	s.Require().NoError(err)
	s.Require().True(applied, "first transition must apply")

	stored, applied, err := s.repo.TransitionLuminaTaskToTerminal(s.ctx, task.TaskID, params)
	s.Require().NoError(err)
	s.Require().False(applied, "second transition must be a no-op; billing depends on this")
	s.Require().Equal(service.LuminaTaskStatusSucceeded, stored.Status)
}

func (s *LuminaTaskRepoSuite) TestUpdateMissingTaskReturnsNotFound() {
	_, err := s.repo.UpdateLuminaTask(s.ctx, "cgt-missing", service.UpdateLuminaTaskParams{
		Status: service.LuminaTaskStatusRunning,
	})
	s.Require().ErrorIs(err, service.ErrLuminaTaskNotFound)
}
