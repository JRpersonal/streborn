# Diagnose-STR.ps1
#
# LAN diagnose helper for STR (SoundTouch Reborn). Collects everything
# needed to debug "no speakers found" issues without asking the user
# to run commands manually.
#
# What it does:
#   1. Detects the local IPv4 subnet(s) and your own IP.
#   2. Pings every host in the /24, plus probes the well-known ports
#      a SoundTouch speaker uses (22 SSH, 80 web, 8090 Bose API,
#      8091 UPnP/AVTransport, 8888 STR agent).
#   3. For each host that answers on port 8090, fetches /info to
#      identify the speaker (stock or STR-overlaid).
#   4. For each host that answers on port 8888, fetches /api/status
#      to confirm the STR agent is alive.
#   5. If Apple Bonjour / dns-sd is installed, browses for the mDNS
#      service names STR cares about.
#   6. Writes two files:
#        str-diag-full.json    -- complete result, KEEP PRIVATE
#        str-diag-public.json  -- safe to attach to a GitHub issue:
#                                 IPs, MACs, device IDs, serial
#                                 numbers, friendly names and SSIDs
#                                 are replaced by stable hashes.
#
# Usage:
#   pwsh -ExecutionPolicy Bypass -File tools\Diagnose-STR.ps1
#   pwsh -ExecutionPolicy Bypass -File tools\Diagnose-STR.ps1 -Subnet 192.168.1.
#   pwsh -ExecutionPolicy Bypass -File tools\Diagnose-STR.ps1 -OutputDir C:\Temp\str
#
# Privacy:
#   The public file is sanitized: IPs are masked, MACs and device
#   IDs are hashed, friendly names are not included. Even so, please
#   skim it before uploading. If something looks too identifying,
#   attach only the parts you are comfortable sharing.

[CmdletBinding()]
param(
    [string]$Subnet,
    [string]$OutputDir = "."
)

$ErrorActionPreference = "Stop"

# === Local subnet detection ===

function Get-LocalIPv4Bases {
    # Returns array of "192.168.x." style strings, one per IPv4
    # interface that has a private (RFC1918) address.
    $bases = @()
    $seen = @{}
    Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { $_.IPAddress -and $_.PrefixOrigin -ne "WellKnown" -and $_.IPAddress -ne "127.0.0.1" } |
        ForEach-Object {
            $ip = $_.IPAddress
            $parts = $ip.Split(".")
            if ($parts.Length -ne 4) { return }
            $a = [int]$parts[0]; $b = [int]$parts[1]
            $private = ($a -eq 10) -or
                       ($a -eq 172 -and $b -ge 16 -and $b -le 31) -or
                       ($a -eq 192 -and $b -eq 168)
            if (-not $private) { return }
            $base = "$($parts[0]).$($parts[1]).$($parts[2])."
            if (-not $seen.ContainsKey($base)) {
                $seen[$base] = $true
                $bases += [PSCustomObject]@{ Base = $base; SelfIP = $ip }
            }
        }
    return $bases
}

# === Port probe ===

function Test-Port {
    param([string]$Ip, [int]$Port, [int]$TimeoutMs = 400)
    try {
        $client = New-Object Net.Sockets.TcpClient
        $iar = $client.BeginConnect($Ip, $Port, $null, $null)
        $ok = $iar.AsyncWaitHandle.WaitOne($TimeoutMs, $false)
        if ($ok -and $client.Connected) {
            $client.EndConnect($iar) | Out-Null
            $client.Close()
            return $true
        }
        $client.Close()
    } catch {}
    return $false
}

function Test-PortBatch {
    # Fires all TCP connect attempts in parallel, waits TimeoutMs once,
    # then collects results. 254 hosts in ~one timeout window instead of
    # 254*timeout serial, turns a multi-minute sweep into a few seconds.
    param([string[]]$Ips, [int]$Port, [int]$TimeoutMs = 300)
    $entries = New-Object System.Collections.Generic.List[object]
    foreach ($ip in $Ips) {
        try {
            $c = New-Object Net.Sockets.TcpClient
            $iar = $c.BeginConnect($ip, $Port, $null, $null)
            $entries.Add([PSCustomObject]@{ Ip = $ip; Client = $c; Iar = $iar })
        } catch {}
    }
    Start-Sleep -Milliseconds $TimeoutMs
    $open = New-Object System.Collections.Generic.List[string]
    foreach ($e in $entries) {
        try {
            if ($e.Iar.IsCompleted -and $e.Client.Connected) {
                $open.Add($e.Ip)
                $e.Client.EndConnect($e.Iar) | Out-Null
            }
        } catch {}
        try { $e.Client.Close() } catch {}
    }
    return $open
}

