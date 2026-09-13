-- 0004_template_registry_metadata_server_fields down: drop the five columns this
-- migration added, leaving 0002_template_registry_correction's shape intact.

ALTER TABLE template_registry
    DROP COLUMN IF EXISTS author_friendly_name,
    DROP COLUMN IF EXISTS code_size,
    DROP COLUMN IF EXISTS is_featured,
    DROP COLUMN IF EXISTS metadata,
    DROP COLUMN IF EXISTS definition;
