---
name: starfleetctl-dev
description: "Developing and deploying the starfleetctl source itself — claim protocol, make, bootstrap, daemon restarts in the right order, end-to-end verification. Load before editing anything under _WORK_/starfleetctl/sources/starfleetctl, before running starfleet-bootstrap, or when a web/plugin change 'is done' but the fleet does not see it."
---

# starfleetctl-dev — Arbeiten am starfleetctl-Source

Der Source liegt **ausschließlich** hier:

    _WORK_/starfleetctl/sources/starfleetctl

Das ist ein mpbt-managed Clone, Branch `master`, mit gesetztem `make-pr`-Config.
`.starfleet-ai/src/starfleetctl` ist nur noch das **Deployment-Target** des Binaries —
dort wird **nicht** entwickelt. Alles, was man dort editiert, geht beim nächsten
Bootstrap verloren.

Handwerkliche Grundregeln stehen in `AGENTS.md` im Repo. Dieser Skill ist die
**Ablauf-Korrektur** dazu — also was in welcher Reihenfolge passieren muss, damit eine
Änderung wirklich bei der Flotte ankommt.

## Zwei Kopien — wer entwickelt, wer deployed

Das ist der Standard-Mechanismus, **kein Sonderfall**: `starfleet-bootstrap` clont
die Repo selbst nach `.starfleet-ai/src/starfleetctl` (Zeile 33 `SRC=`, Zeile 80
`git clone`). Das ist der **Produktivpfad für Nutzer** und gehört so.

Für **aktive Entwicklung** haben wir zusätzlich den mpbt-Clone
`_WORK_/starfleetctl/sources/starfleetctl` — dort kann man sich frei austoben, ohne
das Deployment zu stören.

| | mpbt-Clone | Bootstrap-Clone |
|---|---|---|
| Pfad | `_WORK_/starfleetctl/sources/starfleetctl` | `.starfleet-ai/src/starfleetctl` |
| Zweck | **Entwicklung**, Branches, Tests, austoben | **Produktiv-Deployment** |
| Upstream | `mpbt-hq/starfleetctl` | `mpbt-hq/starfleetctl` |
| Angefasst von | aktivem Entwickeln | niemandem — nur `git pull` durch Bootstrap |
| Verwaltet von | mpbt | Bootstrap-Skript |

**Faustregel: aktive Entwicklung nur im mpbt/workspace-Clone, Produktiv-Deployment
über den Bootstrap-Clone.** Nie im Bootstrap-Clone editieren — dort liegen nur
Artefakte, jeder Handgriff wird beim nächsten `git merge --ff-only` überschrieben.

### Welche Datei ist die Originalquelle? — die Verwechslung, die wirklich passiert

**Für Skills und Plugins ist die Originalquelle IMMER dieses Repo** unter
`fragments/`. Der Workspace-Copy ist ein **generiertes Artefakt**. Nicht umgekehrt.

| Wer | Originalquelle | Workspace-Copy |
|---|---|---|
| Skill `starfleet*` | `fragments/starfleet-skills/<name>/SKILL.md` | `.claude/skills/<name>/SKILL.md` — **generiert, nicht editieren** |
| opencode-Plugin | `fragments/opencode-plugins/<name>.ts` | `.opencode/plugins/<name>.ts` — **generiert, nicht editieren** |
| xlibre-Skill (`backport`, `licensing`, …) | `.claude/skills/<name>/SKILL.md` im **mpbt-workspace** | — (handgepflegt, Bootstrap fasst sie nicht an) |

Die Verwechslung ist natürlich, weil beides unter `.claude/skills/` liegt und 26 von
26 Workspace-Skills nebeneinander im selben Verzeichnis stehen — die meisten davon
sind handgepflegt, die sechs `starfleet*` sind generiert. Ein Diff-Zustand im
Workspace-Copy sieht deshalb vollkommen normal aus.

**Was beim Editieren im Workspace-Copy passiert:** `verifyStarfleetSkills()`
(`internal/bootstrap/checks.go:520`) vergleicht byte-genau (`string(data) !=
string(current)`, Zeile 555) gegen das, was der Binary ausliefern würde, meldet
`stale: <name>/SKILL.md` (Zeile 568), und `fixStarfleetSkills()` (Zeile 573)
**schreibt beim nächsten `./starfleet-bootstrap` die Fragment-Fassung zurück**. Kein
Fehler, kein Abbruch — der Edit ist einfach weg. Es gab trotz geprüftem
`git status` keinen Hinweis.

