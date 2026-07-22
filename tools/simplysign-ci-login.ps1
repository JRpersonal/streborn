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

  !! TODO(before first signed release): the SimplySign Desktop download URL and
  the exact login command differ between SimplySign versions. The install/login
  block at the bottom is a placeholder and MUST be confirmed against the version
  installed locally (proCertumSmartSign / SimplySignDesktop.exe CLI). Once
  verified end-to-end with a throwaway exe, drop the TODO.

.PARAMETER Token
  SimplySign card/token identifier. Defaults to $env:SIMPLYSIGN_TOKEN.

.PARAMETER OtpSecret
  Base32 TOTP secret from the enrolment QR. Defaults to $env:SIMPLYSIGN_OTP_SECRET.
#>
param(
  [string]$Token     = $env:SIMPLYSIGN_TOKEN,
  [string]$OtpSecret = $env:SIMPLYSIGN_OTP_SECRET
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

$otp = Get-Totp -Secret $OtpSecret
Write-Host ("Derived TOTP (masked): {0}****" -f $otp.Substring(0, 2))

# --- TODO: confirm against the locally installed SimplySign Desktop ----------
# 1) Silent install:
#      $installer = Join-Path $env:TEMP 'SimplySignDesktop.exe'
#      Invoke-WebRequest -Uri '<official SimplySign Desktop download URL>' -OutFile $installer
#      Start-Process -FilePath $installer -ArgumentList '/S' -Wait
#
# 2) Non-interactive login (exact executable + flags version-dependent):
#      $ssd = Join-Path ${env:ProgramFiles} 'Certum\SimplySign Desktop\SimplySignDesktop.exe'
#      & $ssd --login --token $Token --otp $otp
#
# After login the certificate appears in the CurrentUser\My store and
# tools/certum-sign.ps1 can select it by thumbprint. Until this block is filled
# in, this script only proves the TOTP derivation works.
# -----------------------------------------------------------------------------
Write-Host "SimplySign login step is not yet wired up (see TODO in this script)."
