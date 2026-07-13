# wharf

Run your local services without remembering how.

`wharf` discovers how every project in a directory runs, assigns each one a
stable port — its *berth* — and starts it with its dependencies and config.

It is workspace-agnostic: point it at any directory and it detects what is
there. Nothing about your projects is hard-coded.

## Quick start

```sh
wharf scan ~/code     # discover projects, write manifests
wharf ls              # what wharf knows, and what is running
wharf doctor          # what will break before you start anything
wharf berth           # give every service a port it owns
```

## Why berths

Services collide: several projects ship with `:8080` and four frontends all
default to `:5173`, so they cannot run together. wharf assigns each service a
port it owns and writes it into that service's own (gitignored) config, backing
up the original first. The fix is durable — the ports stay fixed even when you
run the service by hand, outside wharf.
