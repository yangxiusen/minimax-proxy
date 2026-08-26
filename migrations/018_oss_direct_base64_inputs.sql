ALTER TABLE object_storage_configs
ADD COLUMN upload_base64_inputs INTEGER NOT NULL DEFAULT 0
CHECK (upload_base64_inputs IN (0,1));
