# routes

how we talkin

## definitions
game_id = `install_method:install_identifier`
    - install_method = steamcmd, docker
    - install_identifier = app_id, imagename

## manman-host

### workflow
- host process started, running
- worker process started
    - worker hits /worker/
    - worker registers at /worker/id
        - unsure why this pattern, but feels right
- worker periodically submits health check at /worker/id/health
    - host will attempt to kill worker if no health check for X period

- host receives command for server
    - checks if alive
    - sends command to registered server if alive
        -

### routes

- GET /server
    - list all servers
    - query parameter for worker_id, game_id
- POST /server
    - creates new server
    - worker_id
    - game_id
- GET /server/id
    - info about server
    - custom status info in subclasses? or just generic way for extra status?
    - can return url for server
- GET /server/id/log
    - returns URL to read log (?)
- POST /server/id/health
    - worker server threads post perioidcally to confirm health
- DELETE /server/id
    - deletes a server
    - destructive
- PUT /server/id
    - update server configuration (unsure what scope of 'update' is at this point)
- POST /server/id/command
    - send command to a particular server

- GET /worker
    - list all workers
- POST /worker
    - returns info that worker needs to send to register as a worker
- POST /worker/id
    - id is retrieved from /worker above
    - create/register worker
    - worker-name
    - ip-address
    - api-url/port
    - info about server? max runtime args? not useful yet
- GET /worker/id
    - get info about particular worker
- PUT /worker/id
    - update
- DELETE /worker/id
    - shutdown i guess?
- POST /worker/id/health
    - worker posts periodically for health check


## manman-worker

all state for worker is stored locally in sql-lite. Desired config is in manman-host, and should be worked towards in worker.
need to keep worker in sync for this to work

ideally the main postgres database is never exposed

### statuses
- alive
- shutdown
- frozen
- unknown

### routes
- GET /status
    - just returns ok
    - could return resource utilization later
- POST /freeze
- POST /shutdown
    - shutsdown worker
- GET /server
    - list servers running on worker
- POST /server/
    - create server on worker
    - IDEA: - pass in id? it is local to server
- GET /server/id
    - return server status
- POST /server/id/command
    - send custom or pre-determined command(s)
- POST /server/id/start
    - start server
- POST /server/id/shutdown
    - shutdown server
- POST /server/id/kill
    - kill server
- DELETE /server/id
    - delete server id
    - likely want to use shutdown, delete doesn't make sense here
- GET /server/id/log
    - get log
    - could return dedicated url for streaming
    - some endpoint for streaming log?

## extra
COMMAND TYPES

class MessageType(enum.Enum):
    HEALTH = 1
    START = 2
    STOP = 3
    KILL = 4

    COMMAND_ = 200
