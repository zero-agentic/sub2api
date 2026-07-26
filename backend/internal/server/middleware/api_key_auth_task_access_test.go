package middleware

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsModelArkTaskAccess(t *testing.T) {
	const root = "/api/v3/contents/generations/tasks"

	tests := []struct {
		name   string
		method string
		path   string
		want   bool
	}{
		// GET on the list root must skip billing so an exhausted key can still
		// retrieve its existing tasks.
		{name: "GET list root", method: http.MethodGet, path: root, want: true},
		// GET on a specific task ID (sub-path).
		{name: "GET task by ID", method: http.MethodGet, path: root + "/task_abc123", want: true},
		// DELETE on a specific task must also skip billing (cancellation should
		// remain available even after the key's balance is exhausted).
		{name: "DELETE task by ID", method: http.MethodDelete, path: root + "/task_xyz", want: true},
		// DELETE on the root itself.
		{name: "DELETE list root", method: http.MethodDelete, path: root, want: true},
		// POST (task creation) must go through the full billing gate.
		{name: "POST create task", method: http.MethodPost, path: root, want: false},
		// POST sub-path also stays gated.
		{name: "POST task subpath", method: http.MethodPost, path: root + "/task_123", want: false},
		// Unrelated path must not match.
		{name: "GET unrelated path", method: http.MethodGet, path: "/api/v3/other", want: false},
		// A path that starts with the root string but is not a sub-path
		// (i.e. the root is not followed by "/") must not match — guards
		// against a prefix-fakeout like /api/v3/contents/generations/tasksXYZ.
		{name: "GET prefix fakeout", method: http.MethodGet, path: root + "XYZ", want: false},
		// Empty path.
		{name: "GET empty path", method: http.MethodGet, path: "", want: false},
		// PUT is not an allowed method.
		{name: "PUT task", method: http.MethodPut, path: root + "/task_1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isModelArkTaskAccess(tt.method, tt.path)
			require.Equal(t, tt.want, got)
		})
	}
}
