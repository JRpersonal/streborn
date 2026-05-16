# Bootstrap.ps1: Installiert, entfernt und reboots den Bootstrap auf der Box.

function Install-Bootstrap {
    param(
        [Parameter(Mandatory)][string]$BoxIP,
        [ValidateSet("phase1", "phase2")][string]$Mode = "phase1"
    )

    Write-Step "Installiere Bootstrap auf der Box ($Mode)"

    Write-Info "Pruefe ob die Karte gemountet ist"
    $check = Invoke-BoxSSH $BoxIP "test -x /media/sda1/install.sh && echo READY || echo MISSING"
    if ($check -notmatch "READY") {
        throw "install.sh auf /media/sda1 nicht gefunden. Karte richtig in der Box? Box rebootet nach Stick einstecken?"
    }
    Write-OK "Karte gemountet"

    $arg = if ($Mode -eq "phase2") { "shepherd install" } else { "install" }
    Write-Info "Fuehre install.sh $arg aus"
    $output = Invoke-BoxSSH $BoxIP "sh /media/sda1/install.sh $arg 2>&1"
    Write-Host $output -ForegroundColor Gray
    Write-OK "Bootstrap installiert ($Mode)"
}

function Remove-Bootstrap {
    param([Parameter(Mandatory)][string]$BoxIP)
    Write-Step "Entferne Bootstrap"
    $output = Invoke-BoxSSH $BoxIP "sh /media/sda1/install.sh remove 2>&1; sh /media/sda1/install.sh shepherd remove 2>&1"
    Write-Host $output -ForegroundColor Gray
    Write-OK "Bootstrap entfernt"
}

function Reboot-Box {
    param(
        [Parameter(Mandatory)][string]$BoxIP,
        [int]$WaitSeconds = 120
    )
    Write-Step "Reboote Box"
    Invoke-BoxSSH $BoxIP "reboot" -Quiet
    Write-OK "Reboot Signal gesendet"

    Write-Info "Warte bis Box wieder erreichbar ist"
    $deadline = (Get-Date).AddSeconds($WaitSeconds)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 5
        if (Test-BoxAtIP $BoxIP) {
            Write-OK "Box wieder online"
            Start-Sleep -Seconds 10  # Agent Hochlauf abwarten
            return
        }
        Write-Host "    ..." -NoNewline
    }
    Write-Host ""
    Write-Warn "Box ist nach $WaitSeconds Sekunden noch nicht zurueck"
}
