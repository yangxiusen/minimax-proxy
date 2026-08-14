package sqlite

import (
	"context"
	"testing"
)

func insertNodeAPINode(t *testing.T, store *Store, id string) {
	t.Helper()
	_, err := store.db.ExecContext(context.Background(), `INSERT INTO model_service_nodes(
		id,service_url,protocol_version,api_key_ciphertext,api_key_nonce,api_key_fingerprint,api_key_id,
		capabilities_json,health_path,poll_interval_ms,request_timeout_ms,enabled,version,created_at,updated_at,
		base_url,jobs_base_url,public_base_url
	) VALUES(?,?, 'h3-node-v1',X'01',X'02','fingerprint','key-id','{}','/internal/v1/health',3000,30000,1,1,1,1,?,?,?)`,
		id, "https://"+id+".example", "https://"+id+".example", "https://"+id+".example", "https://"+id+".example")
	if err != nil {
		t.Fatal(err)
	}
}