function Invoke-SmallGet {
    param([string]$Url, [int]$TimeoutSec = 2)
    try {
        $resp = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec $TimeoutSec
        if ($resp.StatusCode -eq 200) {
            $content = $resp.Content
            if ($content.Length -gt 8192) { $content = $content.Substring(0, 8192) }
            return $content
        }
    } catch {}
    return $null
}

# === mDNS via dns-sd (Bonjour) ===

function Get-DnsSdBrowse {
    # dns-sd -B runs indefinitely until killed. Spawn it in a job
    # with a 3-second budget, capture whatever output it produced,
    # then kill it. Anything else (Select -First N, piping into a
    # filter) leaves the subprocess running and hangs the script.
    param([string]$Service)
    $tool = Get-Command dns-sd.exe -ErrorAction SilentlyContinue
    if (-not $tool) { return $null }
    $job = Start-Job -ScriptBlock { param($svc) & dns-sd -B $svc local. 2>$null } -ArgumentList $Service
    try {
        Wait-Job $job -Timeout 3 | Out-Null
        $output = Receive-Job $job -ErrorAction SilentlyContinue
        return @($output)
    } finally {
        Stop-Job  $job -ErrorAction SilentlyContinue | Out-Null
        Remove-Job $job -Force -ErrorAction SilentlyContinue | Out-Null
    }
}

# === Anonymization ===

function Get-StableHash {
    param([string]$Value)
    if ([string]::IsNullOrEmpty($Value)) { return "" }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Value)
    $hash = $sha.ComputeHash($bytes)
    $hex = ($hash | ForEach-Object { $_.ToString("x2") }) -join ""
    return $hex.Substring(0, 8)
}

function Get-PublicIp {
    param([string]$Ip)
    if ([string]::IsNullOrWhiteSpace($Ip)) { return $Ip }
    $parts = $Ip.Split(".")
    if ($parts.Length -ne 4) { return "IP#" + (Get-StableHash $Ip) }
    # Mask first three octets, keep last so two hits on the same host
    # in the same report are still recognizable as one device.
    return "192.0.2.$($parts[3])"
}

function ConvertTo-PublicHost {
    # Renamed parameter to $HostRec because $Host is a PowerShell
    # automatic variable (the current host UI) and cannot be a
    # function parameter.
    param($HostRec)
    $pub = [ordered]@{
        ip             = Get-PublicIp $HostRec.ip
        ports          = $HostRec.ports
        respondsToPing = $HostRec.respondsToPing
        boseInfo       = $null
        strAgent       = $null
    }
    if ($HostRec.boseInfo) {
        $pub.boseInfo = [ordered]@{
            type             = $HostRec.boseInfo.type
            firmware         = $HostRec.boseInfo.firmware
            countryCode      = $HostRec.boseInfo.countryCode
            regionCode       = $HostRec.boseInfo.regionCode
            variant          = $HostRec.boseInfo.variant
            moduleType       = $HostRec.boseInfo.moduleType
            deviceIDHash     = Get-StableHash $HostRec.boseInfo.deviceID
            margeAccountHash = Get-StableHash $HostRec.boseInfo.margeAccountUUID
            serialNumberHash = Get-StableHash $HostRec.boseInfo.serialNumber
            nameHash         = Get-StableHash $HostRec.boseInfo.name
            macHash          = Get-StableHash $HostRec.boseInfo.mac
        }
    }
    if ($HostRec.strAgent) {
        $pub.strAgent = [ordered]@{
            reachable     = $HostRec.strAgent.reachable
            statusExcerpt = $null
        }
        if ($HostRec.strAgent.statusBody) {
            $excerpt = $HostRec.strAgent.statusBody
            if ($excerpt.Length -gt 200) { $excerpt = $excerpt.Substring(0, 200) + "..." }
            $excerpt = [Regex]::Replace($excerpt, "\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b", "<ip>")
            $pub.strAgent.statusExcerpt = $excerpt
        }
    }
    return $pub
}

# === USB stick inspection ===
#
# "No speakers found" is often caused by a half-built stick rather
# than the LAN: missing binary, CRLF line endings on run.sh (BusyBox
# on the box silently refuses to exec CRLF scripts), wrong
# filesystem. Inspecting the stick locally tells us in one shot what
# would have failed on the box.

