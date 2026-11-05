-- Photo metadata (docs/DATABASE.md §4, T-205). Files live in the "photos" bucket under keys built
-- from the property and photo ids; owner_id and property_id are ids of core-service without
-- foreign keys (separate databases, ADR-005).
CREATE TABLE photos (
    id              uuid PRIMARY KEY,
    owner_id        uuid        NOT NULL,
    property_id     uuid        NOT NULL,
    status          text        NOT NULL CHECK (status IN ('UPLOADED', 'ATTACHED')),
    original_key    text        NOT NULL,
    preview_key     text        NOT NULL,
    thumb_key       text        NOT NULL,
    content_type    text        NOT NULL CHECK (content_type = 'image/jpeg'),
    size_bytes      bigint      NOT NULL CHECK (size_bytes > 0),
    width           int         NOT NULL CHECK (width > 0),
    height          int         NOT NULL CHECK (height > 0),
    sha256          text        NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    idempotency_key text        NULL,
    created_at      timestamptz NOT NULL,
    attached_at     timestamptz NULL,
    CHECK ((status = 'ATTACHED') = (attached_at IS NOT NULL))
);

CREATE INDEX photos_property_idx ON photos (property_id);
CREATE INDEX photos_owner_idx ON photos (owner_id);
CREATE INDEX photos_status_created_idx ON photos (status, created_at);
CREATE UNIQUE INDEX photos_owner_idempotency_uq ON photos (owner_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
