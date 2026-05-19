# Common.ps1: Geteilte Hilfsfunktionen fuer alle Setup Module.
#
# Wird vom Install-STR.ps1 dot-sourced. Definiert SSH Flags,
# Logging Helpers, Box Discovery, Repo Root Lookup.

# === SSH Konfiguration ===
$script:SshFlags = @(
    "-oHostKeyAlgorithms=+ssh-rsa",
    "-oPubkeyAcceptedAlgorithms=+ssh-rsa",
    "-oStrictHostKeyChecking=accept-new"
)

# === Yes/No Prompt ===
#
# Read-YesNo accepts both English (y, yes) and German (j, ja) for yes,
# and (n, no, nein) for no. The wizard runs on Windows hosts of any
# locale; hard-coding "j" silently rejected every English user's "y"
# and aborted half-done stick builds (see GH #44).
function Read-YesNo {
    param(
        [Parameter(Mandatory)][string]$Prompt,
        [switch]$DefaultYes
    )
    $suffix = if ($DefaultYes) { " (Y/n) [yes]" } else { " (y/N) [no]" }
    $answer = Read-Host ($Prompt + $suffix)
    if ([string]::IsNullOrWhiteSpace($answer)) {
        return [bool]$DefaultYes
    }
    switch -Regex ($answer.Trim().ToLower()) {
        '^(y|yes|j|ja)$'  { return $true }
        '^(n|no|nein)$'   { return $false }
        default           { return [bool]$DefaultYes }
    }
}

# === Logging Helpers ===

function Write-Step($message) {
    Write-Host ""
    Write-Host "==> $message" -ForegroundColor Cyan
}

function Write-OK($message) {
    Write-Host "    OK $message" -ForegroundColor Green
}

function Write-Info($message) {
    Write-Host "    $message" -ForegroundColor Gray
}

function Write-Warn($message) {
    Write-Host "    WARN $message" -ForegroundColor Yellow
}

function Write-Fail($message) {
    Write-Host "    FEHLER $message" -ForegroundColor Red
}

function Write-Header($message) {
    Write-Host ""
    Write-Host "$message" -ForegroundColor Magenta
    Write-Host ("=" * $message.Length) -ForegroundColor Magenta
}

# === Repo Root ===

function Find-RepoRoot {
    $scriptDir = $PSScriptRoot
    if (-not $scriptDir) { $scriptDir = (Get-Location).Path }

    # Setup ist meist in repo\setup\lib\, also zwei Ebenen hoch
    $candidates = @(
        (Split-Path -Parent (Split-Path -Parent $scriptDir)),
        (Split-Path -Parent $scriptDir),
        $scriptDir
    )
    foreach ($c in $candidates) {
        if ($c -and (Test-Path (Join-Path $c "usb-stick"))) {
            return $c
        }
    }
    throw "Repo Root nicht gefunden (kein usb-stick Verzeichnis)"
}

# === SSH Funktionen ===

function Invoke-BoxSSH {
    param(
        [Parameter(Mandatory)][string]$BoxIP,
        [Parameter(Mandatory)][string]$Command,
        [switch]$Quiet
    )
    if ($Quiet) {
        return (ssh @SshFlags "root@$BoxIP" $Command 2>$null)
    }
    return (ssh @SshFlags "root@$BoxIP" $Command 2>&1)
}

function Test-BoxAtIP {
    param([string]$ip)
    if (-not $ip) { return $false }
    try {
        $result = ssh @SshFlags -oBatchMode=yes -oConnectTimeout=5 "root@$ip" "echo OK" 2>$null
        return ($result -eq "OK")
    } catch {
        return $false
    }
}

# === Box Discovery ===

function Find-Box {
    param([string]$PreferredIP)
    Write-Step "Suche Bose SoundTouch Box im Netzwerk"

    if ($PreferredIP) {
        Write-Info "Pruefe vorgegebene IP $PreferredIP"
        if (Test-BoxAtIP $PreferredIP) {
            Write-OK "Box erreichbar unter $PreferredIP"
            return $PreferredIP
        }
        Write-Warn "Box unter $PreferredIP nicht erreichbar"
    }

    # Aus letzter Session merken
    $cached = Get-CachedConfig "lastBoxIP"
    if ($cached -and $cached -ne $PreferredIP) {
        Write-Info "Pruefe letzte IP aus Cache $cached"
        if (Test-BoxAtIP $cached) {
            Write-OK "Box erreichbar unter $cached"
            return $cached
        }
    }

    # mDNS Lookup
    Write-Info "Versuche mDNS Aufloesung"
    foreach ($name in @("Bose-SM2.local", "Bose-SoundTouch.local")) {
        try {
            $r = Resolve-DnsName $name -Type A -ErrorAction Stop -QuickTimeout 2>$null
            if ($r -and $r.IPAddress) {
                $ip = $r[0].IPAddress
                Write-Info "$name aufgeloest zu $ip"
                if (Test-BoxAtIP $ip) {
                    Write-OK "Box erreichbar unter $ip"
                    return $ip
                }
            }
        } catch {}
    }

    # ARP Cache durchsuchen nach Bose MAC OUIs (a0:f6:fd, d0:b5:c2 fuer SM2 Familie)
    Write-Info "Suche im ARP Cache nach Bose Geraet"
    try {
        $arp = arp -a 2>&1 | Select-String -Pattern "a0-f6-fd|d0-b5-c2|04-a3-16|f4-5e-ab" -CaseSensitive:$false
        foreach ($line in $arp) {
            if ($line -match "(\d+\.\d+\.\d+\.\d+)") {
                $ip = $matches[1]
                Write-Info "Kandidat $ip"
                if (Test-BoxAtIP $ip) {
                    Write-OK "Box erreichbar unter $ip"
                    return $ip
                }
            }
        }
    } catch {
        Write-Warn "ARP Lookup fehlgeschlagen"
    }

    Write-Host ""
    Write-Host "    Box konnte nicht automatisch gefunden werden."
    Write-Host "    Bitte IP der Box manuell eingeben (z.B. 192.0.2.66):"
    $userIP = Read-Host "    IP"
    if (Test-BoxAtIP $userIP) {
        Write-OK "Box erreichbar unter $userIP"
        return $userIP
    }
    throw "Box unter $userIP nicht erreichbar"
}

