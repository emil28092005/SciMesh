-- PostgreSQL cannot drop a single enum value without recreating the type and
-- rewriting every dependent column. Leaving 'running' in place is harmless: no
-- code writes it after the down of 0007 restores the leased-only transitions.
SELECT 1;
