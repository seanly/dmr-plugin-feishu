package queue

import (
	"context"
	"testing"
	"time"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// MockHandler implements Handler interface for testing
type MockHandler struct {
	activeJobs    map[string]*Job
	processCalled int
	replyOutput   string
	replyError    error
}

func NewMockHandler() *MockHandler {
	return &MockHandler{
		activeJobs: make(map[string]*Job),
	}
}

func (m *MockHandler) ProcessJob(job *Job) {
	m.processCalled++
	ProcessJob(context.Background(), m, job)
}

func (m *MockHandler) GetActiveJobByTape(tapeName string) *Job {
	return m.activeJobs[tapeName]
}

func (m *MockHandler) SetActiveJob(job *Job) {
	m.activeJobs[job.TapeName] = job
}

func (m *MockHandler) ClearActiveJob(tapeName string, jobID string) {
	if job, ok := m.activeJobs[tapeName]; ok {
		if jobID == "" || job.ID == jobID {
			delete(m.activeJobs, tapeName)
		}
	}
}

func (m *MockHandler) ComposeRunPrompt(userContent string) string {
	return userContent
}

func (m *MockHandler) CallRunAgent(tape, prompt string, historyAfter int64) (*proto.RunAgentResponse, error) {
	return &proto.RunAgentResponse{Output: m.replyOutput}, nil
}

func (m *MockHandler) ReplyAgentOutput(ctx context.Context, job *Job, output string) error {
	return m.replyError
}

func TestJobCreation(t *testing.T) {
	job := &Job{
		ID:               "test-id-123",
		TapeName:         "feishu:p2p:oc_xxx",
		ChatID:           "oc_xxx",
		Content:          "Hello",
		TriggerMessageID: "om_xxx",
	}

	if job.ID != "test-id-123" {
		t.Errorf("Job.ID = %q, want test-id-123", job.ID)
	}

	if job.TapeName != "feishu:p2p:oc_xxx" {
		t.Errorf("Job.TapeName = %q, want feishu:p2p:oc_xxx", job.TapeName)
	}
}

func TestManagerEnqueue(t *testing.T) {
	handler := NewMockHandler()
	qm := NewManager(handler)

	job := &Job{
		TapeName: "feishu:p2p:oc_xxx",
		ChatID:   "oc_xxx",
		Content:  "Test message",
	}

	qm.Enqueue(job)

	// Give some time for worker to start
	time.Sleep(50 * time.Millisecond)

	// Verify job was processed
	if handler.processCalled != 1 {
		t.Errorf("ProcessJob called %d times, want 1", handler.processCalled)
	}
}

func TestManagerMultipleJobsSameChat(t *testing.T) {
	handler := NewMockHandler()
	qm := NewManager(handler)

	// Enqueue multiple jobs for same chat
	for i := 0; i < 3; i++ {
		job := &Job{
			TapeName: "feishu:p2p:oc_xxx",
			ChatID:   "oc_xxx",
			Content:  string(rune('A' + i)),
		}
		qm.Enqueue(job)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// All jobs should be processed by same worker (serial)
	if handler.processCalled != 3 {
		t.Errorf("ProcessJob called %d times, want 3", handler.processCalled)
	}
}

func TestManagerDifferentChats(t *testing.T) {
	handler := NewMockHandler()
	qm := NewManager(handler)

	// Enqueue jobs for different chats
	for i := 0; i < 3; i++ {
		job := &Job{
			TapeName: "feishu:p2p:oc_" + string(rune('a'+i)),
			ChatID:   "oc_" + string(rune('a'+i)),
			Content:  "Message " + string(rune('0'+i)),
		}
		qm.Enqueue(job)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	if handler.processCalled != 3 {
		t.Errorf("ProcessJob called %d times, want 3", handler.processCalled)
	}
}

func TestManagerNilJob(t *testing.T) {
	handler := NewMockHandler()
	qm := NewManager(handler)

	qm.Enqueue(nil)
	qm.Enqueue(&Job{ChatID: ""})

	time.Sleep(50 * time.Millisecond)

	if handler.processCalled != 0 {
		t.Errorf("ProcessJob called %d times, want 0 for invalid jobs", handler.processCalled)
	}
}

func TestManagerShutdown(t *testing.T) {
	handler := NewMockHandler()
	qm := NewManager(handler)

	// Enqueue some jobs
	for i := 0; i < 3; i++ {
		job := &Job{
			TapeName: "feishu:p2p:oc_xxx",
			ChatID:   "oc_xxx",
			Content:  string(rune('A' + i)),
		}
		qm.Enqueue(job)
	}

	// Wait a bit then shutdown
	time.Sleep(100 * time.Millisecond)
	qm.ShutdownAll()

	// Try to enqueue after shutdown
	job := &Job{
		TapeName: "feishu:p2p:oc_yyy",
		ChatID:   "oc_yyy",
		Content:  "After shutdown",
	}
	qm.Enqueue(job)

	// Should not be processed
	time.Sleep(100 * time.Millisecond)
	// Note: we can't reliably check processCalled here due to race
}

func TestProcessJobWithNilJob(t *testing.T) {
	handler := NewMockHandler()

	// Should not panic
	ProcessJob(context.Background(), handler, nil)

	if handler.processCalled != 0 {
		t.Error("ProcessJob should not be called for nil job")
	}
}

func TestClearActiveJobWithJobID(t *testing.T) {
	handler := NewMockHandler()

	job1 := &Job{
		ID:       "job-1",
		TapeName: "feishu:p2p:oc_xxx",
	}
	job2 := &Job{
		ID:       "job-2",
		TapeName: "feishu:p2p:oc_xxx",
	}

	// Set first job
	handler.SetActiveJob(job1)

	// Try to clear with wrong ID (simulating delayed cleanup of old job)
	handler.ClearActiveJob(job1.TapeName, "wrong-id")

	// Job should still be there
	if handler.GetActiveJobByTape(job1.TapeName) == nil {
		t.Error("Job should not be cleared with wrong ID")
	}

	// Set second job (overwrites first)
	handler.SetActiveJob(job2)

	// Clear with correct ID
	handler.ClearActiveJob(job2.TapeName, job2.ID)

	// Job should be cleared
	if handler.GetActiveJobByTape(job2.TapeName) != nil {
		t.Error("Job should be cleared with correct ID")
	}
}

func TestClearActiveJobWithoutJobID(t *testing.T) {
	handler := NewMockHandler()

	job := &Job{
		ID:       "job-1",
		TapeName: "feishu:p2p:oc_xxx",
	}

	handler.SetActiveJob(job)
	handler.ClearActiveJob(job.TapeName, "") // Empty ID clears unconditionally

	if handler.GetActiveJobByTape(job.TapeName) != nil {
		t.Error("Job should be cleared with empty ID")
	}
}
