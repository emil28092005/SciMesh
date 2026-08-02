BEGIN;

-- Per-workload enable/disable. Absence of a row means "enabled" (the catalog
-- default); a row only exists once an admin flipped a workload off or back on.
CREATE TABLE workload_settings (
    workload   text        NOT NULL PRIMARY KEY,
    enabled    boolean     NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

COMMIT;
