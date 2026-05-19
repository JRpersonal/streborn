# Stick.ps1: SD Karte und USB Stick Operationen.

# Get-DetectedSticks scant alle removable Volumes und liefert eine Liste
# zurueck mit Info, ob es ein STR ist und welche Version.
function Get-DetectedSticks {
    $sticks = @()
    $volumes = Get-Volume |
        Where-Object { $_.DriveType -eq "Removable" -or ($_.Size -and $_.Size -lt 64GB -and $_.DriveLetter) }
    foreach ($v in $volumes) {
        if (-not $v.DriveLetter) { continue }
        $info = @{
            DriveLetter  = $v.DriveLetter
            DrivePath    = "$($v.DriveLetter):\"
            Label        = $v.FileSystemLabel
            FileSystem   = $v.FileSystem
            SizeMB       = [math]::Round($v.Size / 1MB, 0)
            IsSoundTouch = $false
            Version      = $null
            HasBinary    = $false
        }
        $stickPath = "$($v.DriveLetter):\"
        $versionFile = Join-Path $stickPath "version.txt"
        $binaryFile = Join-Path $stickPath "streborn-armv7l"
        $installFile = Join-Path $stickPath "install.sh"

        if (Test-Path $versionFile) {
            $info.Version = (Get-Content $versionFile -Raw -ErrorAction SilentlyContinue).Trim()
        }
        if (Test-Path $binaryFile) {
            $info.HasBinary = $true
        }
        if ((Test-Path $installFile) -and (Test-Path $binaryFile)) {
            $info.IsSoundTouch = $true
        }
        $sticks += [PSCustomObject]$info
    }
    return $sticks
}