# === Config Cache ===

$script:ConfigPath = Join-Path $env:APPDATA "STR\config.json"

function Get-CachedConfig {
    param([string]$Key)
    if (-not (Test-Path $script:ConfigPath)) { return $null }
    try {
        $cfg = Get-Content $script:ConfigPath -Raw | ConvertFrom-Json
        if ($cfg.PSObject.Properties.Name -contains $Key) {
            return $cfg.$Key
        }
    } catch {}
    return $null
}

function Set-CachedConfig {
    param([string]$Key, $Value)
    $dir = Split-Path -Parent $script:ConfigPath
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
    $cfg = @{}
    if (Test-Path $script:ConfigPath) {
        try {
            $loaded = Get-Content $script:ConfigPath -Raw | ConvertFrom-Json
            foreach ($p in $loaded.PSObject.Properties) {
                $cfg[$p.Name] = $p.Value
            }
        } catch {}
    }
    $cfg[$Key] = $Value
    $cfg | ConvertTo-Json -Depth 10 | Set-Content -Path $script:ConfigPath -Encoding UTF8
}

# === Versions Check ===

# Get-LatestReleaseTag holt den Tag Namen des neuesten GitHub Release.
# Liefert $null wenn offline oder API geblockt.
function Get-LatestReleaseTag {
    try {
        $api = "https://api.github.com/repos/JRpersonal/streborn/releases/latest"
        $r = Invoke-RestMethod $api -UseBasicParsing -TimeoutSec 5
        return $r.tag_name
    } catch {
        return $null
    }
}

# Get-LocalVersion liefert die Version des lokalen Repo Stands.
# Versucht erst git describe, faellt zurueck auf binary in bin/.
function Get-LocalVersion {
    param([string]$RepoRoot)
    try {
        Push-Location $RepoRoot
        $tag = git describe --tags --always 2>$null
        Pop-Location
        if ($tag) { return $tag.Trim() }
    } catch {}
    return $null
}

# Compare-Versions vergleicht semantische Versionen v0.0.3, v0.0.4 etc.
# Liefert -1 wenn $a aelter, 0 wenn gleich, +1 wenn $a neuer.
function Compare-Versions {
    param([string]$A, [string]$B)
    if ($A -eq $B) { return 0 }
    if (-not $A) { return -1 }
    if (-not $B) { return 1 }
    # v prefix entfernen plus dirty/sha Suffix
    $aNorm = ($A -replace "^v","" -replace "-.*$","" -split "\.")
    $bNorm = ($B -replace "^v","" -replace "-.*$","" -split "\.")
    for ($i = 0; $i -lt [Math]::Max($aNorm.Length, $bNorm.Length); $i++) {
        $aP = if ($i -lt $aNorm.Length) { [int]($aNorm[$i] -as [int]) } else { 0 }
        $bP = if ($i -lt $bNorm.Length) { [int]($bNorm[$i] -as [int]) } else { 0 }
        if ($aP -lt $bP) { return -1 }
        if ($aP -gt $bP) { return 1 }
    }
    return 0
}

# Show-VersionInfo zeigt aktuell installierte und latest available Version.
# Tut nichts wenn offline.
function Show-VersionInfo {
    param([string]$RepoRoot)
    $local = Get-LocalVersion -RepoRoot $RepoRoot
    $latest = Get-LatestReleaseTag
    Write-Host ""
    if ($local) {
        Write-Host ("  Lokale Version : {0}" -f $local) -ForegroundColor White
    }
    if ($latest) {
        Write-Host ("  Latest Release : {0}" -f $latest) -ForegroundColor White
        if ($local) {
            $cmp = Compare-Versions $local $latest
            if ($cmp -lt 0) {
                Write-Host "  Update verfuegbar! Nutze Menue Punkt 5 fuer Karten Aktualisierung." -ForegroundColor Yellow
            } elseif ($cmp -eq 0) {
                Write-Host "  Du bist auf dem neuesten Stand." -ForegroundColor Green
            } else {
                Write-Host "  Du laeufst lokal mit einer aktuelleren Version als das Release." -ForegroundColor DarkGray
            }
        }
    } else {
        Write-Host "  Latest Release : (GitHub nicht erreichbar)" -ForegroundColor DarkGray
    }
    Write-Host ""
}

# === Admin Check ===

function Test-IsAdmin {
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object System.Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Assert-Admin {
    if (-not (Test-IsAdmin)) {
        throw "Dieses Skript braucht Administratorrechte. Bitte ueber Run-Setup.cmd starten, das den UAC Dialog ausloesen wird."
    }
}

# === Pause ===

function Pause-User {
    param([string]$Message = "Druecke ENTER zum Fortfahren")
    Read-Host $Message | Out-Null
}
