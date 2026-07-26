# SciMesh userservice

Authentication service for SciMesh, in Go on PostgreSQL. It owns user accounts
and issues the JWTs the coordinator trusts. It is a **separate bounded context**
from the coordinator: its own database, its own binary. The only thing shared
between the two services is the JWT signing secret.

Built as a modular monolith following Clean Architecture — one binary, four
layers, dependencies pointing strictly inward:

```
   infra       config, DB pool, clock, HTTP server     ← drivers
   transport   HTTP handlers + JWT middleware          ← incoming
   storage     SQL repository                          ← outgoing
   usecase     Register / Login + PORTS (interfaces)   ← application rules
   domain      User, Role, invariants                  ← business rules
   auth        bcrypt hasher, HS256 JWT issuer         ← crypto adapters
```

## Endpoints

| Method | Path        | Auth        | Purpose                                  |
|--------|-------------|-------------|------------------------------------------|
| GET    | `/health`   | none        | Liveness probe (checks the database)     |
| POST   | `/register` | none        | Create an account (always role `user`)   |
| POST   | `/login`    | none        | Verify credentials, return a signed JWT  |
| GET    | `/me`       | Bearer JWT  | Return the caller's own account          |

Roles are `user` and `admin`. Registration always creates a `user`; promotion to
`admin` is a manual database operation, never a request. The role→permission
mapping lives in the coordinator's authorization checks, not in a table.

## How it connects to the coordinator

The coordinator never calls this service at runtime. A client logs in here, gets
a JWT, and presents it to the coordinator, which verifies the signature locally
with the same `JWT_SECRET` and reads `sub` (the user id) into `jobs.owner_id`.

That link is **off by default**: until the coordinator is given a matching
`JWT_SECRET`, it accepts only the shared worker token and stores `owner_id` as
NULL. Set the same secret (≥ 32 bytes, byte-for-byte identical) on both services
to turn it on.

## Run

```sh
# whole stack: Postgres + migrations + the service on :8081
make up

# or locally against your own Postgres
cp .env.example .env   # then edit JWT_SECRET and DATABASE_URL
make run
```

## Verify

```sh
make test              # unit tests
make check             # vet, lint, race, integration, smoke — needs Docker
make smoke             # end-to-end against a running service
```

Password hashing uses bcrypt (`golang.org/x/crypto/bcrypt`); the salt and cost
are embedded in the stored hash, so there is no separate salt column. Tokens are
HS256 (`github.com/golang-jwt/jwt/v5`).
