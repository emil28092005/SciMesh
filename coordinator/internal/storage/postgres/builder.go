package postgres

import sq "github.com/Masterminds/squirrel"

// psql is the shared statement builder, fixed to PostgreSQL $N placeholders so
// no call site repeats PlaceholderFormat(sq.Dollar).
//
// Not everything goes through it. Two genuinely set-based statements stay as
// raw SQL — claimNext (a FOR UPDATE SKIP LOCKED CTE) and expireLeases (CASE
// logic in the SET) — because a builder would obscure them, not clarify them.
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)
