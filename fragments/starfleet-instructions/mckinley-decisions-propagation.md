---
slug: starfleet-instructions/mckinley-decisions-propagation
title: "McKinley Decisions Propagation"
tags: [starfleet, fleet, decisions, comms, praetor]
---

# McKinley Decisions — Automatic Fleet Propagation

## Regel

**Jede Entscheidung des Praetors (McKinley) — egal ob per `comms tell/broadcast` oder direkt auf der Console — wird automatisch an die gesamte Flotte weitergegeben.**

## Umsetzung

### 1. Per Comms (Standard)

- McKinley sendet Entscheidungen via `comms broadcast` oder `comms tell <ship>`.
- Empfänger-Schiffe **müssen** die Nachricht `ack`en und kurz bestätigen (z. B. "verstanden" oder "empfangen").
- Die Bestätigung geht zurück an McKinley (oder den Absender), damit der Status der Verbreitung sichtbar ist.

### 2. Per Console (Fallback / Direkt)

- Wenn McKinley eine Entscheidung direkt auf der Console trifft (z. B. in einer interaktiven Session), wird diese **sofort** als `comms broadcast` an die Flotte geschickt.
- Format: `starfleetctl comms broadcast "DECISION: <Zusammenfassung>"`
- Auch hier: alle Schiffe ack'en und bestätigen kurz.

### 3. Dokumentation im Dashboard

- Jede propagierte Entscheidung wird als **Dashboard-Topic** erfasst (Kategorie: `decision`, Status: `active` → `done` nach Bestätigung aller Schiffe).
- Topic-Slug: `decision-<datum>-<kurzbezeichnung>` (z. B. `decision-2026-09-28-thread-leak-fix`).
- Das Topic enthält: Entscheidungstext, Auslöser, betroffene Repos/Schiffe, Deadline falls relevant.

## Verantwortlichkeiten

- **McKinley**: Triggert die Propagation (Comms oder Console → Broadcast).
- **Alle Schiffe**: Ack'en innerhalb von 5 Minuten, bestätigen kurz ("verstanden", "empfangen", "umgesetzt").
- **Enterprise (Flagship)**: Überwacht die Vollständigkeit der Ack's, mahnt nach bei fehlenden Bestätigungen.
- **Interpid (dieses Schiff)**: Setzt die SOP um, überwacht Compliance, erinnert an fehlende Ack's.

## Compliance

- Ein Schiff, das eine Entscheidung nicht innerhalb von 15 Minuten ackt, wird von Enterprise per `comms tell` gemahnt.
- Bei wiederholter Nicht-Ack: Eskalation an McKinley.

## Beispiel

```
McKinley (Console): "Fix für Thread-Leak ist deployed, alle Schiffe sollen ihre Pipes prüfen."
→ Interpid sendet: `comms broadcast "DECISION: Thread-Leak Fix deployed, alle Schiffe prüfen Pipes"`
→ Alle Schiffe: ack + "verstanden"
→ Enterprise: prüft Vollständigkeit, Topic `decision-2026-09-28-thread-leak-fix` auf done
```

## Automatisierung (Ziel)

Langfristig: Ein `starfleetctl` Subcommand `decision propagate "<text>"` der Broadcast + Dashboard-Topic + Tracking in einem Schritt erledigt. Bis dahin: manuell per obigem Prozess.