$ExpectedStickFiles = @(
    'run.sh',
    'autostart.sh',
    'install.sh',
    'iptables-setup.sh',
    'provision-wlan.sh',
    'rc.local',
    'setup-tls.sh',
    'update.sh',
    'streborn-armv7l',
    'version.txt'
)

function Get-LineEndingStyle {
    param([string]$Path)
    try {
        # Read a small head only. Line-ending bug is uniform across
        # the file in practice, and BusyBox barfs on the very first
        # CRLF on the shebang line.
        $bytes = [System.IO.File]::ReadAllBytes($Path)
        if ($bytes.Length -eq 0) { return "EMPTY" }
        $head = $bytes
        if ($head.Length -gt 4096) { $head = $head[0..4095] }
        $hasCR = $false
        $hasLF = $false
        $hasCRLF = $false
        for ($i = 0; $i -lt $head.Length; $i++) {
            if ($head[$i] -eq 0x0D) {
                $hasCR = $true
                if ($i + 1 -lt $head.Length -and $head[$i + 1] -eq 0x0A) {
                    $hasCRLF = $true
                }
            } elseif ($head[$i] -eq 0x0A) {
                if ($i -eq 0 -or $head[$i - 1] -ne 0x0D) {
                    $hasLF = $true
                }
            }
        }
        if ($hasCRLF -and -not $hasLF) { return "CRLF" }
        if ($hasLF  -and -not $hasCR)  { return "LF" }
        if ($hasCRLF -and $hasLF)      { return "MIXED" }
        if ($hasCR -and -not $hasLF -and -not $hasCRLF) { return "CR" }
        return "NONE"
    } catch {
        return "UNREADABLE"
    }
}

function Test-IsElfArm {
    param([string]$Path)
    try {
        $bytes = [System.IO.File]::ReadAllBytes($Path)
        if ($bytes.Length -lt 20) { return $false }
        # ELF magic: 0x7F 'E' 'L' 'F'
        if ($bytes[0] -ne 0x7F -or $bytes[1] -ne 0x45 -or $bytes[2] -ne 0x4C -or $bytes[3] -ne 0x46) {
            return $false
        }
        # e_machine at offset 18 (little-endian uint16). 0x28 = ARM.
        $machine = [BitConverter]::ToUInt16($bytes, 18)
        return ($machine -eq 0x28)
    } catch {
        return $false
    }
}

function Inspect-StickDrive {
    param([string]$Root, [string]$Filesystem, [int64]$SizeBytes, [string]$Label)

    $files = @()
    $present = @()
    $missing = @()
    $issues  = @()

    try {
        $items = Get-ChildItem -Path $Root -File -ErrorAction Stop
    } catch {
        return [PSCustomObject]@{
            drive            = $Root
            label            = $Label
            filesystem       = $Filesystem
            sizeBytes        = $SizeBytes
            readable         = $false
            error            = $_.Exception.Message
            files            = @()
            expectedPresent  = @()
            expectedMissing  = $ExpectedStickFiles
            issues           = @("drive not readable")
        }
    }

    $itemsByName = @{}
    foreach ($it in $items) { $itemsByName[$it.Name] = $it }

    foreach ($expected in $ExpectedStickFiles) {
        if ($itemsByName.ContainsKey($expected)) { $present += $expected }
        else { $missing += $expected }
    }

    foreach ($it in $items) {
        $entry = [ordered]@{
            name      = $it.Name
            size      = $it.Length
            modified  = $it.LastWriteTimeUtc.ToString("o")
            lineEndings = $null
            isElf     = $null
            elfArm    = $null
            content   = $null
        }
        $isShell = ($it.Extension -in @('.sh', '.local')) -or ($it.Name -in @('rc.local', 'autostart.sh'))
        if ($isShell) {
            $entry.lineEndings = Get-LineEndingStyle -Path $it.FullName
            if ($entry.lineEndings -in @('CRLF', 'MIXED', 'CR')) {
                $issues += "$($it.Name) has $($entry.lineEndings) line endings (BusyBox refuses to exec it)"
            }
            if ($entry.lineEndings -eq 'EMPTY') {
                $issues += "$($it.Name) is empty"
            }
        }
        if ($it.Name -eq 'streborn-armv7l') {
            $entry.isElf  = Test-IsElfArm -Path $it.FullName
            $entry.elfArm = $entry.isElf
            if (-not $entry.isElf) {
                $issues += "streborn-armv7l is not a valid ARM ELF binary (size $($it.Length) bytes)"
            } elseif ($it.Length -lt 1MB) {
                $issues += "streborn-armv7l suspiciously small ($($it.Length) bytes), likely the empty stub from a dev build"
            }
        }
        if ($it.Name -eq 'version.txt') {
            try {
                $entry.content = (Get-Content $it.FullName -Raw -ErrorAction Stop).Trim()
            } catch {}
        }
        $files += [PSCustomObject]$entry
    }

    # Filesystem must be FAT32. The box's BusyBox can only mount FAT32;
    # NTFS or exFAT sticks are invisible to it.
    if ($Filesystem -and $Filesystem -ne 'FAT32') {
        $issues += "filesystem is $Filesystem, the box can only mount FAT32"
    }

    return [PSCustomObject]@{
        drive            = $Root
        label            = $Label
        filesystem       = $Filesystem
        sizeBytes        = $SizeBytes
        readable         = $true
        files            = $files
        expectedPresent  = $present
        expectedMissing  = $missing
        issues           = $issues
    }
}

