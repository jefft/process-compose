package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/f1bonacc1/process-compose/src/types"
	"github.com/gin-gonic/gin"
)

func TestGetHookOutputEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var receivedCursor uint64 = 999
	mock := &mockProject{getHookOutputFn: func(after uint64) (*types.HookOutputSnapshot, error) {
		receivedCursor = after
		return &types.HookOutputSnapshot{
			Lines: []types.HookOutputLine{
				{Sequence: 8, Process: "web", Event: "ready", Message: "listening on 8080"},
				{Sequence: 9, Event: "ready", Message: "project ready"},
			},
			NextSequence:    9,
			Pending:         1,
			StartupComplete: false,
		}, nil
	}}
	router := InitRoutes(false, NewPcApi(mock))

	t.Run("returns snapshot with cursor", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/project/hooks?after=7", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		if receivedCursor != 7 {
			t.Fatalf("cursor = %d, want 7", receivedCursor)
		}
		var snapshot types.HookOutputSnapshot
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Lines) != 2 || snapshot.Lines[0].Process != "web" || snapshot.Lines[1].Process != "" {
			t.Fatalf("unexpected lines: %+v", snapshot.Lines)
		}
		if snapshot.NextSequence != 9 || snapshot.Pending != 1 || snapshot.StartupComplete {
			t.Fatalf("unexpected accounting: %+v", snapshot)
		}
	})

	t.Run("defaults cursor to zero", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/project/hooks", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
		}
		if receivedCursor != 0 {
			t.Fatalf("cursor = %d, want 0", receivedCursor)
		}
	})

	t.Run("rejects invalid cursor", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "/project/hooks?after=not-a-number", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}
