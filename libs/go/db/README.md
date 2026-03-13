# db

Thin wrapper around `pgxpool` that creates a connection pool from a URL. All manmanv2 services that need PostgreSQL use this library.

## Usage

```go
pool, err := db.NewPool(ctx, "")        // reads PG_DATABASE_URL from environment
pool, err := db.NewPool(ctx, url)       // uses the provided URL directly
```

`NewPool` pings the database before returning. It returns an error if the connection fails.

## Environment Variable

| Variable | Description |
|----------|-------------|
| `PG_DATABASE_URL` | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/dbname` |

When `url` is empty string, `PG_DATABASE_URL` is read automatically. Pass an explicit URL only when a service needs two separate database connections.

## BUILD.bazel

```bazel
deps = [
    "//libs/go/db",
    ...
]
```
