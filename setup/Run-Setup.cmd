@echo off
REM Run-Setup.cmd: Doppelklick Launcher fuer den STR Setup Wizard.
REM Erzwingt Administrator Rechte via UAC, damit Format-Volume und andere
REM privilegierte Operationen funktionieren.

setlocal
set "SCRIPT_DIR=%~dp0"
set "PS_SCRIPT=%SCRIPT_DIR%Install-STR.ps1"

if not exist "%PS_SCRIPT%" (
    echo FEHLER: Install-STR.ps1 nicht gefunden im Verzeichnis %SCRIPT_DIR%
    pause
    exit /b 1
)

REM Pruefen ob wir bereits als Administrator laufen
net session >nul 2>&1
if %ERRORLEVEL% NEQ 0 (
    echo.
    echo Der Setup Wizard braucht Administratorrechte fuer Formatierung der SD Karte.
    echo Es erscheint gleich die Windows Benutzerkonten Steuerung. Bitte mit JA bestaetigen.
    echo.
    timeout /t 3 >nul
    if "%~1"=="" (
        powershell -Command "Start-Process '%~f0' -Verb RunAs"
    ) else (
        powershell -Command "Start-Process '%~f0' -Verb RunAs -ArgumentList '%*'"
    )
    exit /b
)

REM Hier laufen wir als Administrator
where pwsh >nul 2>&1
if %ERRORLEVEL% == 0 (
    pwsh -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%PS_SCRIPT%" %*
) else (
    powershell -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%PS_SCRIPT%" %*
)

echo.
pause
