CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_keys_public_key_unique ON ssh_keys(public_key);
