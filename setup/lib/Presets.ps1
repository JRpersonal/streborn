# Presets.ps1: Editor fuer die Preset Tasten (Radiosender, Spotify, URLs).
#
# Schema von presets.json auf dem USB Stick:
# {
#   "presets": [
#     { "id": 1, "name": "Deutschlandfunk", "type": "radio",
#       "url": "https://st01.sslstream.dlf.de/dlf/01/128/mp3/stream.mp3",
#       "art": "https://www.deutschlandradio.de/cdn/.../logo.png" },
#     ...
#   ]
# }
#
# Die Box hat physikalische Tasten 1 bis 6. Wir mappen presets[].id darauf.

# Liste der haeufigsten deutschen Radio Streams als Default Vorschlaege
$script:RadioPresets = @(
    @{ name = "Deutschlandfunk"; url = "https://st01.sslstream.dlf.de/dlf/01/128/mp3/stream.mp3" },
    @{ name = "Deutschlandfunk Kultur"; url = "https://st02.sslstream.dlf.de/dlf/02/128/mp3/stream.mp3" },
    @{ name = "Deutschlandfunk Nova"; url = "https://st03.sslstream.dlf.de/dlf/03/128/mp3/stream.mp3" },
    @{ name = "Bayern 1"; url = "http://streams.br.de/bayern1nb_2.m3u" },
    @{ name = "Bayern 2"; url = "http://streams.br.de/bayern2sued_2.m3u" },
    @{ name = "Bayern 3"; url = "http://streams.br.de/bayern3_2.m3u" },
    @{ name = "Antenne Bayern"; url = "https://stream.antenne.de/antenne" },
    @{ name = "WDR 2"; url = "https://wdr-wdr2-rheinland.icecastssl.wdr.de/wdr/wdr2/rheinland/mp3/128/stream.mp3" },
    @{ name = "1Live"; url = "https://wdr-1live-live.icecastssl.wdr.de/wdr/1live/live/mp3/128/stream.mp3" },
    @{ name = "NDR 2"; url = "https://icecast.ndr.de/ndr/ndr2/niedersachsen/mp3/128/stream.mp3" },
    @{ name = "NDR Info"; url = "https://icecast.ndr.de/ndr/ndrinfo/live/mp3/128/stream.mp3" },
    @{ name = "FluxFM"; url = "https://streams.fluxfm.de/Flux-Live/mp3-256/" },
    @{ name = "BBC Radio 1"; url = "http://stream.live.vc.bbcmedia.co.uk/bbc_radio_one" },
    @{ name = "BBC World Service"; url = "http://stream.live.vc.bbcmedia.co.uk/bbc_world_service" }
)

function Load-Presets {
    param([Parameter(Mandatory)][string]$StickPath)
    $file = Join-Path $StickPath "presets.json"
    if (-not (Test-Path $file)) {
        return @{ presets = @() }
    }
    try {
        $raw = Get-Content $file -Raw
        if (-not $raw -or $raw.Trim() -eq "") {
            return @{ presets = @() }
        }
        # ConvertFrom-Json gibt PSCustomObject zurueck, wir wollen Hashtable Struktur
        $obj = $raw | ConvertFrom-Json
        $list = @()
        if ($obj.presets) {
            foreach ($p in $obj.presets) {
                $list += @{ id = $p.id; name = $p.name; type = $p.type; url = $p.url; art = $p.art }
            }
        }
        return @{ presets = $list }
    } catch {
        Write-Warn "presets.json konnte nicht gelesen werden: $_"
        return @{ presets = @() }
    }
}

function Save-Presets {
    param(
        [Parameter(Mandatory)][string]$StickPath,
        [Parameter(Mandatory)][hashtable]$Data
    )
    $file = Join-Path $StickPath "presets.json"
    # Schoen formatieren
    $sorted = @($Data.presets | Sort-Object { [int]$_.id })
    $clean = @{ presets = $sorted }
    $json = $clean | ConvertTo-Json -Depth 10
    Set-Content -Path $file -Value $json -Encoding UTF8
    Write-OK "presets.json gespeichert ($($sorted.Count) Eintraege)"
}

