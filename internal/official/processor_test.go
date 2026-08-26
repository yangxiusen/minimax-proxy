package official

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/upstream/minimaxv2"
)

func TestProcessorSubmitsPollsAndCreatesDeliveryJob(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{}`, DeliveryRequired: true}}
	client := &clientFake{submitID: "official-1", queries: []minimaxv2.Task{
		{ID: "official-1", Status: minimaxv2.StatusRunning},
		{ID: "official-1", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}, Ratio: "16:9"},
	}}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 7, Capacity: 3, PollInterval: time.Millisecond, Now: func() time.Time { return time.Date(2033, 5, 18, 1, 2, 3, 0, time.UTC) }}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.boundID != "official-1" || store.generatedURL != "https://origin.example/video.mp4" {
		t.Fatalf("bound=%q generated=%q", store.boundID, store.generatedURL)
	}
	if store.uploadJob == nil || store.uploadJob.ObjectKey != "MiniMax-H3/2033-05-18/proxy-1.mp4" {
		t.Fatalf("upload job=%+v", store.uploadJob)
	}
}

func TestProcessorSubmitsFreshTaskWithPersistedEmptyBaseline(t *testing.T) {
	events := []string{}
	store := &storeFake{claimed: domain.Task{
		TaskID: "proxy-fresh", RequestJSON: `{}`, UpstreamJobsBeforeJSON: `[]`,
	}, events: &events}
	client := &clientFake{
		submitID: "official-fresh",
		queries:  []minimaxv2.Task{{ID: "official-fresh", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}}},
		events:   &events,
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1, PollInterval: time.Millisecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 1 || store.boundID != "official-fresh" || store.failedCode != "" {
		t.Fatalf("submit=%d bound=%q failed=%q", client.submitCalls, store.boundID, store.failedCode)
	}
	if want := []string{"list", "save", "submit"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("submission events=%v want=%v", events, want)
	}
}

func TestProcessorReconcilesSavedEmptyBaselineWithoutSubmittingAgain(t *testing.T) {
	store := &storeFake{claimed: domain.Task{
		TaskID: "proxy-recovery", UpstreamJobsBeforeJSON: `[]`, OfficialSubmissionBaselineSaved: true,
		Resolution: "2K", Duration: 5,
	}}
	client := &clientFake{
		lists:   [][]minimaxv2.Task{{{ID: "official-recovered", Resolution: "2K", Duration: 5}}},
		queries: []minimaxv2.Task{{ID: "official-recovered", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}}},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", PollInterval: time.Millisecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 0 || store.boundID != "official-recovered" || store.failedCode != "" {
		t.Fatalf("submit=%d bound=%q failed=%q", client.submitCalls, store.boundID, store.failedCode)
	}
}

func TestProcessorFailsSavedEmptyBaselineWithoutSubmittingWhenNoCandidateAppears(t *testing.T) {
	store := &storeFake{claimed: domain.Task{
		TaskID: "proxy-recovery-missing", UpstreamJobsBeforeJSON: `[]`, OfficialSubmissionBaselineSaved: true,
		Resolution: "2K", Duration: 5,
	}}
	client := &clientFake{}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", PollInterval: time.Millisecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 0 || store.failedCode != "official_submit_uncertain" {
		t.Fatalf("submit=%d failed=%q", client.submitCalls, store.failedCode)
	}
}

func TestProcessorResumesBoundTaskWithoutSubmittingAgain(t *testing.T) {
	store := &storeFake{}
	client := &clientFake{queries: []minimaxv2.Task{{ID: "official-existing", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}}}}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", PollInterval: time.Millisecond}
	if err := processor.ProcessTask(context.Background(), domain.Task{TaskID: "proxy-1", UpstreamID: "node-1", UpstreamJobID: "official-existing"}); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 0 {
		t.Fatalf("submit calls=%d", client.submitCalls)
	}
}

func TestProcessorDoesNotFailUpstreamTaskWhenRuntimeStops(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{}`}}
	client := &clientFake{submitID: "official-1", queryErr: context.Canceled}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1, PollInterval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := processor.ProcessOne(ctx)
	if !errors.Is(err, context.Canceled) || store.failedCode != "" {
		t.Fatalf("err=%v failed=%q", err, store.failedCode)
	}
}

