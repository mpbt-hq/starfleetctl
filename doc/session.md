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
appears on the fleet board and participates in agent-bus communication.

### stop

```sh
# Stop a session
starfleetctl session stop Voyager
```

Kills the terminal, clears the agent-bus heartbeat, and releases the ship name.

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
# Assign a ship name
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

## Worktrees

```sh
# Create a throwaway worktree
starfleetctl worktree add

# List worktrees
starfleetctl worktree list

# Remove a worktree
starfleetctl worktree remove <branch>
```