function Show-PresetSummary {
    param([Parameter(Mandatory)][hashtable]$Data)

    $byId = @{}
    foreach ($p in $Data.presets) { $byId[[int]$p.id] = $p }

    Write-Host ""
    Write-Host "  Aktuelle Belegung der Preset Tasten:" -ForegroundColor Cyan
    Write-Host ""
    for ($i = 1; $i -le 6; $i++) {
        if ($byId.ContainsKey($i)) {
            $p = $byId[$i]
            $url = $p.url
            if ($url.Length -gt 50) { $url = $url.Substring(0, 47) + "..." }
            Write-Host ("    Taste {0}: {1}" -f $i, $p.name) -ForegroundColor White
            Write-Host ("              {0}" -f $url) -ForegroundColor DarkGray
        } else {
            Write-Host ("    Taste {0}: (leer)" -f $i) -ForegroundColor DarkGray
        }
    }
    Write-Host ""
}

function Edit-Preset {
    param(
        [Parameter(Mandatory)][hashtable]$Data,
        [Parameter(Mandatory)][int]$Slot
    )

    if ($Slot -lt 1 -or $Slot -gt 6) {
        Write-Warn "Slot muss zwischen 1 und 6 sein"
        return $Data
    }

    Write-Host ""
    Write-Host "  Preset Taste $Slot bearbeiten" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "  [V] Aus Vorschlags Liste waehlen (deutsche Radio Sender)"
    Write-Host "  [U] Eigene URL eintragen"
    Write-Host "  [L] Leeren"
    Write-Host "  [Z] Zurueck ohne Aenderung"
    $action = Read-Host "  Wahl"

    switch ($action.ToUpper()) {
        "V" {
            Write-Host ""
            for ($i = 0; $i -lt $script:RadioPresets.Count; $i++) {
                Write-Host ("    [{0}] {1}" -f $i, $script:RadioPresets[$i].name)
            }
            Write-Host ""
            $sel = Read-Host "  Nummer waehlen"
            if ($sel -match '^\d+$' -and [int]$sel -lt $script:RadioPresets.Count) {
                $r = $script:RadioPresets[[int]$sel]
                $Data = Set-Preset $Data $Slot $r.name "radio" $r.url $null
                Write-OK "Preset $Slot auf $($r.name) gesetzt"
            } else {
                Write-Warn "Ungueltige Auswahl, abgebrochen"
            }
        }
        "U" {
            $name = Read-Host "  Anzeigename"
            $url = Read-Host "  URL"
            $type = Read-Host "  Typ (radio/spotify/url, default radio)"
            if (-not $type) { $type = "radio" }
            $Data = Set-Preset $Data $Slot $name $type $url $null
            Write-OK "Preset $Slot manuell gesetzt"
        }
        "L" {
            $Data = Remove-Preset $Data $Slot
            Write-OK "Preset $Slot geleert"
        }
        "Z" {
            Write-Info "Keine Aenderung"
        }
        default {
            Write-Warn "Unbekannte Eingabe"
        }
    }
    return $Data
}

function Set-Preset {
    param([hashtable]$Data, [int]$Slot, [string]$Name, [string]$Type, [string]$Url, [string]$Art)
    $newList = @()
    $added = $false
    foreach ($p in $Data.presets) {
        if ([int]$p.id -eq $Slot) {
            $newList += @{ id = $Slot; name = $Name; type = $Type; url = $Url; art = $Art }
            $added = $true
        } else {
            $newList += $p
        }
    }
    if (-not $added) {
        $newList += @{ id = $Slot; name = $Name; type = $Type; url = $Url; art = $Art }
    }
    return @{ presets = $newList }
}

function Remove-Preset {
    param([hashtable]$Data, [int]$Slot)
    $newList = @($Data.presets | Where-Object { [int]$_.id -ne $Slot })
    return @{ presets = $newList }
}

# Read-PresetsFromBox holt die presets.json via SSH von der Box runter
# in ein temporaeres lokales File.
function Read-PresetsFromBox {
    param([Parameter(Mandatory)][string]$BoxIP)
    $tmp = Join-Path $env:TEMP "streborn-presets-$([guid]::NewGuid().ToString('N').Substring(0,8)).json"

    Write-Info "Lade presets.json von der Box"
    $content = Invoke-BoxSSH $BoxIP "cat /media/sda1/presets.json 2>/dev/null"
    if (-not $content) {
        Write-Warn "presets.json auf der Box leer oder nicht vorhanden, starte mit leerer Liste"
        '{"presets":[]}' | Set-Content -Path $tmp -Encoding UTF8
    } else {
        $content | Set-Content -Path $tmp -Encoding UTF8
    }
    return $tmp
}

