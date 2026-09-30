package logsafe

import (
	"encoding/json"
	"minimax-h3-tc/internal/domain"
	"strings"
	"testing"
)

func TestGenerationDiagnosticPreservesParametersAndRedactsMedia(t *testing.T) {
	r := domain.GenerationRequest{Model: "seed-model", Duration: 10, DurationPresent: true, Content: []domain.GenerationContent{{Type: "text", Text: "private prompt"}, {Type: "image_url", Role: "reference_image", ImageURL: &domain.MediaURL{URL: "data:image/png;base64,cHJpdmF0ZS1iaW5hcnk="}}}}
	data, _ := json.Marshal(Generation(r))
	if !strings.Contains(string(data), `"duration":10`) || !strings.Contains(string(data), `"role":"reference_image"`) {
		t.Fatalf("parameters missing: %s", data)
	}
	if strings.Contains(string(data), "private prompt") || strings.Contains(string(data), "cHJpdmF0ZS1iaW5hcnk=") {
		t.Fatalf("sensitive media in log: %s", data)
	}
}
func TestDiagnosticJSONKeepsIDAndNumbersWithoutSecrets(t *testing.T) {
	value := DiagnosticJSON([]byte(`{"id":"upstream-id","duration":10,"large":7689050665677488180,"token":"secret-token","content":{"video_url":"https://video.example/file?signature=signed-secret"},"message":"Bearer private-key https://private.example/key","text":"private prompt"}`), "private-key")
	data, _ := json.Marshal(value)
	for _, secret := range []string{"secret-token", "signed-secret", "private-key", "private.example", "private prompt"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("secret %s leaked: %s", secret, data)
		}
	}
	if !strings.Contains(string(data), `"id":"upstream-id"`) || !strings.Contains(string(data), "7689050665677488180") {
		t.Fatalf("identity lost: %s", data)
	}
}
func TestDiagnosticJSONOmitsMalformedAndBoundsLargePayloads(t *testing.T) {
	for _, payload := range [][]byte{[]byte("not-json secret"), []byte(`{"value":"` + strings.Repeat("x", 100000) + `"}`)} {
		data, _ := json.Marshal(DiagnosticJSON(payload))
		if len(data) > 32768 || strings.Contains(string(data), "not-json secret") {
			t.Fatalf("unbounded/unsafe summary: length=%d", len(data))
		}
	}
}

func TestDiagnosticFreeTextCredentialsAreRedacted(t *testing.T) {
	for _, value := range []string{"Bearer unknown-provider-secret", "Cookie: session-value", "token=third-party-value"} {
		body, _ := json.Marshal(map[string]string{"message": value})
		encoded, _ := json.Marshal(DiagnosticJSON(body))
		if strings.Contains(string(encoded), strings.Split(value, " ")[len(strings.Split(value, " "))-1]) {
			t.Fatalf("credential text leaked: %s", encoded)
		}
	}
}

func TestDiagnosticValidationErrorsCannotEchoInputs(t *testing.T) {
	value := DiagnosticJSON([]byte(`{"detail":[{"input":"private prompt","msg":"rejected private prompt","type":"value_error"}],"image_base64":"cHJpdmF0ZQ==","message":"echo private prompt","unexpected":["private prompt"],"status":"failed","code":"invalid_duration"}`))
	data, _ := json.Marshal(value)
	for _, secret := range []string{"private prompt", "cHJpdmF0ZQ=="} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("upstream input echoed in diagnostics: %s", data)
		}
	}
	if !strings.Contains(string(data), `"status":"failed"`) || !strings.Contains(string(data), `"code":"invalid_duration"`) {
		t.Fatalf("diagnostic status/code lost: %s", data)
	}
}
