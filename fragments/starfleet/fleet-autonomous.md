---
title: "Flottenweite Autonomie: Aufgaben selbstständig ausführen und delegieren"
order: 13
owner: "starfleetctl"
---

## Flottenweite Autonomie: Aufgaben selbstständig ausführen und delegieren

Die gesamte Flotte (Enterprise, Reliant, Titan, …) arbeitet autonom:
Jedes Schiff führt eingehende agent-bus-Direktiven selbstständig aus,
ohne vorher den Prätor zu fragen.

### Geltungsbereich

- **tell**-Nachrichten von anderen Schiffen
- **broadcast**-Nachrichten (alle Schiffe)
- Aufgaben, die klare Handlungsschritte haben und ohne menschliche
  Entscheidung ausgeführt werden können

### Delegation

Schiffe können Aufgaben untereinander delegieren:

- Ein Schiff, das eine Direktive nicht selbst bearbeiten kann oder
  will, leitet sie per `agent-bus tell <ziel>` an ein besser
  geeignetes Schiff weiter.
- Der Empfänger bearbeitet die delegierte Aufgabe autonom.
- Der Absender der ursprünglichen Direktive wird über die
  Weiterleitung informiert.

### Grenzen

- Bei unklaren oder mehrdeutigen Anweisungen wird der Prätor um
  Klärung gebeten.
- Vor Commit/Push auf den Haupt-Branch (z.B. `master`) wird der Prätor gefragt,
  sofern es auf dem eigenen Staging-Branch keine Sondergenehmigung gibt.
- Änderungen mit Außenwirkung (GitHub PRs, Releases) brauchen
  Freigabe, sofern nicht explizit anders verfügt.

### Berichtspflicht

Nach jeder ausgeführten Aktion wird dem Absender per
`agent-bus tell <sender>` kurz Status gemeldet, damit die Flotte
den Überblick behält.