# Write-PresetsToBox schreibt die geaenderte presets.json zurueck auf die Karte.
function Write-PresetsToBox {
    param(
        [Parameter(Mandatory)][string]$BoxIP,
        [Parameter(Mandatory)][string]$LocalFile
    )
    Write-Info "Schreibe presets.json zurueck auf die Karte"
    $content = Get-Content $LocalFile -Raw
    $content | ssh @SshFlags "root@$BoxIP" "cat > /media/sda1/presets.json"
    Write-OK "presets.json auf die Karte uebertragen"
}

# Restart-AgentOnBox killt den Agent. rc.local hat keinen Auto Restart, daher
# muessen wir entweder Box rebooten oder run.sh manuell aufrufen.
function Restart-AgentOnBox {
    param([Parameter(Mandatory)][string]$BoxIP)
    Write-Step "Agent neu starten damit Presets greifen"

    Write-Info "Beende alten Agent"
    Invoke-BoxSSH $BoxIP "pid=`$(cat /mnt/nv/streborn/agent.pid 2>/dev/null); if [ -n `"`$pid`" ]; then kill `$pid 2>/dev/null; sleep 2; kill -9 `$pid 2>/dev/null; fi; rm -f /mnt/nv/streborn/agent.pid"

    Write-Info "Starte run.sh erneut"
    Invoke-BoxSSH $BoxIP "nohup sh /media/sda1/run.sh > /tmp/run.out 2>&1 &"
    Start-Sleep -Seconds 5

    $check = Invoke-BoxSSH $BoxIP "cat /mnt/nv/streborn/agent.pid 2>/dev/null"
    if ($check -match '^\d+$') {
        Write-OK "Agent neu gestartet (PID $check)"
    } else {
        Write-Warn "Agent PID File noch nicht da, eventuell hat run.sh laenger gebraucht"
    }
}

function Open-PresetEditor {
    param([Parameter(Mandatory)][string]$StickPath)

    $data = Load-Presets $StickPath

    while ($true) {
        Clear-Host
        Write-Header "Preset Editor"
        Show-PresetSummary $data
        Write-Host "  (Aenderungen werden automatisch gespeichert)" -ForegroundColor DarkGray
        Write-Host ""
        Write-Host "  [1-6] Taste bearbeiten"
        Write-Host "  [S]   Standardliste laden (Top 6 deutsche Radio)"
        Write-Host "  [F]   Alles leeren"
        Write-Host "  [Z]   Zurueck (Aenderungen sind schon gespeichert)"
        Write-Host ""
        $choice = Read-Host "  Wahl"

        switch -Regex ($choice.ToUpper()) {
            "^[1-6]$" {
                $newData = Edit-Preset $data ([int]$choice)
                if ($newData -ne $data) {
                    $data = $newData
                    Save-Presets $StickPath $data
                    Start-Sleep -Milliseconds 700
                }
            }
            "^S$" {
                # Standardliste: Top 6 deutsche Radios
                $defaults = @(
                    @{ slot = 1; entry = $script:RadioPresets[0] },
                    @{ slot = 2; entry = $script:RadioPresets[5] },
                    @{ slot = 3; entry = $script:RadioPresets[7] },
                    @{ slot = 4; entry = $script:RadioPresets[8] },
                    @{ slot = 5; entry = $script:RadioPresets[9] },
                    @{ slot = 6; entry = $script:RadioPresets[11] }
                )
                foreach ($d in $defaults) {
                    $data = Set-Preset $data $d.slot $d.entry.name "radio" $d.entry.url $null
                }
                Save-Presets $StickPath $data
                Write-OK "Standardliste geladen und gespeichert"
                Start-Sleep -Milliseconds 800
            }
            "^F$" {
                $data = @{ presets = @() }
                Save-Presets $StickPath $data
                Write-OK "Alle Presets geloescht und gespeichert"
                Start-Sleep -Milliseconds 800
            }
            "^Z$" {
                return
            }
            default {
                Write-Warn "Unbekannte Eingabe"
                Start-Sleep -Seconds 1
            }
        }
    }
}
