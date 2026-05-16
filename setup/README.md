# STR Setup Wizard

Ein Klick Setup um eine Bose SoundTouch Box mit dem STR auszustatten. Funktioniert ohne Kommandozeilen Eingaben.

## Was du brauchst

- Bose SoundTouch Box im gleichen WLAN wie der Laptop
- Eine FAT32 microSD Karte (mindestens 128 MB) in einem USB Slot des Laptops
- Windows mit eingebautem OpenSSH (Windows 10 und 11 haben das standardmaessig)

## Anwendung

1. Repo herunterladen oder auschecken.
2. In das `setup/` Verzeichnis gehen.
3. Doppelklick auf `Run-Setup.cmd`.
4. Anweisungen im Fenster folgen.

## Was der Wizard tut

1. **Stick auswaehlen**: zeigt verfuegbare SD Karten und USB Sticks an, du waehlst die richtige Nummer.
2. **Binary holen**: probiert in dieser Reihenfolge
   - Lokales `bin/streborn-armv7l` aus dem Repo Build
   - Latest GitHub Release Asset `streborn-armv7l`
3. **Karte bestuecken**: kopiert alle Dateien aus `usb-stick/` plus das Binary plus die `remote_services` Marker Datei auf die Karte.
4. **Karte auswerfen**: bereitet die sichere Entnahme vor.
5. **Pause**: du steckst die Karte in die Box, rebootest die Box und kommst zurueck.
6. **Bootstrap installieren**: SSH login, `install.sh install` ausfuehren.
7. **Box rebooten**: damit der Agent automatisch startet.
8. **Health Check**: prueft ob Agent laeuft, Root CA generiert wurde und WebUI antwortet.

## Was wenn die Box nicht gefunden wird

Der Wizard versucht erst mDNS, dann ARP Cache. Wenn nichts gefunden wird, fragt er nach der IP. Du kannst die IP auch direkt mitgeben:

```powershell
.\Install-STR.ps1 -BoxIP 192.0.2.66
```

## Erweiterte Parameter

```powershell
.\Install-STR.ps1 `
    -BoxIP 192.0.2.66 `
    -StickDrive D `
    -BinaryPath C:\pfad\zu\binary `
    -SkipDownload `
    -NoReboot
```

- `-BoxIP`: IP der Box explizit setzen, ueberspringt Auto Discovery.
- `-StickDrive`: Laufwerksbuchstabe des Sticks, z.B. `D`.
- `-BinaryPath`: eigenes Binary verwenden statt Auto Lookup.
- `-SkipDownload`: kein GitHub Download versuchen.
- `-NoReboot`: install.sh ausfuehren aber Box nicht rebooten.

## Phase 2 Aktivierung (shepherdd Integration)

Der Wizard installiert standardmaessig Phase 1 (rc.local direct). Wenn die Box ein paar Tage stabil laeuft kannst du auf Phase 2 (shepherdd Watchdog) wechseln. Per SSH auf der Box:

```sh
sh /media/sda1/install.sh shepherd install
reboot
```

Phase 1 wird dabei automatisch deaktiviert. Zurueckschalten geht analog mit `sh /media/sda1/install.sh install`.

## Wenn was schief geht

Der Wizard schreibt am Ende Fehler in rot. Plus den Stack Trace fuer die Diagnose. Plus Logs auf der Box findest du unter:

- `/mnt/nv/streborn/agent.log` Agent Logs
- `/mnt/nv/streborn/boot.log` rc.local Aktivitaet
- `/mnt/nv/streborn/run.out` run.sh Ausgabe

SSH Login fuer manuelle Inspektion (Box braucht die `remote_services` Datei auf der Karte):

```powershell
ssh -oHostKeyAlgorithms=+ssh-rsa -oPubkeyAcceptedAlgorithms=+ssh-rsa root@<box-ip>
```

## Sicherheitshinweis

Der Wizard erzeugt beim ersten Agent Start eine selbst signierte Root CA in `/mnt/nv/streborn/ca/`. Diese CA wird auf der Box in den System Trust Store eingehaengt (via bind mount). Damit kann der Agent HTTPS Anfragen der Box beantworten. Die Root CA verlaesst die Box nicht und ist nur fuer dein eigenes Geraet relevant.