Am 2026-09-30 ist das genau so gelaufen: eine Skill-Erweiterung committet und
gepusht ins mpbt-workspace, „fertig" gemeldet, und beim nächsten Bootstrap
(12:18) still überschrieben. Der Fix war trivial (`git show <commit>:<pfad>`),
die verlorene Zeit nicht.

**Vor dem Editieren eines `starfleet*`-Skills also prüfen:**

```sh
ls _WORK_/starfleetctl/sources/starfleetctl/fragments/starfleet-skills/
# taucht der Name dort auf, ist das Fragment die Quelle — dort editieren.
```

Für `xlibre/*`-Skills gilt das Umgekehrte: die liegen bewusst nur im Workspace und
gehören in `mtx/agent-config` committet. Beides ist richtig — entscheidend ist, dass
man weiß, welcher der beiden Fälle vorliegt.

### Kopplungskette — warum „Kopie manuell aktualisieren" nichts bringt

Der entscheidende Punkt: **`.starfleet-ai/src/` ist der `go:embed`-Input des Binaries,
nicht die Direktquelle des Plugins.** Das ausgelieferte Plugin kommt aus dem
*eingebetteten* Stand des Builds.

Das ist der Punkt, der am häufigsten Zeit kostet (2026-09-30 dreimal geschehen):

```
_mpbt-Clone__  commit + PUSH  ──►  origin/master
                                        │
                                        ▼  git fetch + merge --ff-only
                              .starfleet-ai/src/starfleetctl/     (go:embed-INPUT)
                                        │
                                        ▼  go build  →  bettet Fragmente/Skills ein
                              .starfleet-ai/bin/starfleetctl
                                        │
                                        ▼  "starfleetctl self-install"
                              .opencode/plugins/*.ts               (was OpenCode lädt)
```

Der entscheidende Punkt: **`.starfleet-ai/src/` ist der `go:embed`-Input des Binaries,
nicht die Direktquelle des Plugins.** Das ausgelieferte Plugin kommt aus dem
*eingebetteten* Stand des Builds.

Daraus folgt zwingend:

- Wer `.starfleet-ai/src/` von Hand „hoizieht", erreicht **nichts** — das Binary
  bettet weiter den alten Stand ein. Erst der **Neu-Bau** wirkt.
- Wer die Datei nur manuell nach `.opencode/plugins/` kopiert, erreicht **nichts** —
  der nächste `self-install` überschreibt sie aus dem alten Build.
- Solange der Commit nicht auf `origin/master` ist, gibt es **nichts zum Ziehen**.
  Deshalb scheitern alle Kopier-Versuche von Hand.

Die korrekte Kette steht in Schritt 1–2; die Verifikation in Schritt 4 ist kein
Opt-out, denn nur sie unterscheidet „kompiliert" von „angekommen".

## 0. Claim, bevor du anfasst

Es darf **immer nur ein Schiff gleichzeitig** den starfleetctl-Source ändern.

```sh
# Prüfen, wer den Source belegt hat
./.starfleet-ai/bin/starfleetctl comms board
./.starfleet-ai/bin/starfleetctl comms msgs --json | tail -40
```

Besser: **vorher** per comms ankündigen, welchen Bereich du anfasst
(`internal/web`, `internal/filestore`, …). Wer den Claim hat, den lässt niemand
parallel editieren. Ein Task-Status `done` ist **kein** Claim.

## 1. Bauen — `make all` ist Pflicht, nicht optional

```sh
cd _WORK_/starfleetctl/sources/starfleetctl
make all          # baut Binary + check-plugin, Tests müssen grün sein
```

`make all` läuft durch Compile **und** Link. Das ist der entscheidende Punkt, siehe
„Pitfall: Build meldet grün, Link ist kaputt" unten.

## 2. Deployen — `./starfleet-bootstrap`

```sh
cd /home/nekrad/src/xorg/mpbt-workspace
./starfleet-bootstrap
```

Läuft am Workspace-Root, **nicht** im Source. Der Bootstrap baut das Binary neu,
deployt es nach `.starfleet-ai/bin/starfleetctl` und installiert Fragmente/Skills/Plugins.
Er macht am Ende `sop reindex`.

**Ist das Binary nicht überschreibbar** (`text file busy`), den laufenden Webserver
nicht einfach killen. Stattdessen:

```sh
rm -f .starfleet-ai/src/starfleetctl/starfleetctl   # rm umgeht text-file-busy
./starfleet-bootstrap
```

## 3. Daemons neu starten — in dieser Reihenfolge

Nach jedem Binary-Wechsel laufen die Daemons **mit dem alten Binary**, bis man sie
explizit neu startet. Das ist der häufigste Grund für „ich habe doch deployed":

