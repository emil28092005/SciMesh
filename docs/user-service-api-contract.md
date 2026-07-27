# SciMesh User Service API contract (v1)

**Status:** `v1`. The User Service owns user accounts and issues access tokens.
The coordinator never receives user passwords and never accesses the User
Service database.

## Authentication boundary

- User Service signs access tokens; coordinator verifies them before accepting
  user-scoped requests.
- Tokens contain a UUID `sub`, `role` (`user` or `admin`), `verified`, `iat`,
  and `exp` claims.
- A user-authenticated caller may operate only workers whose `owner_id` equals
  `sub`. This applies to claim, heartbeat, result, failure, and artifact upload.
- Worker traffic authenticated with the coordinator's shared worker token has
  no user identity and remains an operator-only compatibility path.
- Role or verification changes take effect when the access token is renewed.
  Deployments needing immediate revocation must use a short token lifetime or a
  revocation mechanism before enabling volunteer-worker trust.

## Endpoints

All JSON request bodies reject unknown fields and are size-limited. Error
responses are JSON with a stable `error` value and request ID.

| Method | Path | Auth | Success |
| --- | --- | --- | --- |
| `GET` | `/health` | none | `200 {"status":"ok"}` |
| `POST` | `/register` | none | `201` user object |
| `POST` | `/login` | none | `200` user object and access token |
| `GET` | `/me` | Bearer access token | `200` current user |
| `POST` | `/users/{id}/verify` | Bearer admin token | `204` |
| `POST` | `/users/{id}/unverify` | Bearer admin token | `204` |
| `POST` | `/users/{id}/promote` | Bearer admin token | `204` |
| `POST` | `/users/{id}/demote` | Bearer admin token | `204` |

`POST /register` accepts `{ "email": string, "password": string }` and
always creates role `user` with `verified: false`. `POST /login` accepts the
same shape and returns `{ "token": string, "user": User }`. Password hashes,
JWT signing material, and raw tokens must never be logged.

## Coordinator integration tests

The coordinator must test that a JWT user cannot claim or mutate another
user's worker lease, including heartbeat, failure, result, and artifact upload.
Job and artifact access is restricted to the job owner unless the caller has
the admin role.
