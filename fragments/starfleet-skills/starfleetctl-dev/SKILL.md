---
name: starfleetctl-dev
description: "Developing and deploying the starfleetctl source itself — claim protocol, make, bootstrap, daemon restarts in the right order, end-to-end verification. Load before editing anything in a starfleetctl source checkout, before running starfleet-bootstrap, or when a web/plugin change 'is done' but the fleet does not see it."
---

# starfleetctl-dev — working on the starfleetctl source

Handwerkliche Grundregeln stehen in `AGENTS.md` im Repo. Dieser Skill ist die
**Ablauf-Korrektur** dazu — was in welcher Reihenfolge passieren muss, damit eine
Änderung wirklich bei der Flotte ankommt.

Die **konkreten Pfade/Orte dieser Workspace** (mpbt-Dev-Clone, Worktrees, Bootstrap,
Ports) und die **Testbed-Kurzanleitung** stehen im Skill
**`starfleetctl-dev-workspace`** — lade beide zusammen, wenn du in dieser Workspace
am starfleetctl-Source arbeitest. Dieser Skill hier ist der generische Teil.

## Zwei Kopien — wer entwickelt, wer deployed

`starfleet-bootstrap` clont/aktualisiert das Repo nach `.starfleet-ai/src/starfleetctl`.
Das ist der **Deployment-Clone** — der `go:embed`-Input des Binaries. **Dort wird nie
entwickelt**; jeder Handgriff dort wird beim nächsten `git merge --ff-only`
überschrieben.

Entwickelt wird in einem **eigenen Clone** des Repos (in dieser Workspace: siehe
`starfleetctl-dev-workspace`), auf einem **eigenen Branch** — nicht direkt auf `master`.

| | Dev-Clone | Deployment-Clone |
|---|---|---|
| Zweck | Entwicklung, Branches, Tests | Produktiv-Deployment |
| Angefasst von | dir | nur `git pull` durch den Bootstrap |

### Welche Datei ist die Originalquelle? — die Verwechslung, die wirklich passiert

**Für Skills und Plugins ist die Originalquelle IMMER `fragments/` im Repo.** Die
Kopien unter `.claude/skills/` und `.opencode/plugins/` sind **generierte Artefakte**.

| Artefakt | Originalquelle | generierte Kopie |
|---|---|---|
| Skill `starfleet*` | `fragments/starfleet-skills/<name>/SKILL.md` | `.claude/skills/<name>/SKILL.md` — **nicht editieren** |
| opencode-Plugin | `fragments/opencode-plugins/<name>.ts` | `.opencode/plugins/<name>.ts` — **nicht editieren** |

**Was beim Editieren der generierten Kopie passiert:** `fixStarfleetSkills()`
(`internal/bootstrap/checks.go`) vergleicht byte-genau gegen den eingebetteten Stand
und **schreibt beim nächsten `./starfleet-bootstrap` die Fragment-Fassung zurück** —
kein Fehler, kein Abbruch, der Edit ist einfach weg.

Vor dem Editieren eines `starfleet*`-Skills also prüfen:

```sh
ls <dev-clone>/fragments/starfleet-skills/
# taucht der Name dort auf, ist das Fragment die Quelle — dort editieren.
```

### Kopplungskette — warum „Kopie manuell aktualisieren" nichts bringt

```
dev-clone  commit + PUSH ──► origin/master
                                   │ git fetch + merge --ff-only
                                   ▼
                     .starfleet-ai/src/starfleetctl/    (go:embed-INPUT)
                                   │ go build (bettet Fragmente/Skills ein)
                                   ▼
                     .starfleet-ai/bin/starfleetctl
                                   │ self-install
                                   ▼
                     .claude/skills/*, .opencode/plugins/*   (was Agenten laden)
```

Daraus folgt zwingend:

- Wer `.starfleet-ai/src/` von Hand „hochzieht", erreicht **nichts** — erst der
  **Neu-Bau** bettet den neuen Stand ein.
- Wer eine Plugin-Datei nur manuell nach `.opencode/plugins/` kopiert, erreicht
  **nichts** — der nächste `self-install` überschreibt sie aus dem Build.
- Solange der Commit nicht auf `origin/master` ist, gibt es **nichts zum Ziehen**.

**Letzte Stufe — eine LAUFENDE Session liest Skills nicht neu.** opencode scannt die
Skills **einmal pro Prozess** (im Log `init count=N` bei jedem Prozessstart) und hält
sie im Speicher; auch ein erneuter `skill`-Tool-Aufruf liefert den Stand vom
Prozessstart. Ein geänderter Skill wirkt daher erst in einem **neuen opencode-Prozess**
— d.h. Ship-Neustart (`session stop` + `ship-run`). Zwei Fallstricke:

- `sop-reload` (in-place `session.clear` + re-prompt) läuft im **selben** Prozess und
  lädt nur den SOP-Index neu — **nicht** die Skills.
- Auch wenn beim Neustart **dieselbe Session wieder aufgenommen** wird (`--session
  <id>`), ist der Skill-Registry trotzdem neu: der **Prozess** ist neu, die
  Konversations-Historie bleibt. Skills sind also aktuell, sobald der Prozess neu ist.

