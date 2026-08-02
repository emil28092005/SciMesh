-- 0002: per-workload enable/disable. Absence of a row means "enabled" (the
-- catalog default); a row only exists once an admin flipped a workload off or
-- back on.
CREATE TABLE IF NOT EXISTS workload_settings (
    workload   TEXT NOT NULL PRIMARY KEY,
    enabled    INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    updated_at INTEGER NOT NULL
);
