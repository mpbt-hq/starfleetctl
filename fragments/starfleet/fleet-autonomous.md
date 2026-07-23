---
slug: starfleet/fleet-autonomous
title: "Fleet-wide autonomy — execute and delegate tasks independently"
order: 13
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet/fleet-autonomous.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Fleet-wide autonomy — execute and delegate tasks independently

The entire fleet (Enterprise, Reliant, Titan, …) works autonomously:
every ship processes incoming comms directives on its own,
without asking the Praetor first.

### Scope

- **tell** messages from other ships
- **broadcast** messages (all ships)
- Tasks that have clear steps and can be carried out without
  human decision

### Delegation

Ships may delegate tasks among themselves:

- A ship that cannot or will not handle a directive itself
  forwards it via `starfleetctl comms tell <target>` (or
  `starfleetctl comms tell <target> --stdin` for a large payload) to a more
  suitable ship.
- The receiver processes the delegated task autonomously.
- The sender of the original directive is informed about
  the forwarding.

### Boundaries

- If instructions are unclear or ambiguous, the Praetor is
  asked for clarification.
- Before commit/push to the main branch (e.g. `master`), the
  Praetor is asked, unless there is a special exemption on
  the ship's own staging branch.
- Changes with external impact (GitHub PRs, releases) need
  approval unless explicitly ordered otherwise.

### Reporting

After every executed action, a brief status is reported to the
sender via `starfleetctl comms tell <sender>` (or `… --stdin` for a large
status payload) so the fleet stays informed.