## 0. Claim, bevor du anfasst

Es darf **immer nur ein Schiff gleichzeitig** den starfleetctl-Source ändern. Vorher
per comms ankündigen, welchen Bereich du anfasst (`internal/web`, `internal/filestore`,
…). Ein Task-Status `done` ist **kein** Claim.

## 1. Bauen — `make all` ist Pflicht, nicht optional

```sh
cd <dev-clone>
make all          # baut Binary + check-plugin, Tests müssen grün sein
```

`make all` läuft durch Compile **und** Link — siehe „Pitfall: Build meldet grün, Link
ist kaputt".

## 2. Deployen — `./starfleet-bootstrap`

Am Workspace-Root, **nicht** im Source:

```sh
./starfleet-bootstrap
```

Der Bootstrap pullt `origin/master` in den Deployment-Clone, baut das Binary neu,
deployt es nach `.starfleet-ai/bin/starfleetctl` und installiert Fragmente/Skills/
Plugins; am Ende `sop reindex`.

**Ist das Binary nicht überschreibbar** (`text file busy`), den laufenden Server
**nicht** killen. Stattdessen das alte Binary löschen und neu deployen:

```sh
rm -f .starfleet-ai/src/starfleetctl/starfleetctl   # rm umgeht text-file-busy
./starfleet-bootstrap
```

## 3. Daemons neu starten — in dieser Reihenfolge

Nach jedem Binary-Wechsel laufen die Daemons **mit dem alten Binary**, bis man sie
explizit neu startet — der häufigste Grund für „ich habe doch deployed":

```sh
./.starfleet-ai/bin/starfleetctl web restart           # HTTP-UI
./.starfleet-ai/bin/starfleetctl timer worker restart
./.starfleet-ai/bin/starfleetctl model-proxy restart    # nur bei Go-Änderungen
```

Reihenfolge: **erst `bootstrap`, dann die Daemons.** `web restart` beendet die
laufende Web-Oberfläche (Arbeitswerkzeug des Praetors/der Web-Console-Schiffe) —
Häufungen vermeiden, einen Restart pro auslieferbarem Zustand, nicht pro Commit. Für
reine **Plugin**-Änderungen genügt ein `PLUGIN_VERSION`-Bump; für reine Go-Änderungen
reicht `make all` + `model-proxy restart`.

## 4. Verifizieren — nie „fertig" melden ohne Messung

Vier Checks, **live gegen das laufende System**:

```sh
# 1. Ist das neue Binary wirklich das deployte?
stat -c '%y %n' .starfleet-ai/bin/starfleetctl
stat -c '%y %n' <dev-clone>/starfleetctl
#    Binary-Zeitstempel muss >= Quell-Zeitstempel sein.

# 2. Läuft der Daemon mit dem neuen Binary?
go version -m /proc/<pid>/exe | grep vcs.revision   # exakter Commit des laufenden Prozesses
ps -eo pid,etimes,cmd | grep 'starfleetctl web start' | grep -v grep

# 3. Funktioniert die Änderung am laufenden System?
curl -s -D- -o /dev/null http://127.0.0.1:8080/ | head -20
#    Bei einem Frontend-Fix: die ausgelieferte Datei prüfen, nicht nur den Source.

# 4. Ist alles committed und gepusht?
cd <dev-clone>
git status --short          # muss leer sein
git rev-parse HEAD origin/master   # muss identisch sein
```

Erst wenn **alle vier** passen, darf „fertig" gemeldet werden — sonst Task
`in-progress`.

### Bei Fragment-/Plugin-Änderungen zusätzlich: Inhalt vergleichen, nicht Version lesen

Ein Versions-String ist **kein Nachweis**. Der eigentliche Beweis ist der
Byte-Vergleich Source vs. ausgeliefert:

```sh
diff -q <dev-clone>/fragments/opencode-plugins/starfleet-dispatch.ts \
        .opencode/plugins/starfleet-dispatch.ts   # "identisch" = deployed
```

Und die **Wirksamkeit**, nicht nur die Ankunft: hat die neue Logik eine beobachtbare
Wirkung? Sonst ist „deployed" nur eine Behauptung über Bytes.

`index.html` oder Fragmente werden nur durch `./starfleet-bootstrap` ausgeliefert —
ein reines `make all` ändert die ausgelieferte SPA **nicht**.

## Pitfall: Build meldet grün, Link ist kaputt

Ein entferntes `size_t ConnectionInfoSize = 0;` kompiliert in allen
Übersetzungseinheiten, erst der **Link** bricht ab (`undefined reference`). Ein reiner
Compile-Lauf meldet „erfolgreich". Und: der Build eine Sekunde **vor** dem letzten
Speichern deployt den vorherigen Stand. Deshalb Schritt 1 und 4 der Verifikation.

Merksatz: **`ninja` kompiliert, `ninja install` linkt und installiert. Beides zählt.**

## Pitfall: uncommittete Arbeit gilt als nicht existent

