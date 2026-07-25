<#
.SYNOPSIS
  Establish a SimplySign Desktop cloud session non-interactively, for CI.

.DESCRIPTION
  The Certum "Open Source" certificate's private key lives in the SimplySign
  cloud. signtool can only reach it while a SimplySign Desktop session is active,
  and opening that session normally requires a human to approve it in the
  SimplySign mobile app. To run in GitHub Actions without a human, we
  authenticate with a time-based one-time code (TOTP) derived from the base32
  secret that is embedded in the QR code shown during SimplySign enrolment
  (the `otpauth://` URI). Capture that secret once and store it as the
  SIMPLYSIGN_OTP_SECRET repository secret.

  Get-Totp below is a standard RFC 6238 implementation (HMAC-SHA1, 6 digits,
  30 s period) and is correct as written.

  SimplySign Desktop has no headless login CLI, so this script drives the GUI:
  it installs SimplySign Desktop silently from Certum's official MSI, launches
  it, focuses the connect window, types the user ID and a freshly derived TOTP
  via SendKeys, and then polls the CurrentUser\My store until the cloud
  smart-card mounts and the certificate appears. This is the documented
  automation route for SimplySign (there is no API): the same approach is used
  by the Inkdrop build pipeline (devas.life) and works on GitHub-hosted Windows
  runners because their build session is interactive enough for AppActivate +
  SendKeys.

  First run is verified via a release DRY-RUN (gh workflow run release.yml,
  no version input) once the three repository secrets are set.

