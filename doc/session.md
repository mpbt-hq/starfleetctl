# session

Agent session management using termctl terminals.

## Synopsis

```
starfleetctl session <command> [args...]
```

## Commands

### attach

```sh
# Attach to a running terminal (shared read-write)
starfleetctl session attach Enterprise

# List running terminals
starfleetctl session attach --list
```

Resolves agent IDs, ship names, bus handles, or unique substrings.

### run

```sh
# Launch a new agent session
starfleetctl session run 25.2 --client opencode --name Voyager

# With additional options
starfleetctl session run 25.2 \
  --client claude \
  --name Enterprise \
  --tier standard \
  --supervisor admin \
  --permission-mode controlled

# Print launch variables instead of starting
starfleetctl session run 25.2 --print
```

### ship-run

```sh
# Start a background ship (detached termctl terminal)
starfleetctl session ship-run --name Voyager

# With specific model
starfleetctl session ship-run --name Voyager --model opencode/big-pickle
```

Launches an opencode control-agent session in a detached terminal. The ship
appears on the fleet board and participates in comms communication.

### stop

```sh
# Stop a session
starfleetctl session stop Voyager
```

Kills the terminal, clears the comms heartbeat, and releases the ship name.

Only detached ships may be stopped this way — check the launch type with
`starfleetctl session list` first. Ships launched with launch type `terminal`
have a human at that console; leave them running and ask the praetor to stop
them by hand.

### autoscale

```sh
# Show fleet size and capacity
starfleetctl session autoscale status
starfleetctl session autoscale status --max 8

# Spawn additional agents as needed
starfleetctl session autoscale need 3 \
  --reason "heavy PR backlog" \
  --client opencode \
  --dry-run
```

## Ship Names

```sh
# Assign the next available ship name
starfleetctl ship-names assign

# Assign as flagship (control agent)
starfleetctl ship-names assign flagship

# List assigned names
starfleetctl ship-names list
starfleetctl ship-names list --json

# Release a name
starfleetctl ship-names release Voyager

# Garbage-collect stale names
starfleetctl ship-names gc

# Get shell environment (PS1 prefix, EXIT trap)
eval "$(starfleetctl ship-names shell-env)"
```

### Automatic allocation

When a session is started without an explicit name, starfleetctl normally
takes the first available name from the configured ship-name list. The list
is processed in order: with the built-in defaults, worker names start with
`Defiant`, `Voyager`, `Discovery`, `Excelsior`, and so on. Thus, when all
these names are free, the first automatically named worker gets `Defiant`,
the next gets `Voyager`, and the next gets `Discovery`. The flagship name
(default `Enterprise`) is excluded from the worker pool.

The order can be customized with `fleet.ship_names` in
`.starfleet-ai/conf/fleet.yaml`. The legacy
`.starfleet-ai/conf/ship-names.yaml` is also supported when no list is
configured in `fleet.yaml`.

Before scanning the list, the allocator makes a best-effort attempt to reuse
a stale reservation recorded for the current TTY. This is only a restart
convenience, not a persistent TTY-to-ship assignment. In the usual case,
automatic allocation follows the configured list order; it does not bind a
ship name permanently to a terminal.

### Explicit names and reservations

Use the launch command's `--name` option when a particular identity is
required, for example:

```sh
./run-opencode.ship --name Voyager
```

A name can be owned by only one live runner/OpenCode session at a time.
Starting another session with an already-used name is rejected rather than
sharing the existing ship identity or OpenCode session. A reservation remains
in use while either its runner process or its recorded OpenCode process is
alive. Once both have exited, the stale reservation can be reclaimed by a
later allocation or by `starfleetctl ship-names gc`.

## Worktrees

```sh
# Create a throwaway worktree
starfleetctl worktree add

# List worktrees
starfleetctl worktree list

# Remove a worktree
starfleetctl worktree remove <branch>
```