Nach jeder Änderung `git status --short`. **Und: committet ist nicht gepusht.** Für
Fragmente/Skills/Plugins ist der *gepushte* Stand auf `origin/master` die
Voraussetzung — der Bootstrap sieht nur, was auf `origin` liegt. Eine committete,
aber nicht gepushte Änderung liefert dieselbe Fehlermeldung wie gar keine.

## Pitfall: Temp-Dateien im Source

`.bak`, `before.txt`, `after.txt` o.ä. gehören **nicht** in den Source (sondern unter
`_WORK_/<projekt>/tmp/`). Vor dem Commit `git status --short` prüfen.

## Pitfall: Frontend ohne Cache-Header

Die Web-SPA wird ohne `Cache-Control`/`ETag`/`Last-Modified` ausgeliefert — nach einem
Deploy kann der Browser alten JS-Code zeigen. Beim Debuggen von „geht nicht" zuerst
einen **Hard-Reload** anfordern, und immer prüfen, ob der Fehler im **Backend** oder im
**ausgelieferten Frontend** liegt (die ausgelieferte Datei holen und darin suchen).

## 5. Modell-Proxy niemals direkt killen — immer `model-proxy restart`

Der **model-proxy** ist ein eigener Daemon, den starfleetctl verwaltet.

```sh
./.starfleet-ai/bin/starfleetctl model-proxy restart|stop|start
```

**Niemals** `kill`/`pkill`/`systemctl stop` gegen den Proxy — das hinterlässt
inkonsistente Zustände (verbliebene Sockets, hängende Verbindungen). Der offizielle
`restart` gibt Ressourcen sauber frei und lädt die aktuelle Config
(`.starfleet-ai/conf/model-proxy.yaml`) neu. Ein reines `model-proxy restart` reicht
**nur** für Config-Änderungen (yaml); bei Go-Änderungen erst `make all` + `bootstrap`.

Verifikation nach restart: `model-proxy status`; die Ausgabe muss für alle Provider
`ok` zeigen.

## 6. Branch + Testbed, bevor etwas auf `master`/live geht

**Jede Änderung zuerst auf einem eigenen Branch, im isolierten Testbed durchtesten —
erst danach auf `master` und live deployen.**

1. **Eigener Branch/Worktree** — nicht auf `master` arbeiten.
2. **Bauen** (`make all`) und eine **isolierte Instanz** hochziehen (eigene Workspace,
   eigene Ports, eigene Identität) und die Änderung **end-to-end** durchspielen —
   inklusive des Fehlerfalls, den sie behebt.
3. **Erst danach**: Merge auf `master`, dann `./starfleet-bootstrap` + Daemon-Restart.

Ein Testbed-Harness existiert in dieser Workspace; die konkreten Befehle stehen im
Skill **`starfleetctl-dev-workspace`** (Abschnitt „Isoliertes Testbed").

Warum so streng: der Fix läuft live durch den model-proxy; ein Proxy-/Daemon-Bug kann
die **ganze Flotte** lahmlegen. Isoliert testen kostet Minuten, ein Live-Ausfall kostet
die Flotte.

## 7. NIEMALS `pkill -f starfleetctl` (Incident 2026-10-13)

Für ein manuelles Deployment **niemals** einen breiten `pkill`-Befehl verwenden —
weder `pkill -f starfleetctl`, `pkill -f ship-supervisor` noch `kill`/`systemctl`
gegen die laufende Instanz. **Vor dem Kopieren das alte Binary löschen** (`rm -f`),
das umgeht das text-file-busy und ist die einzige zulässige Methode:

```sh
rm -f .starfleet-ai/bin/starfleetctl
cp <dev-clone>/starfleetctl .starfleet-ai/bin/starfleetctl
```

**Warum das so gefährlich ist — gemessen am 2026-10-13:** `pkill -9 -f "starfleetctl"`
matcht die **volle** Kommandozeile. Damit trifft es nicht nur die eigene Instanz,
sondern **jeden** laufenden Wrapper `starfleetctl run --client opencode …` **aller
Schiffe** gleichzeitig, den `timer worker` und den `web`-Daemon. Der `run`-Wrapper ist
der **Vordergrund-Prozessgruppen-Führer auf dem pts**; wird er gekillt, überlebt
`opencode --session …` (kein „starfleetctl" in der Kommandozeile) **verwaist** und hält
fd 0/1/2 weiter auf dem pts. Der Kernel gibt die Vordergrund-Kontrolle an die shell
zurück → das Terminal ist „total kaputt". Blast radius ist **flottenweit**, nicht lokal.

Merksatz: **Der Prozess, den man treffen will, und der Prozess, den man trifft, sind
bei `pkill -f` nie garantiert derselbe.** `rm -f` + `cp` hat diesen Nebeneffekt nicht.

## Melden

Report mit `starfleetctl reports submit ... --task-ref <slug>`, Body mit **Messwerten**
statt Behauptungen. Antwort auf comms an den Absender **und** an McKinley (die
web-console), wenn die Web-Oberfläche betroffen ist.
