-- 0002_template_registry_correction down: revert validators' column rename and
-- restore template_registry to 0001_init's original (guessed, incorrect) shape.
-- template_registry has never held real data so this is a clean drop+recreate, same
-- as the up migration.

COMMENT ON COLUMN validators.shard_group_end_inclusive IS NULL;
ALTER TABLE validators RENAME COLUMN shard_group_end_inclusive TO shard_group_end;

DROP TABLE IF EXISTS template_registry;

CREATE TABLE IF NOT EXISTS template_registry (
    template_address TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    tags             JSONB NOT NULL DEFAULT '[]'::jsonb,
    category         TEXT,
    metadata_hash    TEXT NOT NULL,
    published_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_template_registry_category ON template_registry (category);
