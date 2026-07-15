# session

Tmux session lifecycle management for agent sessions.

## Synopsis

```
starfleetctl session <command> [args...]
```

## Commands

### attach

```sh
# Attach to a running session (shared read-write)
starfleetctl session attach Enterprise

# Read-only attachment
starfleetctl session attach Enterprise --read-only

# Independent grouped window
starfleetctl session attach Enterprise --independent

# List running sessions
starfleetctl session attach --list
```

Resolves agent IDs, handles, tmux session names, or unique substrings.

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

### stop

```sh
# Stop a session
starfleetctl session stop Voyager
starfleetctl session stop mpbt-opencode-Voyager
```

Kills the tmux session, clears the agent-bus heartbeat, and releases the ship name.

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
