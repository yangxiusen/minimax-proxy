ALTER TABLE callback_deliveries ADD COLUMN request_body BLOB NOT NULL DEFAULT X'';
ALTER TABLE callback_deliveries ADD COLUMN lease_expires_at INTEGER;

CREATE INDEX idx_callback_deliveries_lease
    ON callback_deliveries(status,next_attempt_at,lease_expires_at,created_at);
