package tk2sd

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestSubmitLogsBodyResponseDurationAndIDWithoutCredentials(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, `{"id":"`+taskID+`","token":"test-secret"}`)
	})
	var output bytes.Buffer
	c.logger = slog.New(slog.NewJSONHandler(&output, nil))
	body := []byte(`{"model":"doubao-seedance-2-0-mini-260615","duration":10,"content":[{"type":"text","text":"private prompt"},{"type":"image_url","role":"reference_image","image_url":{"url":"asset://` + assetID + `"}}]}`)
	id, err := c.Submit(context.Background(), body, "private-idempotency-key")
	if err != nil || id != taskID {
		t.Fatal(id, err)
	}
	log := output.String()
	if !strings.Contains(log, `"duration":10`) || !strings.Contains(log, taskID) || !strings.Contains(log, `"event":"response"`) {
		t.Fatalf("missing diagnostics: %s", log)
	}
	for _, secret := range []string{"test-secret", "private-idempotency-key", "private prompt", assetID} {
		if strings.Contains(log, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
}
func TestMissingUpstreamIDHasDiagnosticAndRemainsUnknown(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, `{"accepted":true}`) })
	var output bytes.Buffer
	c.logger = slog.New(slog.NewJSONHandler(&output, nil))
	if _, err := c.Submit(context.Background(), []byte(submitBody), "key"); err == nil {
		t.Fatal("missing id accepted")
	}
	if !strings.Contains(output.String(), `"accepted":true`) || !strings.Contains(output.String(), "missing_task_id") {
		t.Fatalf("missing response-shape diagnostic: %s", output.String())
	}
}

func TestRejectedResponseLogsStructureWithoutEchoedInputs(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		jsonReply(w, `{"detail":[{"type":"value_error","input":"private prompt","msg":"invalid private prompt"}],"image_base64":"cHJpdmF0ZQ=="}`)
	})
	var output bytes.Buffer
	c.logger = slog.New(slog.NewJSONHandler(&output, nil))
	if _, err := c.Submit(context.Background(), []byte(submitBody), "key"); err == nil {
		t.Fatal("rejection accepted")
	}
	log := output.String()
	if !strings.Contains(log, `"status_code":422`) || !strings.Contains(log, `"type":"value_error"`) {
		t.Fatalf("missing rejection diagnostic: %s", log)
	}
	for _, secret := range []string{"private prompt", "cHJpdmF0ZQ=="} {
		if strings.Contains(log, secret) {
			t.Fatalf("upstream input echoed in log: %s", log)
		}
	}
}