function Get-RemovableDrives {
    # Returns every removable / small fixed drive that has a drive
    # letter. We do not auto-detect "STR-ness" here, we want to
    # report on any stick the user has inserted so a wrong-stick
    # mix-up also surfaces.
    $out = @()
    Get-CimInstance -ClassName Win32_LogicalDisk -ErrorAction SilentlyContinue |
        Where-Object { $_.DeviceID -and ($_.DriveType -eq 2 -or ($_.Size -and $_.Size -lt 64GB)) } |
        ForEach-Object {
            $out += [PSCustomObject]@{
                Root       = "$($_.DeviceID)\"
                Label      = $_.VolumeName
                Filesystem = $_.FileSystem
                SizeBytes  = [int64]$_.Size
            }
        }
    return $out
}

function ConvertTo-PublicStick {
    # Public version: drop user-data file contents (presets.json,
    # remote_services markers etc) but keep filename + size + line
    # endings + ELF flags + version.txt content. Volume label is
    # hashed because users sometimes name sticks after themselves.
    param($Stick)
    $files = $Stick.files | ForEach-Object {
        $isSafeName = ($_.name -in $ExpectedStickFiles) -or ($_.name -like '*.sh')
        [ordered]@{
            name        = if ($isSafeName) { $_.name } else { "OTHER#" + (Get-StableHash $_.name) }
            size        = $_.size
            lineEndings = $_.lineEndings
            isElf       = $_.isElf
            elfArm      = $_.elfArm
            content     = if ($_.name -eq 'version.txt') { $_.content } else { $null }
        }
    }
    return [ordered]@{
        drive           = "REMOVABLE#" + (Get-StableHash $Stick.drive)
        labelHash       = Get-StableHash $Stick.label
        filesystem      = $Stick.filesystem
        sizeBytes       = $Stick.sizeBytes
        readable        = $Stick.readable
        files           = $files
        expectedPresent = $Stick.expectedPresent
        expectedMissing = $Stick.expectedMissing
        issues          = $Stick.issues
    }
}

# === Bose /info parser ===

