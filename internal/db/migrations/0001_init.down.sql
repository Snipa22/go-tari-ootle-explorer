-- 0001_init down: drop every table 0001_init.up.sql created, in reverse dependency
-- order (none of these four tables actually reference each other via FK in v1, but
-- reverse-creation-order is the safe default convention to follow regardless).

DROP TABLE IF EXISTS template_registry;
DROP TABLE IF EXISTS burn_claims;
DROP TABLE IF EXISTS validators;
DROP TABLE IF EXISTS ootle_blocks;
