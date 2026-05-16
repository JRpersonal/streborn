# Status.ps1: Health Check, Logs anzeigen, Spy Log abfragen.

function Get-BoxStatus {
    param([Parameter(Mandatory)][string]$BoxIP)

    $status = @{
        ip = $BoxIP
        online = $false
        uptime = $null
        agentPid = $null
        agentRunning = $false
        margeHttp = $false
        margeHttps = $false
        bmx = $false
        webui = $false
        hostsApplied = $false
        caInstalled = $false
        iptablesActive = $false
    }

    if (-not (Test-BoxAtIP $BoxIP)) {
        return $status
    }
    $status.online = $true

    # Uptime
    $u = Invoke-BoxSSH $BoxIP "uptime"
    if ($u -match "up\s+([\w\s,:]+),\s+\d+\s+users") {
        $status.uptime = $matches[1].Trim()
    }

    # Agent PID
    $pidRaw = Invoke-BoxSSH $BoxIP "cat /mnt/nv/streborn/agent.pid 2>/dev/null"
    if ($pidRaw -and $pidRaw -match '^\d+$') {
        $status.agentPid = [int]$pidRaw
        $alive = Invoke-BoxSSH $BoxIP "kill -0 $($pidRaw) 2>/dev/null && echo YES || echo NO"
        $status.agentRunning = ($alive -match "YES")
    }

    # Listening Ports pruefen. Marge HTTPS laeuft direkt auf 443.
    $netstat = Invoke-BoxSSH $BoxIP "netstat -ltn 2>/dev/null | grep -E ':9080|:443 |:8081|:8888'"
    $status.margeHttp  = ($netstat -match ":9080")
    $status.margeHttps = ($netstat -match ":443\s")
    $status.bmx        = ($netstat -match ":8081")
    $status.webui      = ($netstat -match ":8888")

    # Hosts Patch
    $hosts = Invoke-BoxSSH $BoxIP "grep 'streborn' /etc/hosts 2>/dev/null"
    $status.hostsApplied = ($hosts -match "streborn")

    # Root CA
    $ca = Invoke-BoxSSH $BoxIP "test -r /mnt/nv/streborn/ca/root.crt && echo YES"
    $status.caInstalled = ($ca -match "YES")

    # iptables
    $ipt = Invoke-BoxSSH $BoxIP "iptables -t nat -S 2>/dev/null | grep streborn-redirect"
    $status.iptablesActive = ($ipt -match "streborn-redirect")

    return $status
}

function Show-BoxStatus {
    param([Parameter(Mandatory)][string]$BoxIP)

    Write-Header "Status der Box"
    Write-Info "Hole Daten von $BoxIP"
    $st = Get-BoxStatus $BoxIP

    function ynLabel($b) { if ($b) { "JA" } else { "nein" } }
    function color($b) { if ($b) { "Green" } else { "DarkGray" } }

    Write-Host ""
    Write-Host ("  IP             : {0}" -f $st.ip)
    Write-Host ("  Online         : {0}" -f (ynLabel $st.online)) -ForegroundColor (color $st.online)
    if ($st.uptime)   { Write-Host ("  Uptime         : {0}" -f $st.uptime) }
    if ($st.agentPid) { Write-Host ("  Agent PID      : {0}" -f $st.agentPid) }
    Write-Host ("  Agent laeuft   : {0}" -f (ynLabel $st.agentRunning)) -ForegroundColor (color $st.agentRunning)
    Write-Host ("  Marge HTTP     : {0}" -f (ynLabel $st.margeHttp))   -ForegroundColor (color $st.margeHttp)
    Write-Host ("  Marge HTTPS    : {0}" -f (ynLabel $st.margeHttps))  -ForegroundColor (color $st.margeHttps)
    Write-Host ("  BMX            : {0}" -f (ynLabel $st.bmx))         -ForegroundColor (color $st.bmx)
    Write-Host ("  WebUI          : {0}" -f (ynLabel $st.webui))       -ForegroundColor (color $st.webui)
    Write-Host ("  hosts Patch    : {0}" -f (ynLabel $st.hostsApplied))-ForegroundColor (color $st.hostsApplied)
    Write-Host ("  Root CA        : {0}" -f (ynLabel $st.caInstalled)) -ForegroundColor (color $st.caInstalled)
    Write-Host ("  iptables       : {0}" -f (ynLabel $st.iptablesActive))-ForegroundColor (color $st.iptablesActive)
    Write-Host ""
}

function Show-SpyLog {
    param(
        [Parameter(Mandatory)][string]$BoxIP,
        [int]$Lines = 50
    )
    Write-Header "Marge Spy Log"
    Write-Info "Hole letzte $Lines Zeilen via /__spy/log"
    try {
        $url = "http://$BoxIP`:9080/__spy/log"
        $body = Invoke-WebRequest $url -UseBasicParsing -TimeoutSec 5 | Select-Object -ExpandProperty Content
        if (-not $body) {
            Write-Warn "Keine Spy Eintraege vorhanden"
            return
        }
        $arr = $body -split "`n"
        $tail = $arr | Select-Object -Last ($Lines + 1)
        Write-Host ($tail -join "`n") -ForegroundColor Gray
    } catch {
        Write-Fail "Spy Log nicht abrufbar: $_"
    }
}

function Show-AgentLog {
    param(
        [Parameter(Mandatory)][string]$BoxIP,
        [int]$Lines = 50
    )
    Write-Header "Agent Log auf der Box"
    Write-Info "Hole tail von /mnt/nv/streborn/agent.log"
    $out = Invoke-BoxSSH $BoxIP "tail -n $Lines /mnt/nv/streborn/agent.log 2>/dev/null"
    if ($out) {
        Write-Host $out -ForegroundColor Gray
    } else {
        Write-Warn "Kein Agent Log vorhanden"
    }
}

function Show-BootLog {
    param([Parameter(Mandatory)][string]$BoxIP)
    Write-Header "Boot Log auf der Box"
    $out = Invoke-BoxSSH $BoxIP "cat /mnt/nv/streborn/boot.log 2>/dev/null"
    if ($out) {
        Write-Host $out -ForegroundColor Gray
    } else {
        Write-Warn "Kein Boot Log vorhanden"
    }
}
