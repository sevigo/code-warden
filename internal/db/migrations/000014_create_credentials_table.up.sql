CREATE TABLE IF NOT EXISTS credentials (
    id          VARCHAR(100) PRIMARY KEY,
    data        BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION update_credentials_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER credentials_updated_at
    BEFORE UPDATE ON credentials
    FOR EACH ROW
    EXECUTE FUNCTION update_credentials_updated_at();