func TestProcessorReconcilesUncertainSubmissionWithoutCreatingAgain(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{}`, Resolution: "2K", Duration: 5, RatioRequested: "16:9"}}
	client := &clientFake{
		submitErr: errors.New("connection reset after write"),
		lists:     [][]minimaxv2.Task{{}, {{ID: "official-reconciled", Resolution: "2K", Duration: 5, Ratio: "16:9"}}},
		queries:   []minimaxv2.Task{{ID: "official-reconciled", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}}},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 1 || store.boundID != "official-reconciled" || store.failedCode != "" {
		t.Fatalf("submit=%d bound=%q failed=%q", client.submitCalls, store.boundID, store.failedCode)
	}
}

func TestProcessorRetriesReconciliationWhenCreatedTaskIsBrieflyInvisible(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{}`, Resolution: "2K", Duration: 5}}
	client := &clientFake{
		submitErr: errors.New("connection reset after write"),
		lists: [][]minimaxv2.Task{
			{},
			{},
			{{ID: "official-delayed", Resolution: "2K", Duration: 5}},
		},
		queries: []minimaxv2.Task{{ID: "official-delayed", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}}},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1, PollInterval: time.Millisecond}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 1 || store.boundID != "official-delayed" {
		t.Fatalf("submit=%d bound=%q", client.submitCalls, store.boundID)
	}
}

func TestProcessorStoresStableMessageInsteadOfNetworkError(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{}`}}
	client := &clientFake{lists: [][]minimaxv2.Task{nil}, submitErr: errors.New("request https://secret.internal.example failed")}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1, PollInterval: time.Millisecond}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "official_submit_failed" || store.failedMessage != "官方任务提交失败" {
		t.Fatalf("failure=%q %q", store.failedCode, store.failedMessage)
	}
}

func TestProcessorPersistsStructuredOfficialSubmitFeedback(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-feedback", RequestJSON: `{}`}}
	client := &clientFake{
		submitErr: &minimaxv2.HTTPError{
			StatusCode: 422, Code: "1027", Type: "unprocessable_entity_error",
			Message: "text content contains sensitive content (1027)", ResourceType: "text", RequestID: "req-sensitive",
		},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1, PollInterval: time.Millisecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	feedback := store.failedFeedback
	if store.failedCode != "official_submit_failed" || feedback == nil || feedback.HTTPStatus != 422 ||
		feedback.Code != "1027" || feedback.Type != "unprocessable_entity_error" ||
		feedback.Message != "text content contains sensitive content (1027)" || feedback.ResourceType != "text" ||
		feedback.RequestID != "req-sensitive" {
		t.Fatalf("failure=%q feedback=%+v", store.failedCode, feedback)
	}
}

func TestProcessorPreservesSubmitFeedbackWhenReconciliationFails(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-feedback-reconcile", RequestJSON: `{}`}}
	client := &clientFake{
		submitErr: &minimaxv2.HTTPError{
			StatusCode: 422, Code: "1027", Type: "unprocessable_entity_error",
			Message: "text content contains sensitive content (1027)", ResourceType: "text", RequestID: "req-sensitive",
		},
		lists:    [][]minimaxv2.Task{{}},
		listErrs: []error{nil, errors.New("reconciliation unavailable")},
	}
	processor := Processor{Store: store, Client: client, NodeID: "node-1", NodeVersion: 1, Capacity: 1, PollInterval: time.Millisecond}

	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.failedCode != "official_reconcile_failed" || store.failedMessage != "官方任务提交结果对账失败" || store.failedFeedback == nil || store.failedFeedback.Code != "1027" {
		t.Fatalf("failure=%q feedback=%+v", store.failedCode, store.failedFeedback)
	}
}

func TestProcessorSubmitsMaterializedRequest(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{"content":[]}`}}
	client := &clientFake{submitID: "official-1", queries: []minimaxv2.Task{{ID: "official-1", Status: minimaxv2.StatusSucceeded, Content: minimaxv2.Content{URL: "https://origin.example/video.mp4"}}}}
	processor := Processor{Store: store, Client: client, Inputs: restoreFake{result: []byte(`{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`)}, NodeID: "node-1", NodeVersion: 1, Capacity: 1}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(client.submitBody) != `{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}` {
		t.Fatalf("submit body=%s", client.submitBody)
	}
}

