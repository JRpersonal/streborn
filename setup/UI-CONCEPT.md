# STR Setup Tool: UI Konzept

Roadmap und Architektur des Setup Tools. Aktuell als Console Wizard implementiert, langfristig als GUI Anwendung mit Installer.

## Phasen

### Phase 1: Console Wizard (jetzt)

PowerShell Skript mit Menue Struktur. Doppelklick startet die Console, Menue erscheint.

```
STR Wizard
======================

  [1] Box einrichten
        a. Werkszustand zuruecksetzen (geschuetzt)
        b. WLAN konfigurieren
        c. Bose Konto trennen
  [2] SD Karte vorbereiten
        a. Karte bestuecken (alles drauf)
        b. Karte aktualisieren (nur neueste Files)
  [3] Bootstrap installieren
        a. Phase 1 aktivieren (rc.local direkt)
        b. Phase 2 aktivieren (Shepherd Integration)
        c. Bootstrap entfernen
  [4] Presets verwalten
        a. Radiosender auswaehlen und auf Tasten 1-6 legen
        b. Spotify Playlist als Preset
        c. Eigene URL als Preset
  [5] Status anzeigen
        a. Agent Health Check
        b. Spy Log einsehen
        c. Box Logs anzeigen
        d. Hosts Datei Status
  [6] Update Binary
        a. GitHub Release pruefen
        b. Update durchfuehren
  [7] Diagnose
        a. SSH Test
        b. Box Reachability
        c. DNS Aufloesung
  [8] Verlassen
```

### Phase 2: WPF GUI Anwendung (zukuenftig)

Selbe Funktionalitaet als grafische Anwendung. Tabs statt Menue. Live Status Anzeige mit Auto Refresh. Drag and Drop fuer Radiosender Auswahl.

Layout Mockup:

```
+------------------------------------------------------------+
| STR Setup                            [_] [#] [x]|
+------------------------------------------------------------+
| [Box]  [Karte]  [Bootstrap]  [Presets]  [Status]  [Update] |
+------------------------------------------------------------+
|                                                            |
|   Verbundene Box:  Bose-SM2-AABBCC.local (192.168.x.x)    |
|   Agent Version:   v0.0.4                                  |
|   Status:          ● Online, Spy aktiv                     |
|                                                            |
|   [Presets bearbeiten ...] [Logs einsehen ...]            |
|                                                            |
+------------------------------------------------------------+
```

Technologie Stack: PowerShell + WPF (Windows Presentation Foundation), kein .NET 6 Aufwand, kein Electron Bundle.

### Phase 3: Eigenstaendige .exe (langfristig)

Self-contained Installer mit Bundling der PowerShell Runtime und allen Skripten. Doppelklick installiert sich selbst, Start Menue Eintrag, kein Repo Auscheck noetig.

Optionen:
- **PS2EXE** kompiliert PS Skript zu .exe (einfach, fuer Konsole)
- **Inno Setup** baut .msi/.exe Installer mit allen Files
- **Squirrel.Windows** fuer Auto Updates

Distribution: jedes Release auf GitHub haengt zusaetzlich `STR-Setup-vX.Y.Z.exe` an.

## Architektur Prinzipien

### Trennung Logik und UI

- **Backend Skripte** in `setup/lib/*.ps1` fuer die eigentliche Logik (SSH, Stick Operations, Box Manipulation).
- **Frontend** ist austauschbar. Aktuell Console, spaeter WPF, spaeter standalone .exe.
- Beide nutzen die selben Backend Funktionen.

### Stateless Backend

Backend Funktionen geben Daten zurueck, schreiben nicht direkt auf den Bildschirm. UI Layer entscheidet wie Daten dargestellt werden.

Beispiel:
```powershell
# Backend
function Get-BoxStatus { return @{ ip=...; version=...; agentRunning=... } }

# Console UI
$st = Get-BoxStatus
Write-Host "IP: $($st.ip)"

# Spaeter WPF UI
$st = Get-BoxStatus
$Window.Find("IpLabel").Text = $st.ip
```

### Konfiguration

Settings unter `$env:APPDATA\STR\config.json`:

