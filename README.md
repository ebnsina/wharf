# wharf

Run your local services without remembering how.

`wharf` reads your projects, works out how each one runs, gives it a port it
owns — its **berth** — and starts it with its dependencies, its config and its
database.

It knows nothing about any particular workspace. Point it at a directory and it
detects what is there, so it works the same on a new machine or someone else's
repo.

```sh
wharf scan ~/code     # discover projects, write a manifest for each
wharf doctor          # what will break, before you start anything
wharf tui             # the dashboard
wharf up frontend     # start a service and everything it needs
wharf db auth-api     # open a psql shell — no lookup required
```

## The problem it solves

A workspace of microservices accumulates knowledge that lives only in your head:
which command starts this one, which database it wants, which port it listens
on, what has to be running first. None of it is written down in one place, and
some of it is contradictory — several services ship with `:8080`, so they cannot
all run at once.

wharf writes it all down, by reading it out of the code.

## Berths

Detected ports collide. wharf assigns each service a port it owns and, because
these services read a hard-coded config path and accept no override, writes that
port into the service's own config file.

That edit is surgical: only the port value changes. Comments, ordering and
formatting survive, the original is backed up under `~/.wharf/backups/`, and
running it twice does nothing the second time. These config files are gitignored
local files, so nothing lands in version control.

The fix is durable rather than a spell wharf recasts on every launch — run `air`
by hand, outside wharf, and the service still comes up on the right port.

## The gateway

If a local reverse proxy fronts your services, wharf generates its config from
the same manifests that assign the berths. A route can no longer point at a port
a service is not on, because nobody types the port twice.

```sh
wharf gateway import path/to/your/nginx.conf
wharf gateway apply
```

`import` matches each existing route to the service that *originally declared*
its port, not to whoever holds that port now — otherwise a route would be handed
to whichever service won the collision.

Both `nginx` and `caddy` drivers are supported. Caddy is recommended: snippets
remove the per-route boilerplate, WebSockets need no directives, and local HTTPS
is automatic.

## Manifests

`wharf scan` writes one manifest per service under `~/.wharf/services/`.
Detection is a starting point, not an authority — edit any manifest to correct
it, and a rescan keeps your edits.

```yaml
name: billing-api
path: /Users/you/code/billing-api
kind: service
stack: go
berth: 8103
declared_berth: 8080      # what it used to ask for
processes:
  - name: api
    cmd: air
    primary: true
  - name: worker
    cmd: air -c .air.worker.toml
    autostart: false      # rarely needed; start it from the dashboard
config:
  - format: yaml
    path: config/config.yaml
    template: config/config.example.yaml
    port_key: app.port
    port_template: "{port}"
needs:
  - type: postgres
    dsn: postgres://postgres@localhost:5432/billing
  - type: redis
    dsn: redis://localhost:6379
lifecycle:
  migrate: go run ./cmd migrate up
```

## Commands

| | |
|---|---|
| `wharf scan <dir>` | discover projects and write manifests |
| `wharf ls` | services, berths, what is running |
| `wharf tui` | dashboard: start, stop, live logs |
| `wharf up <svc>` | start a service, its dependencies and its infra |
| `wharf db <svc>` | open a shell on that service's database |
| `wharf bootstrap <svc>` | fresh clone → config, install, migrate, seed |
| `wharf berth` | write each service's berth into its config |
| `wharf doctor` | contested ports, missing configs, dead upstreams |
| `wharf gateway import/apply` | generate the reverse-proxy config |

## Install

```sh
go install github.com/ebnsina/wharf/cmd/wharf@latest
```

State lives in `~/.wharf` (override with `WHARF_HOME`).
