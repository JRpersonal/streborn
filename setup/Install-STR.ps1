# Install-STR.ps1
#
# Setup Wizard fuer den STR mit Menue Struktur.
# Modular aufgebaut, Module liegen in lib/.
#
# Aufruf via Doppelklick auf Run-Setup.cmd oder direkt:
#   pwsh -ExecutionPolicy Bypass -File Install-STR.ps1
#
# Optional direkter Modus (ohne Menue) ueber Parameter:
#   -DirectAction install   bestueckt Stick und installiert Bootstrap
#   -DirectAction status    zeigt Status, beendet
#   -DirectAction presets   oeffnet Preset Editor
#
# Voraussetzungen:
#   * Bose SoundTouch Box im selben WLAN
#   * FAT32 SD Karte in einem USB Slot
#   * OpenSSH (in Win10/11 eingebaut)

[CmdletBinding()]
param(
    [string]$BoxIP,
    [string]$StickDrive,
    [string]$BinaryPath,
    [ValidateSet("menu", "install", "status", "presets", "update", "uninstall")]
    [string]$DirectAction = "menu"
)

$ErrorActionPreference = "Stop"

# === Module laden ===
$libDir = Join-Path $PSScriptRoot "lib"
. (Join-Path $libDir "Common.ps1")
. (Join-Path $libDir "Stick.ps1")
. (Join-Path $libDir "Bootstrap.ps1")
. (Join-Path $libDir "Presets.ps1")
. (Join-Path $libDir "Status.ps1")

# === Menue ===

function Show-MainMenu {
    Clear-Host

    # Status Header sammeln
    $repoRoot = try { Find-RepoRoot } catch { $null }
    $localVer  = if ($repoRoot) { Get-LocalVersion -RepoRoot $repoRoot } else { "unbekannt" }
    $latestVer = Get-LatestReleaseTag
    $sticks    = Get-DetectedSticks
    $sttStick  = $sticks | Where-Object { $_.IsSoundTouch } | Select-Object -First 1
    $lastBox   = Get-CachedConfig "lastBoxIP"

    # Update Status berechnen
    $updateAvailable = $false
    $updateColor = "Green"
    $updateText = "aktuell"
    if ($latestVer -and $localVer) {
        $cmp = Compare-Versions $localVer $latestVer
        if ($cmp -lt 0) {
            $updateAvailable = $true
            $updateColor = "Yellow"
            $updateText = "Update auf $latestVer verfuegbar"
        }
    } elseif (-not $latestVer) {
        $updateColor = "DarkGray"
        $updateText = "GitHub nicht erreichbar"
    }

    # === Frame zeichnen ===
    $bar = "=" * 64
    Write-Host ""
    Write-Host "  $bar" -ForegroundColor Magenta
    Write-Host "   STR Setup                                  $localVer".PadRight(66) -ForegroundColor Magenta
    Write-Host "  $bar" -ForegroundColor Magenta
    Write-Host ""

    # Status Block
    Write-Host "   Status:" -ForegroundColor White
    Write-Host ""
    Write-Host ("     Lokale Version : {0}" -f $localVer)
    if ($latestVer) {
        Write-Host -NoNewline ("     GitHub Latest  : {0}  " -f $latestVer)
        Write-Host "($updateText)" -ForegroundColor $updateColor
    } else {
        Write-Host ("     GitHub Latest  : nicht erreichbar")
    }

    if ($sttStick) {
        Write-Host ("     Karte erkannt  : {0}\  Version: {1}" -f $sttStick.DrivePath, $sttStick.Version) -ForegroundColor Cyan
    } elseif ($sticks.Count -gt 0) {
        Write-Host ("     Karte erkannt  : {0} Stick(s), aber kein STR" -f $sticks.Count) -ForegroundColor DarkGray
    } else {
        Write-Host  "     Karte erkannt  : keine eingelegt" -ForegroundColor DarkGray
    }

    if ($lastBox) {
        Write-Host ("     Letzte Box     : {0}" -f $lastBox)
    }

    Write-Host ""
    Write-Host "  $bar" -ForegroundColor Magenta
    Write-Host ""

    # Aktions Block
    Write-Host "   Aktionen:" -ForegroundColor White
    Write-Host ""
    Write-Host "     [1] Neue Karte einrichten (formatiert + bestueckt)"
    Write-Host "     [2] Preset Tasten 1 bis 6 belegen"
    Write-Host "     [3] Box Status anzeigen"
    Write-Host "     [4] Logs einsehen (Spy Log, Agent Log)"
    if ($updateAvailable) {
        Write-Host "     [5] Karte aktualisieren (NEUE VERSION VERFUEGBAR)" -ForegroundColor Yellow
    } else {
        Write-Host "     [5] Karte aktualisieren (neueres Binary plus Skripte)"
    }
    Write-Host "     [6] Bootstrap entfernen"
    Write-Host "     [7] Phase 2 Aktivierung (shepherdd Integration)"
    Write-Host "     [8] Diagnose und Reachability Test"
    Write-Host ""
    Write-Host "     [9] Box vorbereiten (Werkszustand, WLAN)" -ForegroundColor DarkGray -NoNewline
    Write-Host "  IN VORBEREITUNG" -ForegroundColor DarkGray
    if ($updateAvailable) {
        Write-Host ""
        Write-Host "     [U] Wizard aktualisieren (git pull oder neueres Release)" -ForegroundColor Yellow
    }
    Write-Host ""
    Write-Host "     [Q] Verlassen"
    Write-Host ""
    Write-Host "  $bar" -ForegroundColor Magenta
    Write-Host ""
}