```json
{
  "lastBoxIP": "192.0.2.66",
  "lastStickDrive": "D:",
  "preferredArchitecture": "armv7l",
  "githubToken": null,
  "presetSources": {
    "1": { "name": "Deutschlandfunk", "url": "https://..." }
  }
}
```

Damit muss der User nicht jedes Mal IP und Stick Buchstabe neu eingeben.

## Module Map (Phase 1)

```
setup/
├── Install-STR.ps1   Hauptskript mit Menue
├── Run-Setup.cmd                 Doppelklick Launcher
├── UI-CONCEPT.md                 (diese Datei)
├── README.md                     User Doku
└── lib/
    ├── Common.ps1                Gemeinsame Helper (SSH Flags, Logging)
    ├── Box.ps1                   Box Discovery, SSH, Reboot
    ├── Stick.ps1                 Stick Listing, Bestueckung
    ├── Bootstrap.ps1             Bootstrap install und remove
    ├── Presets.ps1               Preset Editor und Sync
    ├── Status.ps1                Health Check, Logs
    └── Update.ps1                Binary Update von GitHub
```

Jedes Modul exportiert ein paar Funktionen. Das Hauptskript dot-sourced sie und ruft sie aus dem Menue auf.

## Stationen der UI

### Setup (Erstmaliges Aufsetzen)

1. Skript Doppelklick.
2. Menue.
3. User: "Box einrichten".
4. Wizard: "Steck die Box in den Strom. Halte Power 10 Sekunden gedrueckt um Werkszustand zu erzwingen. Wenn die Box blinkt, druecke ENTER hier."
5. User druckt.
6. Wizard: "Verbinde dich mit dem 'Bose ST 10' WLAN das die Box anbietet. Druecke ENTER wenn verbunden."
7. Wizard: "Welches WLAN soll die Box benutzen? Trage SSID und Passwort ein."
8. Wizard: API Call gegen Box `/addWirelessProfile` und `/setWifiRadio`.
9. Wizard: "Box verbindet sich. Etwa 30 Sekunden warten."
10. Wizard: Box wird im Heim WLAN gefunden.
11. Wizard: "SD Karte einlegen ..."

Dann weiter wie heute.

### Presets bearbeiten

```
Presets bearbeiten
==================

  Taste 1: Deutschlandfunk         https://st01.sslstream.dlf.de/...
  Taste 2: (leer)
  Taste 3: Bayern 3                http://streams.br.de/...
  Taste 4: (leer)
  Taste 5: Mein Spotify Mix        spotify:playlist:abc...
  Taste 6: (leer)

  [1-6] Taste bearbeiten
  [S]   Standardliste laden (Top Deutscher Radio)
  [I]   Import aus presets.json
  [E]   Export zu presets.json
  [Z]   Zurueck zum Hauptmenue
```

Eine Taste bearbeiten ruft einen kleinen Editor auf mit Name, Typ (Radio/Spotify/URL), URL/Identifier. Sync auf den Stick und auf die Box.

### Status

```
Status der Box
==============

  IP:                192.0.2.66
  Hostname:          rhino
  Online:            ja (seit 2:14 Stunden)
  Agent PID:         3421
  Agent Version:     v0.0.4
  Marge HTTP:        8080 OK, 1 Anfrage in den letzten 60s
  Marge HTTPS:       8443 OK, 0 Anfragen
  BMX:               8081 OK
  WebUI:             8888 OK
  Hosts Patch:       aktiv (4 Eintraege)
  iptables Redirect: aktiv (443 -> 8443, 80 -> 8080)
  Trust Store:       Root CA installiert, bind mount aktiv

  Letzte 10 Spy Eintraege:
    13:42:01 GET /v1/info from 127.0.0.1
    13:42:03 POST /v1/auth/...
    ...

  [L] Vollstaendige Logs ansehen
  [R] Status neu laden
  [Z] Zurueck
```

## Hinweise zu Phase 2 GUI

Wenn wir zu WPF migrieren:
- Behalte die Module in `lib/`.
- Neue UI Datei `STR.GUI.ps1` ueber den Modulen.
- Bestehende Konsolen UI bleibt parallel verfuegbar als Fallback / Headless Modus.

Damit ist die GUI Implementation eine reine Frontend Aufgabe, das Backend bleibt unveraendert.