func TestProcessorFailsWithoutSubmittingWhenMaterializationFails(t *testing.T) {
	store := &storeFake{claimed: domain.Task{TaskID: "proxy-1", RequestJSON: `{"content":[]}`}}
	client := &clientFake{}
	processor := Processor{Store: store, Client: client, Inputs: restoreFake{err: errors.New("sensitive path")}, NodeID: "node-1", NodeVersion: 1, Capacity: 1}
	if err := processor.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.submitCalls != 0 || store.failedCode != "official_input_materialization_failed" || store.failedMessage != "官方任务输入素材还原失败" {
		t.Fatalf("submit=%d failure=%q %q", client.submitCalls, store.failedCode, store.failedMessage)
	}
}

type restoreFake struct {
	result []byte
	err    error
}

func (r restoreFake) Restore(context.Context, string, []byte) ([]byte, error) {
	return r.result, r.err
}

type storeFake struct {
	claimed        domain.Task
	boundID        string
	generatedURL   string
	uploadJob      *domain.ResultUploadJob
	failedCode     string
	failedMessage  string
	failedFeedback *domain.UpstreamFeedback
	events         *[]string
}

func (s *storeFake) SaveOfficialSubmissionBaseline(context.Context, string, string, []string) error {
	if s.events != nil {
		*s.events = append(*s.events, "save")
	}
	return nil
}

func (s *storeFake) ClaimNextOfficial(context.Context, string, int64, int) (domain.Task, error) {
	return s.claimed, nil
}
func (s *storeFake) BindOfficialTask(_ context.Context, _, _, upstreamID string) error {
	s.boundID = upstreamID
	return nil
}
func (s *storeFake) MarkOfficialGenerated(_ context.Context, _, _, resultURL, _ string, job *domain.ResultUploadJob) error {
	s.generatedURL, s.uploadJob = resultURL, job
	return nil
}
func (s *storeFake) MarkOfficialFailed(_ context.Context, _, _, code, message string, feedback *domain.UpstreamFeedback) error {
	s.failedCode, s.failedMessage = code, message
	s.failedFeedback = feedback
	return nil
}

type clientFake struct {
	submitID    string
	submitErr   error
	submitCalls int
	queries     []minimaxv2.Task
	queryErr    error
	lists       [][]minimaxv2.Task
	listErrs    []error
	submitBody  []byte
	events      *[]string
}

func (c *clientFake) Submit(_ context.Context, body []byte) (string, error) {
	if c.events != nil {
		*c.events = append(*c.events, "submit")
	}
	c.submitCalls++
	c.submitBody = append([]byte(nil), body...)
	return c.submitID, c.submitErr
}
func (c *clientFake) Query(context.Context, string) (minimaxv2.Task, error) {
	if c.queryErr != nil {
		return minimaxv2.Task{}, c.queryErr
	}
	result := c.queries[0]
	c.queries = c.queries[1:]
	return result, nil
}
func (c *clientFake) List(context.Context) ([]minimaxv2.Task, error) {
	if c.events != nil {
		*c.events = append(*c.events, "list")
	}
	if len(c.listErrs) > 0 {
		err := c.listErrs[0]
		c.listErrs = c.listErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(c.lists) == 0 {
		return []minimaxv2.Task{}, nil
	}
	result := c.lists[0]
	c.lists = c.lists[1:]
	return result, nil
}