.PARAMETER Token
  SimplySign user ID (the ID entered in SimplySign Desktop's connect dialog).
  Defaults to $env:SIMPLYSIGN_TOKEN.

.PARAMETER OtpSecret
  Base32 TOTP secret from the enrolment QR. Defaults to $env:SIMPLYSIGN_OTP_SECRET.

.PARAMETER Thumbprint
  Certificate SHA-1 thumbprint to wait for after login. Defaults to
  $env:CERT_THUMBPRINT. When empty, the wait accepts any new cert with a
  private key appearing in CurrentUser\My.
#>
param(
  [string]$Token      = $env:SIMPLYSIGN_TOKEN,
  [string]$OtpSecret  = $env:SIMPLYSIGN_OTP_SECRET,
  [string]$Thumbprint = $env:CERT_THUMBPRINT
)
$ErrorActionPreference = 'Stop'

function ConvertFrom-Base32 {
  param([Parameter(Mandatory = $true)][string]$Value)
  $alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  $clean = ($Value -replace '=', '').ToUpperInvariant()
  $bits = New-Object System.Text.StringBuilder
  foreach ($ch in $clean.ToCharArray()) {
    $idx = $alphabet.IndexOf($ch)
    if ($idx -lt 0) { continue }
    [void]$bits.Append([Convert]::ToString($idx, 2).PadLeft(5, '0'))
  }
  $bitStr = $bits.ToString()
  $bytes = New-Object System.Collections.Generic.List[byte]
  for ($i = 0; $i + 8 -le $bitStr.Length; $i += 8) {
    [void]$bytes.Add([Convert]::ToByte($bitStr.Substring($i, 8), 2))
  }
  return , $bytes.ToArray()
}

function Get-Totp {
  param(
    [Parameter(Mandatory = $true)][string]$Secret,
    [int]$Digits = 6,
    [int]$Period = 30
  )
  $key = ConvertFrom-Base32 -Value $Secret
  $counter = [int64][math]::Floor([DateTimeOffset]::UtcNow.ToUnixTimeSeconds() / $Period)
  $counterBytes = [BitConverter]::GetBytes($counter)
  if ([BitConverter]::IsLittleEndian) { [Array]::Reverse($counterBytes) }
  $hmac = New-Object System.Security.Cryptography.HMACSHA1
  try {
    $hmac.Key = $key
    $hash = $hmac.ComputeHash($counterBytes)
  } finally {
    $hmac.Dispose()
  }
  $offset = $hash[$hash.Length - 1] -band 0x0f
  $binary = ((($hash[$offset] -band 0x7f) -shl 24) -bor `
             (($hash[$offset + 1] -band 0xff) -shl 16) -bor `
             (($hash[$offset + 2] -band 0xff) -shl 8) -bor `
              ($hash[$offset + 3] -band 0xff))
  $otp = $binary % [int][math]::Pow(10, $Digits)
  return ([string]$otp).PadLeft($Digits, '0')
}

if (-not $OtpSecret) { throw "SIMPLYSIGN_OTP_SECRET is not set." }
if (-not $Token)     { throw "SIMPLYSIGN_TOKEN is not set." }

# Escape a literal string for WScript.Shell SendKeys: +^%~(){}[] are control
# characters there and must be wrapped in braces to arrive verbatim.
function ConvertTo-SendKeysLiteral {
  param([Parameter(Mandatory = $true)][string]$Value)
  return ($Value -replace '([+^%~(){}\[\]])', '{$1}')
}

# Seconds left in the current TOTP period. Deriving an OTP that expires while
# SendKeys is typing it produces a flaky one-in-six login failure, so the
# caller waits out the tail of a period instead of racing it.
function Get-TotpSecondsRemaining {
  param([int]$Period = 30)
  return $Period - ([DateTimeOffset]::UtcNow.ToUnixTimeSeconds() % $Period)
}

# --- 1. Install SimplySign Desktop (official Certum MSI, silent) -------------
# Version pinned; override via SIMPLYSIGN_MSI_URL when Certum ships a newer
# build. The MSI supports standard msiexec silent switches.
$msiUrl = $env:SIMPLYSIGN_MSI_URL
if (-not $msiUrl) {
  $msiUrl = 'https://files.certum.eu/software/SimplySignDesktop/Windows/9.4.4.92/SimplySignDesktop-9.4.4.92-64-bit-en.msi'
}

function Find-SimplySignExe {
  $roots = @($env:ProgramFiles, ${env:ProgramFiles(x86)}) | Where-Object { $_ }
  foreach ($root in $roots) {
    $hit = Get-ChildItem -Path $root -Recurse -Depth 3 -Filter '*.exe' -ErrorAction SilentlyContinue |
      Where-Object { $_.Name -match '^SimplySign.*Desktop.*\.exe$' -or ($_.DirectoryName -match 'SimplySign' -and $_.Name -match 'SimplySign') } |
      Select-Object -First 1
    if ($hit) { return $hit.FullName }
  }
  return $null
}

$ssd = Find-SimplySignExe
if ($ssd) {
  Write-Host "SimplySign Desktop already installed: $ssd"
} else {
  $msi = Join-Path $env:TEMP 'SimplySignDesktop.msi'
  Write-Host "Downloading SimplySign Desktop: $msiUrl"
  Invoke-WebRequest -Uri $msiUrl -OutFile $msi
  Write-Host ("Installer SHA256: {0}" -f (Get-FileHash $msi -Algorithm SHA256).Hash)
  $p = Start-Process msiexec.exe -ArgumentList '/i', "`"$msi`"", '/quiet', '/norestart' -Wait -PassThru
  if ($p.ExitCode -ne 0) { throw "msiexec install failed (exit $($p.ExitCode))" }
  $ssd = Find-SimplySignExe
  if (-not $ssd) { throw "SimplySign Desktop installed but no executable found under Program Files." }
  Write-Host "Installed: $ssd"
}

# --- 2. Launch and log in via SendKeys ---------------------------------------
# SimplySign Desktop is a tray app that opens its "Connect to SimplySign"
# dialog on first launch. Focus is acquired by process id first (most
# reliable), falling back to the window title. The dialog takes the user ID
# and the current OTP; field order userID -> TAB -> OTP -> ENTER.
$proc = Start-Process -FilePath $ssd -PassThru
Write-Host "SimplySign Desktop started (pid $($proc.Id)), waiting for the connect window"
Start-Sleep -Seconds 8

$wshell = New-Object -ComObject WScript.Shell
$focused = $false
for ($i = 0; $i -lt 20 -and -not $focused; $i++) {
  $focused = $wshell.AppActivate($proc.Id)
  if (-not $focused) { $focused = $wshell.AppActivate('SimplySign Desktop') }
  if (-not $focused) { Start-Sleep -Milliseconds 500 }
}
if (-not $focused) {
  Get-Process | Where-Object { $_.MainWindowTitle } |
    ForEach-Object { Write-Host ("  window: pid={0} title='{1}'" -f $_.Id, $_.MainWindowTitle) }
  throw "Could not focus the SimplySign Desktop connect window."
}

# Never send an OTP that is about to roll over mid-typing.
if ((Get-TotpSecondsRemaining) -lt 6) { Start-Sleep -Seconds (Get-TotpSecondsRemaining) }
$otp = Get-Totp -Secret $OtpSecret
Write-Host ("Derived TOTP (masked): {0}****" -f $otp.Substring(0, 2))

Start-Sleep -Milliseconds 400
$wshell.SendKeys((ConvertTo-SendKeysLiteral $Token))
Start-Sleep -Milliseconds 200
$wshell.SendKeys('{TAB}')
Start-Sleep -Milliseconds 200
$wshell.SendKeys($otp)
Start-Sleep -Milliseconds 200
$wshell.SendKeys('{ENTER}')
Write-Host "Credentials sent; waiting for the cloud smart-card to mount."

# --- 3. Wait until the certificate is usable ---------------------------------
# The virtual card mounts a few seconds after login and the cert lands in
# CurrentUser\My; signtool selects it there by thumbprint. Poll up to 3
# minutes: a slow mount is retried for free here, and failing fast after that
# beats letting signtool produce a cryptic "no certificates were found" later.
$deadline = [DateTime]::UtcNow.AddMinutes(3)
while ([DateTime]::UtcNow -lt $deadline) {
  $certs = Get-ChildItem Cert:\CurrentUser\My -ErrorAction SilentlyContinue
  if ($Thumbprint) {
    $hit = $certs | Where-Object { $_.Thumbprint -eq $Thumbprint.ToUpperInvariant() }
  } else {
    $hit = $certs | Where-Object { $_.HasPrivateKey }
  }
  if ($hit) {
    Write-Host ("SimplySign session active; certificate present: {0} ({1})" -f $hit[0].Subject, $hit[0].Thumbprint)
    exit 0
  }
  Start-Sleep -Seconds 5
}
Write-Host "Certificates currently in CurrentUser\My:"
Get-ChildItem Cert:\CurrentUser\My -ErrorAction SilentlyContinue |
  ForEach-Object { Write-Host ("  {0}  {1}" -f $_.Thumbprint, $_.Subject) }
throw "SimplySign login did not surface the signing certificate within 3 minutes."