```sh
./.starfleet-ai/bin/starfleetctl web restart        # Port 8080
./.starfleet-ai/bin/starfleetctl timer worker restart
./.starfleet-ai/bin/starfleetctl model-proxy restart   # nur bei reinen Go-Änderungen
```

Reihenfolge nicht beliebig: erst `bootstrap`, dann die Daemons. Wer die Daemons vorher
neu startet, startet sie mit dem alten Binary.

**`web restart` beendet die laufende Web-Oberfläche.** Das Web ist das Arbeitswerkzeug
des Praetors und der web-console-Schiffe. Jeder Restart trennt offene Sitzungen, und
während des Neustarts schlagen Klicks ins Leere. Deshalb:

- **Häufungen vermeiden.** Ein Restart pro auslieferbarem Zustand, nicht pro Commit und
  nicht pro Zwischenergebnis. Wer fünfmal in zehn Minuten neu startet, macht die Flotte
  unbenutzbar, ohne je etwas zu gewinnen — das ist als „Web killt und startet neu"
  aufgefallen und ist als Fehler zu behandeln.
- **Ankündigen**, wenn ein Restart nötig ist, damit niemand gerade klickt.
- Für reine **Plugin**-Änderungen genügt `PLUGIN_VERSION`-Bump; für reine Go-Änderungen
  reicht `make all` + `model-proxy restart` ohne vollständigen Bootstrap.
- Läuft bereits ein `starfleet-bootstrap` oder `make`, **nicht** zusätzlich neu starten.

`web autostart` (aus dem minute-Cron) ist **idempotent**: es prüft
`IsWebServerRunning(addr)` und startet kein zweites Exemplar. Es ist **kein** Ersatz für
einen bewussten Restart und kein Übeltäter, wenn die PID wechselt — in dem Fall hat
jemand explizit `web restart` oder `bootstrap` ausgeführt.

## 4. Verifizieren — nie „fertig" melden ohne Messung

Die Fertigmeldung ist der Teil, der am häufigsten falsch ist. Vier Checks, in dieser
Reihenfolge, jeweils **live gegen das laufende System**:

```sh
# 1. Ist das neue Binary wirklich das deployte?
stat -c '%y %n' .starfleet-ai/bin/starfleetctl
stat -c '%y %n' _WORK_/starfleetctl/sources/starfleetctl/starfleetctl
#    Binary-Zeitstempel muss >= Quell-Zeitstempel sein.

# 2. Läuft der Daemon mit dem neuen Binary?
ps -eo pid,etimes,cmd | grep 'starfleetctl web start' | grep -v grep

# 3. Funktioniert die Änderung am laufenden System?
curl -s -D- -o /dev/null http://127.0.0.1:8080/ | head -20
#    Bei einem Frontend-Fix: die ausgelieferte Datei prüfen, nicht nur den Source.
curl -s http://127.0.0.1:8080/ | grep -c '<die neue zeile>'

# 4. Ist alles committed und gepusht?
cd _WORK_/starfleetctl/sources/starfleetctl
git status --short          # muss leer sein
git log --oneline -1
git log -1 --format='%(trailers:key=Signed-off-by,valueonly)'
git rev-parse HEAD origin/master   # muss identisch sein
```

Erst wenn **alle vier** passen, darf „fertig" gemeldet werden. Wenn ein Schritt fehlt,
ist der Task `in-progress`, nicht `done`.

### Bei Fragmente-/Plugin-Änderungen zusätzlich: Inhalt vergleichen, nicht Version lesen

Ein Versions-String ist **kein Nachweis**. Am 2026-09-30 meldete ein Schiff
„v2.5.4 deployed, verifiziert über `bootstrap --fix`" — deployed war weiterhin 2.5.3,
weil nur die Versionskonstante geprüft wurde und der Bootstrap-Output naturgemäß
„up to date" sagt, sobald eine Datei existiert.

```sh
# Version UND Inhalt müssen übereinstimmen — mit beiden Kopien:
grep -n "PLUGIN_VERSION = " _WORK_/starfleetctl/sources/starfleetctl/fragments/opencode-plugins/starfleet-dispatch.ts
grep -n "PLUGIN_VERSION = " .starfleet-ai/src/starfleetctl/fragments/opencode-plugins/starfleet-dispatch.ts
grep -n "PLUGIN_VERSION = " .opencode/plugins/starfleet-dispatch.ts   # <- was OpenCode wirklich lädt

# Der eigentliche Beweis — Byte-Vergleich Source vs. ausgeliefert:
diff -q _WORK_/starfleetctl/sources/starfleetctl/fragments/opencode-plugins/starfleet-dispatch.ts \
        .opencode/plugins/starfleet-dispatch.ts
# "identisch" — erst das ist deployed. Bei jeder anderen Abweichung: nicht fertig.

# Und die Wirksamkeit, nicht nur die Ankunft: hat die neue Logik eine
# beobachtbare Wirkung? Sonst ist "deployed" nur eine Behauptung über Bytes.
```