function Parse-BoseInfo {
    param([string]$Xml)
    if (-not $Xml) { return $null }
    function _attr($s, $name) {
        $m = [Regex]::Match($s, "$name=`"([^`"]+)`"")
        if ($m.Success) { return $m.Groups[1].Value } else { return "" }
    }
    function _tag($s, $name) {
        $m = [Regex]::Match($s, "<$name[^>]*>([^<]+)</$name>")
        if ($m.Success) { return $m.Groups[1].Value } else { return "" }
    }
    $mac = ""
    $macMatch = [Regex]::Match($Xml, "<macAddress>([^<]+)</macAddress>")
    if ($macMatch.Success) { $mac = $macMatch.Groups[1].Value }
    return [PSCustomObject]@{
        deviceID         = _attr $Xml 'deviceID'
        name             = _tag  $Xml 'name'
        type             = _tag  $Xml 'type'
        margeAccountUUID = _tag  $Xml 'margeAccountUUID'
        countryCode      = _tag  $Xml 'countryCode'
        regionCode       = _tag  $Xml 'regionCode'
        variant          = _tag  $Xml 'variant'
        moduleType       = _tag  $Xml 'moduleType'
        firmware         = _tag  $Xml 'softwareVersion'
        serialNumber     = _tag  $Xml 'serialNumber'
        mac              = $mac
    }
}

# === Main ===

Write-Host ""
Write-Host "STR Diagnose" -ForegroundColor Magenta
Write-Host "============" -ForegroundColor Magenta
Write-Host ""

# Resolve subnet
$bases = if ($Subnet) {
    if ($Subnet -notmatch "\.$") { $Subnet = "$Subnet." }
    @([PSCustomObject]@{ Base = $Subnet; SelfIP = "" })
} else {
    Get-LocalIPv4Bases
}

if (-not $bases -or $bases.Count -eq 0) {
    Write-Host "ERROR: no local IPv4 subnet detected." -ForegroundColor Red
    Write-Host "Pass -Subnet 192.168.1. (the first three octets and a trailing dot) to scan manually." -ForegroundColor Red
    exit 1
}

Write-Host "Scanning subnet(s):" -ForegroundColor Cyan
foreach ($b in $bases) {
    if ($b.SelfIP) {
        Write-Host "  $($b.Base)1..254  (this machine is $($b.SelfIP))"
    } else {
        Write-Host "  $($b.Base)1..254"
    }
}
Write-Host ""

$hosts = New-Object 'System.Collections.Generic.List[object]'
$probePorts = @(22, 8090, 8091, 8888)

foreach ($entry in $bases) {
    $base = $entry.Base
    Write-Host "Probing $base (parallel)..." -ForegroundColor Cyan

    $allIps = 1..254 | ForEach-Object { "$base$_" }

    # One parallel batch per port. ~300ms per batch instead of
    # 254 serial probes per port. Total per subnet ~ 5 * 300ms.
    $portMap = @{}
    foreach ($p in $probePorts) {
        $open = Test-PortBatch -Ips $allIps -Port $p -TimeoutMs 300
        foreach ($ip in $open) {
            if (-not $portMap.ContainsKey($ip)) { $portMap[$ip] = @() }
            $portMap[$ip] += $p
        }
    }

    if ($portMap.Count -eq 0) {
        Write-Host "  no hosts answered on probed ports" -ForegroundColor DarkGray
        continue
    }

    foreach ($ip in ($portMap.Keys | Sort-Object {[int]($_ -split "\.")[-1]})) {
        $openPorts = $portMap[$ip]
        $hostEntry = [PSCustomObject]@{
            ip             = $ip
            respondsToPing = $null
            ports          = $openPorts
            boseInfo       = $null
            strAgent       = $null
        }

        if ($openPorts -contains 8090) {
            $xml = Invoke-SmallGet -Url "http://$ip`:8090/info" -TimeoutSec 2
            if ($xml) {
                $hostEntry.boseInfo = Parse-BoseInfo $xml
            }
        }
        if ($openPorts -contains 8888) {
            $body = Invoke-SmallGet -Url "http://$ip`:8888/api/status" -TimeoutSec 2
            $hostEntry.strAgent = [PSCustomObject]@{
                reachable  = [bool]$body
                statusBody = $body
            }
        }

        $hosts.Add($hostEntry)
        if ($hostEntry.boseInfo) {
            Write-Host ("  {0}  Bose {1}  fw {2}" -f $ip, $hostEntry.boseInfo.type, $hostEntry.boseInfo.firmware) -ForegroundColor Green
        } elseif ($hostEntry.strAgent -and $hostEntry.strAgent.reachable) {
            Write-Host ("  {0}  STR agent reachable on 8888" -f $ip) -ForegroundColor Green
        } else {
            Write-Host ("  {0}  ports: {1}" -f $ip, ($openPorts -join ",")) -ForegroundColor DarkGray
        }
    }
}

Write-Host ""
Write-Host "Inspecting removable drives (USB sticks)..." -ForegroundColor Cyan
$sticks = New-Object 'System.Collections.Generic.List[object]'
$drives = Get-RemovableDrives
if (-not $drives -or $drives.Count -eq 0) {
    Write-Host "  no removable drives plugged in" -ForegroundColor DarkGray
} else {
    foreach ($d in $drives) {
        Write-Host ("  {0}  label='{1}'  {2}  {3:N1} GB" -f $d.Root, $d.Label, $d.Filesystem, ($d.SizeBytes / 1GB))
        $inspected = Inspect-StickDrive -Root $d.Root -Filesystem $d.Filesystem -SizeBytes $d.SizeBytes -Label $d.Label
        $sticks.Add($inspected)
        if ($inspected.expectedPresent.Count -eq 0) {
            Write-Host "    (no STR files on this drive, probably not a Bose stick)" -ForegroundColor DarkGray
        } else {
            Write-Host ("    STR files present: {0}/{1}" -f $inspected.expectedPresent.Count, $ExpectedStickFiles.Count) -ForegroundColor Green
            if ($inspected.expectedMissing.Count -gt 0) {
                Write-Host ("    MISSING:  {0}" -f ($inspected.expectedMissing -join ", ")) -ForegroundColor Yellow
            }
            foreach ($issue in $inspected.issues) {
                Write-Host ("    ISSUE:    {0}" -f $issue) -ForegroundColor Red
            }
        }
    }
}

Write-Host ""
Write-Host "Querying mDNS (Bonjour) if available..." -ForegroundColor Cyan
$mdns = [ordered]@{
    "_streborn._tcp"          = Get-DnsSdBrowse "_streborn._tcp"
    "_soundtouchstick._tcp"   = Get-DnsSdBrowse "_soundtouchstick._tcp"
    "_soundtouch._tcp"        = Get-DnsSdBrowse "_soundtouch._tcp"
    "_bose-soundtouch._tcp"   = Get-DnsSdBrowse "_bose-soundtouch._tcp"
}
if (-not (Get-Command dns-sd.exe -ErrorAction SilentlyContinue)) {
    Write-Host "  dns-sd.exe not found (install Apple Bonjour Print Services to get mDNS data). Skipping." -ForegroundColor Yellow
}

# === Build output ===

$wizardVersion = "unknown"
$repoVersionFile = Join-Path $PSScriptRoot "..\VERSION"
if (Test-Path $repoVersionFile) {
    $wizardVersion = (Get-Content $repoVersionFile -Raw).Trim()
}

$full = [ordered]@{
    timestamp        = (Get-Date).ToString("o")
    diagnoseVersion  = "1"
    wizardVersion    = $wizardVersion
    osCaption        = (Get-CimInstance Win32_OperatingSystem).Caption
    osLocale         = (Get-Culture).Name
    osConsoleCodepage = [Console]::OutputEncoding.CodePage
    subnets          = $bases | ForEach-Object { $_.Base }
    selfIPs          = $bases | Where-Object { $_.SelfIP } | ForEach-Object { $_.SelfIP }
    mdns             = $mdns
    hosts            = $hosts
    sticks           = $sticks
}

$public = [ordered]@{
    timestamp        = $full.timestamp
    diagnoseVersion  = $full.diagnoseVersion
    wizardVersion    = $full.wizardVersion
    osCaption        = $full.osCaption
    osLocale         = $full.osLocale
    osConsoleCodepage = $full.osConsoleCodepage
    subnetCount      = ($full.subnets | Measure-Object).Count
    selfIPsCount     = ($full.selfIPs | Measure-Object).Count
    mdnsServiceTypesWithResults = $mdns.Keys | Where-Object { $mdns[$_] -and $mdns[$_].Count -gt 0 }
    mdnsServiceTypesEmpty       = $mdns.Keys | Where-Object { -not ($mdns[$_] -and $mdns[$_].Count -gt 0) }
    hosts            = $hosts  | ForEach-Object { ConvertTo-PublicHost  $_ }
    sticks           = $sticks | ForEach-Object { ConvertTo-PublicStick $_ }
}

$ts = Get-Date -Format "yyyyMMdd-HHmmss"
$fullPath   = Join-Path $OutputDir "str-diag-full-$ts.json"
$publicPath = Join-Path $OutputDir "str-diag-public-$ts.json"

$full   | ConvertTo-Json -Depth 10 | Set-Content -Path $fullPath   -Encoding UTF8
$public | ConvertTo-Json -Depth 10 | Set-Content -Path $publicPath -Encoding UTF8

Write-Host ""
Write-Host "Diagnose finished." -ForegroundColor Green
Write-Host ""
Write-Host "  Full (KEEP PRIVATE):  $fullPath" -ForegroundColor Yellow
Write-Host "  Public (safe):        $publicPath" -ForegroundColor Green
Write-Host ""
Write-Host "What to do with these:" -ForegroundColor White
Write-Host "  1. Attach the *-public.json file to the GitHub issue." -ForegroundColor White
Write-Host "     It contains hashed device IDs, masked IPs and no" -ForegroundColor White
Write-Host "     friendly names, so it is safe to share publicly." -ForegroundColor White
Write-Host "  2. Keep the *-full.json on your machine. Do not post" -ForegroundColor White
Write-Host "     it publicly. If more detail is requested in the" -ForegroundColor White
Write-Host "     issue thread, share only the specific fields asked for." -ForegroundColor White
Write-Host ""