function Format-Stick {
    param(
        [Parameter(Mandatory)][string]$Drive,
        [switch]$QuickFormat,
        [switch]$NoConfirm
    )

    $letter = $Drive.TrimEnd('\').TrimEnd(':')

    Write-Step "Formatiere Karte $letter`:"

    # Volume Info nochmal holen fuer Sicherheits Check
    $vol = Get-Volume -DriveLetter $letter -ErrorAction SilentlyContinue
    if (-not $vol) {
        throw "Laufwerk $letter`: nicht gefunden"
    }
    if ($vol.DriveType -ne "Removable" -and $vol.Size -gt 64GB) {
        throw "Laufwerk $letter`: ist nicht removable und groesser als 64GB - Sicherheits Stopp, um nicht versehentlich eine interne Festplatte zu formatieren"
    }

    $sizeGB = [math]::Round($vol.Size / 1GB, 1)
    $sizeMB = [math]::Round($vol.Size / 1MB, 0)
    Write-Info "Karte: $letter`: ($sizeGB GB, $($vol.FileSystem), Label '$($vol.FileSystemLabel)')"

    if (-not $NoConfirm) {
        Write-Host ""
        Write-Host "  ACHTUNG: ALLE DATEN AUF DER KARTE WERDEN GELOESCHT" -ForegroundColor Yellow
        Write-Host ""
        $confirm = Read-Host "  Tippe 'FORMAT' zum Bestaetigen (alles andere bricht ab)"
        if ($confirm -ne "FORMAT") {
            Write-Warn "Formatieren abgebrochen"
            return $false
        }
    }

    # Strategie: Bei <= 32GB nutze Format-Volume (Windows builtin).
    # Bei > 32GB: nutze Ridgecrop fat32format weil Windows FAT32 builtin
    # auf 32GB begrenzt ist.
    if ($vol.Size -le 32GB) {
        if ($QuickFormat) {
            Write-Info "Schnellformat (Format-Volume) laeuft"
        } else {
            Write-Info "Vollformat (Format-Volume) laeuft, dauert wenige Minuten"
        }
        try {
            $params = @{
                DriveLetter         = $letter
                FileSystem          = "FAT32"
                NewFileSystemLabel  = "SOUNDTOUCH"
                Force               = $true
                Confirm             = $false
            }
            if (-not $QuickFormat) { $params.Full = $true }
            Format-Volume @params | Out-Null
            Write-OK "Karte formatiert ($letter`: , FAT32, Label SOUNDTOUCH)"
            return $true
        } catch {
            Write-Fail "Format-Volume fehlgeschlagen: $_"
            Write-Info "Versuche Fallback auf fat32format Tool"
            # Faellt durch zum fat32format Block
        }
    } else {
        Write-Info "Karte ist groesser als 32GB, Format-Volume wuerde fehlschlagen"
        Write-Info "Nutze fat32format (umgeht Microsoft FAT32 Limit)"
    }

    # Fallback: fat32format
    return Invoke-Fat32Format -DriveLetter $letter -QuickFormat:$QuickFormat
}

# Invoke-Fat32Format nutzt das Ridgecrop fat32format CLI Tool. Falls nicht
# vorhanden, wird der User darauf hingewiesen.
function Invoke-Fat32Format {
    param(
        [Parameter(Mandatory)][string]$DriveLetter,
        [switch]$QuickFormat
    )

    $tool = Get-Fat32FormatPath
    if (-not $tool) {
        Write-Fail "fat32format Tool nicht gefunden"
        Show-Fat32FormatHelp
        return $false
    }

    Write-Info "Verwende: $tool"

    # fat32format Syntax:
    #   fat32format X:   (Schnell)
    #   fat32format -y X: (ohne Bestaetigung, sicher)
    # Manche Versionen brauchen ein Y per stdin.
    try {
        $args = @("-y", "$DriveLetter`:")
        $proc = Start-Process -FilePath $tool -ArgumentList $args -NoNewWindow -Wait -PassThru
        if ($proc.ExitCode -eq 0) {
            Write-OK "Karte formatiert via fat32format"
            # Label nachtraeglich setzen
            try {
                Set-Volume -DriveLetter $DriveLetter -NewFileSystemLabel "SOUNDTOUCH" -ErrorAction SilentlyContinue
            } catch {}
            return $true
        } else {
            Write-Fail "fat32format exit code $($proc.ExitCode)"
            return $false
        }
    } catch {
        Write-Fail "fat32format Fehler: $_"
        return $false
    }
}

# Get-Fat32FormatPath sucht das Tool in Standard Pfaden.
function Get-Fat32FormatPath {
    $candidates = @(
        (Join-Path $PSScriptRoot "..\tools\fat32format.exe"),
        (Join-Path $PSScriptRoot "..\..\tools\fat32format.exe"),
        "C:\Tools\fat32format.exe",
        "$env:LOCALAPPDATA\STR\fat32format.exe"
    )
    foreach ($c in $candidates) {
        try {
            $full = [System.IO.Path]::GetFullPath($c)
            if (Test-Path $full) { return $full }
        } catch {}
    }
    # PATH durchsuchen
    try {
        $found = Get-Command fat32format.exe -ErrorAction SilentlyContinue
        if ($found) { return $found.Source }
    } catch {}
    return $null
}

# Show-Fat32FormatHelp gibt dem User klare Anleitung wie fat32format
# zu beschaffen ist.
function Show-Fat32FormatHelp {
    Write-Host ""
    Write-Host "  fat32format ist ein kleines freies Tool von Ridgecrop um" -ForegroundColor Yellow
    Write-Host "  FAT32 auch auf Karten > 32GB zu formatieren. Windows kann das" -ForegroundColor Yellow
    Write-Host "  von Haus aus nicht." -ForegroundColor Yellow
    Write-Host ""
    Write-Host "  So besorgst du fat32format:"
    Write-Host "    1. Browser oeffnen: http://ridgecrop.co.uk/index.htm?fat32format.htm"
    Write-Host "    2. fat32format.exe herunterladen (CLI Version, ca. 50 KB)"
    Write-Host "    3. Datei nach $env:LOCALAPPDATA\STR\ kopieren"
    Write-Host "       oder einfach in das tools/ Verzeichnis dieses Repos legen"
    Write-Host "    4. Wizard neu starten"
    Write-Host ""
    Write-Host "  Alternative: einfach eine kleinere SD Karte (8 oder 16 GB) nutzen."
    Write-Host "  Die Box braucht weniger als 100 MB, jede kleine Karte reicht."
    Write-Host ""
}

function Select-Stick {
    param([string]$PreferredDrive)

    if ($PreferredDrive) {
        $drive = $PreferredDrive.TrimEnd('\').TrimEnd(':') + ":\"
        if (Test-Path $drive) {
            Write-OK "Vorgegebenes Laufwerk $drive benutzt"
            return $drive
        }
        Write-Warn "Vorgegebenes Laufwerk $drive nicht gefunden"
    }

    Write-Step "Waehle USB Stick / SD Karte"

    # Mit @() forcen wir Array auch bei einzelnem Treffer
    $candidates = @(Get-WmiObject Win32_LogicalDisk -Filter "DriveType=2 OR DriveType=3" |
        Where-Object { $_.DriveType -eq 2 -or ($_.Size -and ($_.Size -lt 64GB)) } |
        Select-Object DeviceID, VolumeName, FileSystem, Size, FreeSpace)

    if ($candidates.Count -eq 0) {
        Write-Warn "Keine SD Karte oder kleiner USB Stick im Laptop gefunden"
        Write-Info "Wenn die Karte gerade in der Box steckt, nutze stattdessen:"
        Write-Info "  Menue Punkt 5 (Karte aktualisieren) fuer Update ueber SSH"
        Write-Info "  Menue Punkt 2 (Preset Editor) fuer Preset Aenderung ueber SSH"
        throw "Keine SD Karten oder kleinen USB Sticks gefunden"
    }

    Write-Host "    Verfuegbare Laufwerke:"
    for ($i = 0; $i -lt $candidates.Count; $i++) {
        $c = $candidates[$i]
        $sizeMB = [math]::Round($c.Size / 1MB, 0)
        $label = if ($c.VolumeName) { $c.VolumeName } else { "(kein Label)" }
        Write-Host ("    [{0}] {1}\  {2}  {3} MB  {4}" -f $i, $c.DeviceID, $label, $sizeMB, $c.FileSystem)
    }
    Write-Host ""
    $idx = Read-Host "    Nummer waehlen (oder a fuer abbrechen)"
    if ($idx -eq "a" -or [string]::IsNullOrWhiteSpace($idx)) {
        throw "Vom User abgebrochen"
    }
    if (-not ($idx -match '^\d+$') -or [int]$idx -ge $candidates.Count) {
        throw "Ungueltige Auswahl: $idx"
    }
    $sel = $candidates[[int]$idx]
    $drive = "$($sel.DeviceID)\"

    if ($sel.FileSystem -ne "FAT32") {
        Write-Warn "Laufwerk ist $($sel.FileSystem), nicht FAT32"
        if (-not (Read-YesNo "    Trotzdem weitermachen? / Continue anyway?")) {
            throw "Vom User abgebrochen wegen Filesystem"
        }
    }

    Set-CachedConfig "lastStickDrive" $drive
    Write-OK "Verwende Laufwerk $drive"
    return $drive
}

function Get-Binary {
    param(
        [string]$RepoRoot,
        [string]$CustomPath,
        [switch]$SkipDownload
    )

    Write-Step "Beschaffe STR Binary"

    if ($CustomPath -and (Test-Path $CustomPath)) {
        Write-OK "Verwende vorgegebenes Binary: $CustomPath"
        return $CustomPath
    }

    $localBin = Join-Path $RepoRoot "bin\streborn-armv7l"
    if (Test-Path $localBin) {
        Write-OK "Verwende lokales Build: $localBin"
        return $localBin
    }

    if ($SkipDownload) {
        throw "Kein Binary gefunden und SkipDownload aktiv"
    }

    Write-Info "Hole latest Release von GitHub"
    try {
        $api = "https://api.github.com/repos/JRpersonal/streborn/releases/latest"
        $release = Invoke-RestMethod $api -UseBasicParsing
        $asset = $release.assets | Where-Object { $_.name -eq "streborn-armv7l" } | Select-Object -First 1
        if (-not $asset) {
            throw "Asset streborn-armv7l nicht im Release $($release.tag_name) gefunden"
        }
        $tempBin = Join-Path $env:TEMP "streborn-armv7l"
        Write-Info "Lade $($asset.name) ($([math]::Round($asset.size/1KB,0)) KB) von $($release.tag_name)"
        Invoke-WebRequest -Uri $asset.browser_download_url -OutFile $tempBin -UseBasicParsing
        Write-OK "Binary heruntergeladen nach $tempBin"
        return $tempBin
    } catch {
        throw "Konnte Binary nicht von GitHub holen: $_"
    }
}

function Copy-StickFiles {
    param(
        [Parameter(Mandatory)][string]$StickPath,
        [Parameter(Mandatory)][string]$RepoRoot,
        [Parameter(Mandatory)][string]$BinaryPath
    )

    Write-Step "Bestuecke SD Karte"

    # remote_services Marker fuer SSH
    "" | Set-Content -Path (Join-Path $StickPath "remote_services") -Encoding Ascii -NoNewline
    Write-OK "remote_services geschrieben"

    # Skripte aus usb-stick/
    $usbStickDir = Join-Path $RepoRoot "usb-stick"
    Get-ChildItem $usbStickDir -File | ForEach-Object {
        Copy-Item -Path $_.FullName -Destination $StickPath -Force
        Write-OK "$($_.Name) kopiert"
    }

    # Binary
    Copy-Item -Path $BinaryPath -Destination (Join-Path $StickPath "streborn-armv7l") -Force
    Write-OK "streborn-armv7l kopiert"

    # version.txt mit Setup Marker falls noch leer
    $versionFile = Join-Path $StickPath "version.txt"
    if (-not (Test-Path $versionFile) -or (Get-Content $versionFile -Raw).Trim() -eq "") {
        $today = Get-Date -Format "yyyy-MM-dd"
        "setup-$today" | Set-Content -Path $versionFile -Encoding Ascii -NoNewline
        Write-OK "version.txt mit Setup Marker geschrieben"
    }
}

function Update-StickFiles {
    param(
        [Parameter(Mandatory)][string]$StickPath,
        [Parameter(Mandatory)][string]$RepoRoot,
        [string]$BinaryPath
    )

    Write-Step "Aktualisiere SD Karte (nur veraenderte Files)"

    $usbStickDir = Join-Path $RepoRoot "usb-stick"
    $changed = 0
    Get-ChildItem $usbStickDir -File | ForEach-Object {
        $dst = Join-Path $StickPath $_.Name
        if (-not (Test-Path $dst) -or (Get-FileHash $_.FullName).Hash -ne (Get-FileHash $dst).Hash) {
            Copy-Item -Path $_.FullName -Destination $dst -Force
            Write-OK "$($_.Name) aktualisiert"
            $changed++
        }
    }

    if ($BinaryPath) {
        $dst = Join-Path $StickPath "streborn-armv7l"
        if (-not (Test-Path $dst) -or (Get-FileHash $BinaryPath).Hash -ne (Get-FileHash $dst).Hash) {
            Copy-Item -Path $BinaryPath -Destination $dst -Force
            Write-OK "streborn-armv7l aktualisiert"
            $changed++
        }
    }

    if ($changed -eq 0) {
        Write-OK "Alle Files schon aktuell"
    } else {
        Write-OK "$changed Files aktualisiert"
    }
}

function Eject-Stick {
    param([string]$StickPath)
    Write-Step "Sicheres Auswerfen"
    $letter = $StickPath.TrimEnd('\').TrimEnd(':')
    try {
        $shell = New-Object -ComObject Shell.Application
        $shell.Namespace(17).ParseName("$letter`:").InvokeVerb("Eject")
        Write-OK "Karte ausgeworfen"
    } catch {
        Write-Warn "Auswerfen fehlgeschlagen, bitte ueber System Tray manuell auswerfen"
    }
}