Zusätzlich: **Wenn `index.html` oder ein Fragment geändert wurde**, ist ein
`./starfleet-bootstrap` nötig — der Bootstrap installiert sie. Ein reines `make all`
ändert die ausgelieferte SPA **nicht**.

## Pitfall: Build meldet grün, Link ist kaputt

Zwei Vorkommnisse in derselben Session, beide von „Build successful" begleitet:

- `size_t ConnectionInfoSize = 0;` wurde beim Aufräumen einer Leerzeile gelöscht. Alle
  Übersetzungseinheiten kompilieren, erst der Link bricht ab
  (`undefined reference to 'ConnectionInfoSize'`). Ein reiner Compile-Lauf meldet
  „erfolgreich".
- Der Build lief **eine Sekunde vor** dem letzten Speichern. Was deployed und gestartet
  wurde, war der vorherige Stand. Deshalb Schritt 1 und 4 in der Verifikation.

Merksatz: **`ninja` kompiliert, `ninja install` linkt und installiert. Beides zählt.**

## Pitfall: uncommittete Arbeit gilt als nicht existent

Zweimal wurde „fertig" gemeldet, während die Änderung nur im Working Tree lag — einmal
davon wurde schon gepusht und war damit ein kaputter Branch auf `origin`. Nach jeder
Änderung `git status --short`. Wenn dort etwas steht, ist es nicht fertig.

**Und: committet ist nicht gepusht.** Für Fragmente/Skills/Plugins ist der
*gepushte* Stand auf `origin/master` die Voraussetzung — der Bootstrap zieht
`.starfleet-ai/src/` per `git merge --ff-only origin/master`, also sieht er nur,
was auf `origin` liegt. Eine committete, aber nicht gepuschte Änderung liefert
dieselbe Fehlermeldung wie gar keine: deployed bleibt der alte Stand, ohne dass
irgendwo ein Fehler sichtbar wird.

Der 2.5.4-Fall vom 2026-09-30 lief drei Runden, bis das gefunden war: Hand-Kopie ins
Deployment, dann `bootstrap --fix` — beides wirkungslos, weil der Commit nie
erstellt war. Diagnose in einem Schritt:

```sh
cd _WORK_/starfleetctl/sources/starfleetctl
git status --short fragments/                     # uncommittet?
git show HEAD:fragments/opencode-plugins/starfleet-dispatch.ts | grep PLUGIN_VERSION
git rev-parse HEAD origin/master                  # identisch? sonst fehlt der Push
```

## Pitfall: Temp-Dateien im Source

`.bak`, `before.txt`, `after.txt` und ähnliches gehören **nicht** in den Source. Sie
landen unter `_WORK_/<projekt>/tmp/`. Vor dem Commit `git status --short` prüfen und
unktrackte Dateien mit `#include <assert.h>`-Klammern im Source-Tree aufräumen.

## Pitfall: Frontend ohne Cache-Header

Die Web-SPA wird ohne `Cache-Control`, `ETag` und `Last-Modified` ausgeliefert. Nach
einem Deploy kann der Browser deshalb alten JavaScript-Code anzeigen, obwohl der Server
lange die richtigen Bytes ausliefert. Symptom: „die Änderung ist doch drin, aber es
funktioniert noch immer nicht".

Für SPA-Antworten `Cache-Control: no-cache` plus ETag setzen. **Nicht** für `/api/`-Antworten
— das Frontend pollt dauernd, und `no-cache` ohne ETag erzeugt bei jedem Poll eine volle
Antwort statt eines `304`.

Beim Debuggen von „geht nicht" zuerst einen **Hard-Reload** anfordern, bevor weitere
Ursachen gesucht werden. Und immer prüfen, ob der Fehler im **Backend** oder im
**ausgelieferten Frontend** liegt: die ausgelieferte Datei holen und darin suchen, nicht
im Source.

## Melden

Report mit `starfleetctl reports submit ... --task-ref <slug>`, Body mit den Messwerten
statt Behauptungen. Antwort auf comms an den Absender **und** an McKinley (die
web-console), wenn die Web-Oberfläche betroffen ist.