function Run-MainMenu {
    while ($true) {
        Show-MainMenu
        $choice = Read-Host "  Wahl"
        try {
            switch ($choice.ToUpper()) {
                "1" { Action-Setup; Pause-User }
                "2" { Action-Presets; Pause-User }
                "3" { Action-Status; Pause-User }
                "4" { Action-Logs; Pause-User }
                "5" { Action-UpdateStick; Pause-User }
                "6" { Action-RemoveBootstrap; Pause-User }
                "7" { Action-Phase2; Pause-User }
                "8" { Action-Diagnose; Pause-User }
                "9" { Action-BoxPrepareStub; Pause-User }
                "U" { Action-UpdateWizard; Pause-User }
                "Q" {
                    Write-Host ""
                    Write-Host "  Auf Wiedersehen." -ForegroundColor Magenta
                    Write-Host ""
                    return
                }
                default {
                    Write-Warn "Unbekannte Eingabe"
                    Start-Sleep -Seconds 1
                }
            }
        } catch {
            Write-Fail "Fehler bei Aktion: $_"
            Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
            Pause-User
        }
    }
}

# === Aktionen ===

function Action-Setup {
    Write-Header "Erstmaliges Setup"
    Assert-Admin

    $repoRoot = Find-RepoRoot
    $stick = Select-Stick -PreferredDrive $StickDrive

    # Karte pruefen: ist sie schon FAT32? Dann formatieren wir nicht erneut.
    # Nur wenn das Dateisystem anders ist (NTFS, exFAT, RAW), wird formatiert.
    $letter = $stick.TrimEnd('\').TrimEnd(':')
    $vol = Get-Volume -DriveLetter $letter -ErrorAction SilentlyContinue
    if ($vol -and $vol.FileSystem -eq "FAT32") {
        Write-Info "Karte ist FAT32 (Label '$($vol.FileSystemLabel)', $([math]::Round($vol.Size/1GB,1)) GB), Format uebersprungen"
    } else {
        $actual = if ($vol) { $vol.FileSystem } else { "unbekannt" }
        Write-Warn "Karte ist nicht FAT32 (aktuell: $actual). Box braucht FAT32, formatiere jetzt."
        $formatted = Format-Stick -Drive $stick
        if (-not $formatted) {
            Write-Warn "Setup ohne Formatierung fortsetzen ist nicht vorgesehen."
            Write-Info "Falls du die Karte erhalten willst, nimm stattdessen Menue Punkt 5 (Karte aktualisieren)."
            return
        }
    }

    $bin   = Get-Binary -RepoRoot $repoRoot -CustomPath $BinaryPath
    Copy-StickFiles -StickPath $stick -RepoRoot $repoRoot -BinaryPath $bin

    Eject-Stick -StickPath $stick

    Write-Host ""
    Write-Host "  +================================================================+" -ForegroundColor Yellow
    Write-Host "  |                                                                |" -ForegroundColor Yellow
    Write-Host "  |  KARTE IST FERTIG BESTUECKT UND BEREIT                         |" -ForegroundColor Yellow
    Write-Host "  |                                                                |" -ForegroundColor Yellow
    Write-Host "  +================================================================+" -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Bitte fuehre jetzt folgende Schritte aus:" -ForegroundColor White
    Write-Host ""
    Write-Host "  Schritt 1:" -ForegroundColor Cyan -NoNewline
    Write-Host " Karte aus dem Kartenslot ziehen"
    Write-Host "             Wurde gerade ausgeworfen, du kannst sie sicher entnehmen."
    Write-Host ""
    Write-Host "  Schritt 2:" -ForegroundColor Cyan -NoNewline
    Write-Host " Karte in den USB Port der Bose SoundTouch stecken"
    Write-Host "             SoundTouch 10: Micro-USB Port unten an der Box."
    Write-Host "                            Du brauchst dafuer einen OTG-faehigen Stick"
    Write-Host "                            oder einen Micro-USB-OTG-Adapter."
    Write-Host "             SoundTouch 20 / 30: USB-A Port auf der Rueckseite."
    Write-Host "                                 Normaler USB-Stick passt direkt rein."
    Write-Host "             Karte komplett einstecken bis sie sitzt."
    Write-Host ""
    Write-Host "  Schritt 3:" -ForegroundColor Cyan -NoNewline
    Write-Host " Box vom Strom trennen"
    Write-Host "             Stecker direkt an der Box rausziehen (nicht nur Schalter)."
    Write-Host ""
    Write-Host "  Schritt 4:" -ForegroundColor Cyan -NoNewline
    Write-Host " 10 Sekunden warten"
    Write-Host ""
    Write-Host "  Schritt 5:" -ForegroundColor Cyan -NoNewline
    Write-Host " Box wieder an den Strom anschliessen"
    Write-Host ""
    Write-Host "  Schritt 6:" -ForegroundColor Cyan -NoNewline
    Write-Host " 60 bis 90 Sekunden warten"
    Write-Host "             Die Box bootet und verbindet sich mit dem WLAN."
    Write-Host "             Du erkennst es daran dass die LEDs an der Box aufhoeren"
    Write-Host "             zu blinken und ruhig leuchten."
    Write-Host ""
    Write-Host "  Wichtig:" -ForegroundColor Yellow -NoNewline
    Write-Host " Lass dieses Fenster bitte offen waehrend du das machst."
    Write-Host "           Wir brauchen es gleich um die Box fertig einzurichten."
    Write-Host ""
    Write-Host "  +================================================================+" -ForegroundColor Yellow
    Write-Host ""
    Pause-User "  Wenn alle 6 Schritte gemacht und die Box im WLAN ist, druecke hier ENTER"

    $box = Find-Box -PreferredIP $BoxIP
    Set-CachedConfig "lastBoxIP" $box
    Install-Bootstrap -BoxIP $box -Mode "phase1"
    Reboot-Box -BoxIP $box

    Write-Host ""
    Write-OK "Setup abgeschlossen"
    Show-BoxStatus -BoxIP $box
    Write-Host "  Browser: http://$box`:8888/   (Web UI)"
    Write-Host "  Spy Log: ueber Menue Punkt 4"
}

function Action-Presets {
    Write-Header "Preset Tasten belegen"

    # Erst pruefen ob eine SD Karte mit STR lokal verfuegbar ist
    $sticks = Get-DetectedSticks | Where-Object { $_.IsSoundTouch }
    if ($sticks.Count -gt 0) {
        Write-Info "Karte mit STR lokal gefunden, editiere direkt"
        $stick = Select-Stick -PreferredDrive (Get-CachedConfig "lastStickDrive")
        Open-PresetEditor -StickPath $stick
        Write-Host ""
        if (Read-YesNo "  Karte jetzt auswerfen? / Eject the card now?") {
            Eject-Stick -StickPath $stick
        }
        return
    }

    # Keine Karte lokal - editiere via SSH auf der Box
    Write-Info "Keine Karte im Laptop gefunden"
    Write-Info "Editiere presets.json direkt auf der Box per SSH"

    $box = Find-Box -PreferredIP $BoxIP
    Set-CachedConfig "lastBoxIP" $box

    # presets.json in temp Verzeichnis holen
    $tmpDir = Join-Path $env:TEMP "streborn-preset-edit"
    if (Test-Path $tmpDir) { Remove-Item $tmpDir -Recurse -Force }
    New-Item -ItemType Directory -Path $tmpDir | Out-Null

    $localFile = Join-Path $tmpDir "presets.json"
    $content = Invoke-BoxSSH $box "cat /media/sda1/presets.json 2>/dev/null"
    if (-not $content) {
        Write-Warn "presets.json auf Karte leer, starte mit leerer Liste"
        '{"presets":[]}' | Set-Content -Path $localFile -Encoding UTF8
    } else {
        $content | Set-Content -Path $localFile -Encoding UTF8
    }
    Write-OK "presets.json von der Box geholt"

    # Editor oeffnen (nutzt tmpDir als "Stick Path")
    Open-PresetEditor -StickPath $tmpDir

    # Zurueckschieben
    Write-Info "Schreibe presets.json zurueck auf die Karte"
    $newContent = Get-Content $localFile -Raw
    $newContent | ssh @SshFlags "root@$box" "cat > /media/sda1/presets.json"
    Write-OK "presets.json zurueck auf die Box"

    Write-Host ""
    Write-Host "  Damit der Agent die neuen Presets nutzt, muss er neu starten."
    if (Read-YesNo "  Agent jetzt neu starten? / Restart the agent now?") {
        Restart-AgentOnBox -BoxIP $box
    } else {
        Write-Info "Agent laeuft weiter mit alten Presets. Bei naechstem Reboot werden neue geladen."
    }

    Remove-Item $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}

function Action-Status {
    $box = Find-Box -PreferredIP $BoxIP
    Set-CachedConfig "lastBoxIP" $box
    Show-BoxStatus -BoxIP $box
}

function Action-Logs {
    $box = Find-Box -PreferredIP $BoxIP
    Set-CachedConfig "lastBoxIP" $box
    while ($true) {
        Write-Host ""
        Write-Host "  [1] Marge Spy Log (letzte 50 Eintraege)"
        Write-Host "  [2] Agent Log"
        Write-Host "  [3] Boot Log"
        Write-Host "  [Z] Zurueck zum Hauptmenue"
        Write-Host ""
        $choice = Read-Host "  Wahl"
        switch ($choice.ToUpper()) {
            "1" { Show-SpyLog   -BoxIP $box }
            "2" { Show-AgentLog -BoxIP $box }
            "3" { Show-BootLog  -BoxIP $box }
            "Z" { return }
            default { Write-Warn "Unbekannte Eingabe"; Start-Sleep -Seconds 1 }
        }
    }
}

function Action-UpdateStick {
    Write-Header "Karte aktualisieren"
    $repoRoot = Find-RepoRoot

    # Erst pruefen ob Karte lokal verfuegbar
    $sticks = Get-DetectedSticks | Where-Object { $_.IsSoundTouch }
    if ($sticks.Count -gt 0) {
        Write-Info "Karte lokal gefunden, aktualisiere direkt"
        $stick = Select-Stick -PreferredDrive (Get-CachedConfig "lastStickDrive")
        $bin = Get-Binary -RepoRoot $repoRoot -CustomPath $BinaryPath
        Update-StickFiles -StickPath $stick -RepoRoot $repoRoot -BinaryPath $bin
        if (Read-YesNo "  Karte jetzt auswerfen? / Eject the card now?") {
            Eject-Stick -StickPath $stick
        }
        return
    }

    # Keine Karte lokal: via SSH direkt auf die Box Karte schieben
    Write-Info "Keine Karte im Laptop. Aktualisiere ueber SSH direkt auf der Box."
    $box = Find-Box -PreferredIP $BoxIP
    Set-CachedConfig "lastBoxIP" $box

    $bin = Get-Binary -RepoRoot $repoRoot -CustomPath $BinaryPath
    $usbStickDir = Join-Path $repoRoot "usb-stick"

    Write-Step "Uebertrage neue Skripte auf die Karte"
    foreach ($file in (Get-ChildItem $usbStickDir -File)) {
        $name = $file.Name
        Write-Info "kopiere $name"
        Get-Content $file.FullName -Raw | ssh @SshFlags "root@$box" "cat > /media/sda1/$name"
    }

    Write-Step "Uebertrage neues Binary"
    Write-Info "Binary Groesse: $([math]::Round((Get-Item $bin).Length / 1KB)) KB"
    # SCP fuer Binary (binary safe via cat ginge auch, scp ist sicherer)
    & scp @SshFlags $bin "root@$box`:/media/sda1/streborn-armv7l"
    if ($LASTEXITCODE -ne 0) {
        Write-Warn "scp Fehler, versuche fallback ueber base64"
        $b64 = [Convert]::ToBase64String([System.IO.File]::ReadAllBytes($bin))
        $b64 | ssh @SshFlags "root@$box" "base64 -d > /media/sda1/streborn-armv7l && chmod +x /media/sda1/streborn-armv7l"
    } else {
        Invoke-BoxSSH $box "chmod +x /media/sda1/streborn-armv7l" | Out-Null
    }

    Write-OK "Files uebertragen"

    if (Read-YesNo "  Agent jetzt neu starten? / Restart the agent now?") {
        Restart-AgentOnBox -BoxIP $box
        Start-Sleep -Seconds 5
        Show-BoxStatus -BoxIP $box
    }
}

function Action-RemoveBootstrap {
    Write-Header "Bootstrap entfernen"
    $box = Find-Box -PreferredIP $BoxIP
    Remove-Bootstrap -BoxIP $box
    Write-Host ""
    if (Read-YesNo "  Box jetzt rebooten? / Reboot the box now?") {
        Reboot-Box -BoxIP $box
    }
}

function Action-Phase2 {
    Write-Header "Phase 2 Aktivierung (Shepherd Integration)"
    Write-Host "  Phase 2 integriert den Agent in den Bose shepherdd Watchdog."
    Write-Host "  Vorteil: automatischer Neustart bei Crash."
    Write-Host "  Phase 1 wird dabei automatisch deaktiviert."
    Write-Host ""
    if (-not (Read-YesNo "  Fortfahren? / Continue?")) { return }

    $box = Find-Box -PreferredIP $BoxIP
    Install-Bootstrap -BoxIP $box -Mode "phase2"
    if (Read-YesNo "  Box jetzt rebooten? / Reboot the box now?") {
        Reboot-Box -BoxIP $box
        Show-BoxStatus -BoxIP $box
    }
}

function Action-Diagnose {
    Write-Header "Diagnose"
    Write-Info "SSH Reachability"
    $box = Find-Box -PreferredIP $BoxIP
    Set-CachedConfig "lastBoxIP" $box

    Write-Info "DNS Tests auf der Box"
    $dns = Invoke-BoxSSH $box "nslookup streaming.bose.com 2>&1; echo ---; nslookup content.api.bose.io 2>&1"
    Write-Host $dns -ForegroundColor Gray

    Write-Info "Hosts Datei"
    $hosts = Invoke-BoxSSH $box "cat /etc/hosts"
    Write-Host $hosts -ForegroundColor Gray

    Write-Info "iptables NAT"
    $ipt = Invoke-BoxSSH $box "iptables -t nat -L -v -n --line-numbers 2>&1 | head -30"
    Write-Host $ipt -ForegroundColor Gray
}

function Action-UpdateWizard {
    Write-Header "Wizard selbst aktualisieren"
    $repoRoot = Find-RepoRoot
    $local = Get-LocalVersion -RepoRoot $repoRoot
    $latest = Get-LatestReleaseTag
    Write-Host ""
    Write-Host "  Lokal:  $local"
    Write-Host "  Online: $latest"
    Write-Host ""
    if (-not $latest) {
        Write-Fail "GitHub nicht erreichbar, kein Update Check moeglich"
        return
    }
    if ((Compare-Versions $local $latest) -ge 0) {
        Write-OK "Du bist bereits auf der neuesten Version"
        return
    }
    Write-Host "  Eine neuere Version ist verfuegbar." -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  [1] Per git pull aktualisieren (wenn Repo via git geklont)"
    Write-Host "  [2] Anleitung anzeigen wie ich es selbst mache"
    Write-Host "  [Z] Abbrechen"
    $choice = Read-Host "  Wahl"
    switch ($choice) {
        "1" {
            try {
                Push-Location $repoRoot
                $output = git pull 2>&1
                Pop-Location
                Write-Host $output -ForegroundColor Gray
                Write-OK "git pull abgeschlossen"
                Write-Host ""
                Write-Host "  Wichtig: schliesse den Wizard und starte ihn neu damit die Updates greifen." -ForegroundColor Yellow
                Write-Host "  Auch das Binary in bin/ muss neu gebaut werden falls du Stick aktualisieren willst:"
                Write-Host '    $env:GOOS="linux"; $env:GOARCH="arm"; $env:GOARM="7"; $env:CGO_ENABLED="0"'
                Write-Host "    go build -o bin/streborn-armv7l ./cmd/agent"
            } catch {
                Write-Fail "git pull fehlgeschlagen: $_"
            }
        }
        "2" {
            Write-Host ""
            Write-Host "  Manuelle Anleitung:"
            Write-Host "    1. Im Repo Verzeichnis: git pull"
            Write-Host "    2. Falls Binary neu gebaut werden soll:"
            Write-Host '       set GOOS=linux & set GOARCH=arm & set GOARM=7 & set CGO_ENABLED=0'
            Write-Host "       go build -o bin\streborn-armv7l .\cmd\agent"
            Write-Host "    3. Wizard schliessen und neu starten."
            Write-Host "    4. Im Wizard Menue Punkt 5 (Karte aktualisieren) waehlen."
            Write-Host ""
            Write-Host "  Alternative: latest Release Binary direkt von GitHub:"
            Write-Host "    https://github.com/JRpersonal/streborn/releases/latest"
        }
        default {
            Write-Info "Update abgebrochen"
        }
    }
}

function Action-BoxPrepareStub {
    Write-Header "Box vorbereiten"
    Write-Host ""
    Write-Host "  Diese Funktion ist in Vorbereitung." -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  Geplante Schritte:"
    Write-Host "    1. Werkszustand der Box pruefen (geschuetzt vor versehentlichem Reset)"
    Write-Host "    2. WLAN Profil auf der Box einrichten"
    Write-Host "    3. Bose Konto Bindung aufloesen"
    Write-Host ""
    Write-Host "  Heute musst du diese Schritte noch manuell via Bose App machen."
    Write-Host "  Sobald die Box im WLAN ist und im Werkszustand laeuft, kannst du mit Menue 1 weiter machen."
}

# === Direct Action Modus (ohne Menue) ===

function Run-DirectAction {
    switch ($DirectAction) {
        "install"   { Action-Setup }
        "status"    { Action-Status }
        "presets"   { Action-Presets }
        "update"    { Action-UpdateStick }
        "uninstall" { Action-RemoveBootstrap }
        default     { Write-Warn "Unbekannte DirectAction: $DirectAction" }
    }
}

# === Main ===

try {
    if ($DirectAction -ne "menu") {
        Run-DirectAction
    } else {
        Run-MainMenu
    }
} catch {
    Write-Host ""
    Write-Fail "Skript abgebrochen: $_"
    Write-Host ""
    Write-Host "Stack Trace zur Diagnose:" -ForegroundColor Gray
    Write-Host $_.ScriptStackTrace -ForegroundColor Gray
    exit 1
}
