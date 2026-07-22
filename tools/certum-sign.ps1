<#
.SYNOPSIS
  Sign a Windows binary with the Certum "Open Source" code signing certificate.

.DESCRIPTION
  Locates signtool.exe from the installed Windows SDK, signs the given file with
  SHA-256 and an RFC-3161 timestamp from Certum, then verifies the signature.

  The certificate is selected by its SHA-1 thumbprint (visible in the SimplySign
  cloud profile). The private key lives in the SimplySign cloud and is only
  reachable while a SimplySign Desktop session is active, so this script assumes
  such a session already exists: established interactively via the SimplySign
  mobile app when signing locally, or by tools/simplysign-ci-login.ps1 in CI.

  Usable both locally (after logging in through SimplySign Desktop) and from the
  release workflow.

.PARAMETER File
  Path to the .exe (or other PE) to sign.

.PARAMETER Thumbprint
  SHA-1 thumbprint of the code signing certificate. Defaults to $env:CERT_THUMBPRINT.
#>
param(
  [Parameter(Mandatory = $true)][string]$File,
  [string]$Thumbprint = $env:CERT_THUMBPRINT
)
$ErrorActionPreference = 'Stop'

if (-not $Thumbprint) {
  throw "No certificate thumbprint given (pass -Thumbprint or set CERT_THUMBPRINT)."
}
if (-not (Test-Path -LiteralPath $File)) {
  throw "File not found: $File"
}

# Locate the newest x64 signtool.exe from the Windows SDK.
$roots = @(
  "${env:ProgramFiles(x86)}\Windows Kits\10\bin",
  "${env:ProgramFiles}\Windows Kits\10\bin"
)
$signtool = $null
foreach ($r in $roots) {
  if (Test-Path -LiteralPath $r) {
    $cand = Get-ChildItem -Path $r -Recurse -Filter signtool.exe -ErrorAction SilentlyContinue |
      Where-Object { $_.FullName -match '\\x64\\signtool\.exe$' } |
      Sort-Object FullName -Descending | Select-Object -First 1
    if ($cand) { $signtool = $cand.FullName; break }
  }
}
if (-not $signtool) {
  throw "signtool.exe not found. Install the Windows SDK (or Visual Studio Build Tools)."
}
Write-Host "Using signtool: $signtool"

# Certum's RFC-3161 timestamp authority. Timestamping is what keeps the
# signature valid after the certificate itself expires.
$timestamp = 'http://time.certum.pl'

& $signtool sign /sha1 $Thumbprint /fd SHA256 /tr $timestamp /td SHA256 /v "$File"
if ($LASTEXITCODE -ne 0) { throw "signtool sign failed (exit $LASTEXITCODE)" }

& $signtool verify /pa /v "$File"
if ($LASTEXITCODE -ne 0) { throw "signtool verify failed (exit $LASTEXITCODE)" }

Write-Host "Signed and verified: $File"
