package postgres

import (
	"context"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/emil28092005/SciMesh/users/internal/domain"
	"github.com/emil28092005/SciMesh/users/internal/usecase"
)

// uniqueViolation is PostgreSQL's SQLSTATE for a unique-constraint breach.
const uniqueViolation = "23505"

var userColumns = []string{"id", "email", "password_hash", "role", "created_at", "updated_at"}

// UserRepo implements usecase.UserRepository on PostgreSQL.
type UserRepo struct {
	pool *pgxpool.Pool
}

func NewUserRepo(pool *pgxpool.Pool) *UserRepo {
	return &UserRepo{pool: pool}
}

func (r *UserRepo) Insert(ctx context.Context, u *domain.User) error {
	sql, args, err := psql.Insert("users").
		Columns(userColumns...).
		Values(u.ID, u.Email, u.PasswordHash, string(u.Role), u.CreatedAt, u.UpdatedAt).
		ToSql()
	if err != nil {
		return err
	}
	if _, err := conn(ctx, r.pool).Exec(ctx, sql, args...); err != nil {
		// A concurrent insert of the same email surfaces as a unique violation
		// on uq_users_email; translate it to the port's sentinel so the use
		// case never sees a driver type.
		if isUniqueViolation(err) {
			return usecase.ErrEmailExists
		}
		return err
	}
	return nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	return r.getBy(ctx, sq.Eq{"email": email})
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return r.getBy(ctx, sq.Eq{"id": id})
}

func (r *UserRepo) getBy(ctx context.Context, pred sq.Sqlizer) (*domain.User, error) {
	sql, args, err := psql.Select(userColumns...).From("users").Where(pred).ToSql()
	if err != nil {
		return nil, err
	}
	return scanUser(conn(ctx, r.pool).QueryRow(ctx, sql, args...))
}

func scanUser(row pgx.Row) (*domain.User, error) {
	var (
		u    domain.User
		role string
	)
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, usecase.ErrUserNotFound
		}
		return nil, err
	}
	u.Role = domain.Role(role)
	return &u, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
