# STR Webseite

Statische Webseite fuer das STR Projekt. Gebaut mit Astro plus Tailwind. Produziert reines statisches HTML, kein Backend, kein JavaScript Framework. Damit minimale Angriffsflaeche, laeuft auf jedem Shared Hosting mit FTP Zugang.

## Schnellstart lokal

```bash
cd website
npm install
npm run dev          # Dev Server http://localhost:4321
npm run build        # Build nach dist/
npm run preview      # Build lokal anschauen
```

## Konfiguration

Lege im `website/` Ordner eine Datei `.env` an (aus `.env.example` kopieren):

```env
PUBLIC_SITE_URL=https://streborn.de
PUBLIC_GOATCOUNTER_CODE=                 # leer lassen wenn kein Analytics
```

Die Werte landen beim Build im HTML. Aendern heisst neu bauen.

## Deploy auf Shared Hosting mit FTP

Du hast FTP plus Webspace plus PHP plus DB. Wir nutzen FTP, der Rest bleibt ungenutzt. Die Seite ist statisches HTML und braucht weder PHP noch DB. PHP ist nur als Header Fallback hinterlegt fuer den Fall dass `mod_headers` deaktiviert ist (kommt selten vor).

### Variante A: Push auf main, automatisches Deploy

GitHub Action ist eingerichtet unter `.github/workflows/website-deploy.yml`. Bei jedem Push auf `main` mit Aenderungen im `website/` Ordner baut sie die Seite und laedt per FTP hoch.

Einmaliges Setup in der GitHub Repo Konfiguration unter Settings → Secrets and variables → Actions:

**Secrets** (verschluesselt, nicht sichtbar):

| Name | Wert |
|------|------|
| `FTP_SERVER` | dein FTP Host, z.B. `ftp.example.de` (kein `ftp://`) |
| `FTP_USERNAME` | dein FTP User |
| `FTP_PASSWORD` | dein FTP Passwort |

**Variables** (sichtbar, weniger sensibel):

| Name | Wert |
|------|------|
| `PUBLIC_SITE_URL` | `https://streborn.de` |
| `PUBLIC_GOATCOUNTER_CODE` | dein GoatCounter Code oder leer |
| `FTP_REMOTE_DIR` | Zielpfad auf dem FTP, oft `/html/` oder `/public_html/` oder `/`. Default `/` |

Nach Setup einfach Push auf main, der Workflow laeuft. Erste Lauf laedt alles hoch, danach nur Diffs (sehr schnell). State wird in `.ftp-deploy-state.json` auf dem Server gespeichert.

Wenn dein Hoster **SFTP** statt FTP anbietet, einfach in der Workflow Datei `SamKirkland/FTP-Deploy-Action` gegen `wlixcc/SFTP-Deploy-Action` tauschen. Falls **FTPS** noetig, in der gleichen Action `protocol: ftps` ergaenzen.

### Variante B: Manuell mit FileZilla

```bash
cd website
npm install
npm run build
```

Inhalt von `website/dist/` per FTP in dein Webspace Root hochladen. Wichtig:

- alles aus `dist/` ins Root des Hosters, nicht den `dist/` Ordner selbst
- die `.htaccess` Datei muss mit hoch (in FileZilla unter Server > Versteckte Dateien zwingen anzeigen aktivieren)
- bei Updates neu bauen und nur veraenderte Dateien hochladen, oder einfach alles ersetzen

### Variante C: lftp Mirror per PowerShell

Wenn du oft manuell deployen willst:

```powershell
cd website
npm run build

# WinSCP CLI vorausgesetzt
& "C:\Program Files (x86)\WinSCP\WinSCP.com" /command `
    "open ftp://USER:PASS@ftp.example.de" `
    "lcd dist" `
    "cd /" `
    "synchronize remote -delete" `
    "exit"
```

## Hoster spezifische Hinweise

### 1und1 IONOS / Strato / all-inkl / Hostpoint / Netcup

`mod_headers` und `mod_rewrite` sind standardmaessig aktiv. `.htaccess` greift sofort. HTTPS Zertifikat kommt vom Hoster (Let's Encrypt eingebaut). Einfach im Kundenbereich aktivieren.

### Mittwald / Uberspace

PHP basiert, alles funktioniert wie oben. Uberspace bietet zusaetzlich SSH, das brauchst du aber nicht.

### Plesk basierte Hoster

Im Plesk Panel unter Apache und nginx Settings kannst du die Header auch dort setzen, falls `.htaccess` nicht greift. Aber zuerst die .htaccess testen.

### Wenn deine Seite nach Deploy 500 zeigt

Meistens ein Modul Problem mit .htaccess. Loesung:

1. .htaccess temporaer umbenennen (`.htaccess.bak`) und Seite neu laden
2. Wenn 200 OK: in der .htaccess die `IfModule` Blocks einzeln aktivieren bis du den Stoerenfried findest
3. Im Zweifel Hoster Support fragen welche Module aktiv sind

## Pruefen nach Deploy

| Tool | URL | Was prueft es |
|------|-----|---------------|
| Security Headers | https://securityheaders.com/?q=streborn.de | A oder A+ Note ist Ziel |
| Mozilla Observatory | https://observatory.mozilla.org/analyze/streborn.de | Best Practices |
| SSL Labs | https://www.ssllabs.com/ssltest/analyze.html?d=streborn.de | A oder A+ Note |
| PageSpeed | https://pagespeed.web.dev/analysis?url=https://streborn.de | Performance |
| W3C HTML Validator | https://validator.w3.org/nu/?doc=https://streborn.de | HTML korrekt |

## Analytics

**GoatCounter** ist eingebaut, optional. Eigenschaften:

- keine Cookies
- kein eindeutiger Besucher Fingerprint
- hash basierter Schutz, nach 8 Stunden geloescht
- DSGVO konform
- kostenlos bis 100.000 Hits pro Monat

Anmelden unter https://www.goatcounter.com, Subdomain waehlen, `PUBLIC_GOATCOUNTER_CODE` setzen, neu bauen.

Alternativen wenn du auf deinem Hoster selbst hosten willst (PHP basiert geht beides):

- **Matomo** klassisch, PHP plus MySQL, du hast beides
- **Plausible Self Hosted** Docker, aber dafuer brauchst du SSH
- **Cloudflare Web Analytics** wenn deine Domain ueber Cloudflare laeuft

Da du MySQL DB im Webhosting hast, ist **Matomo** auch interessant. Brauchst du nicht, aber moeglich.

## Pflege

Inhalte aendern: `src/i18n/ui.ts` editieren. Beide Sprachen pflegen.

Neue Sprache hinzufuegen:

1. In `src/i18n/ui.ts` neuen Eintrag unter `languages` und `ui` anlegen
2. In `astro.config.mjs` die neue Locale unter `i18n.locales` ergaenzen
3. Routen unter `src/pages/<lang>/index.astro`, `imprint.astro`, `privacy.astro` anlegen

Spenden Plattform aendern: `src/components/Donate.astro` Variable `channels`. Plus `.github/FUNDING.yml` falls GitHub Sponsors Button auf dem Repo aktualisiert werden soll.

## Sicherheits Ueberlegungen

Was diese Seite gegen Angriffe schuetzt:

- **Statisches HTML**, kein PHP Code der laeuft. PHP ist nur Backup, nicht aktiv genutzt.
- **Kein User Input**, keine Formulare. Spenden ueber externe Plattformen.
- **Keine Datenbank** angeschlossen.
- **CSP Header** in .htaccess blockiert alles ausser deine eigenen Quellen plus GoatCounter.
- **HSTS** in .htaccess erzwingt HTTPS dauerhaft.
- **Sensible Dateien blockiert**: `.env`, `.git*`, `.md`, `.sh`, etc werden nicht ausgeliefert.

Bei sehr hohem Traffic Cloudflare Free Plan davor schalten. Orange Wolke in DNS einschalten, fertig. Damit CDN, DDoS Schutz, WAF.

## Was im `dist/` Ordner ist nach dem Build

```
dist/
  index.html              Landing Page deutsch
  impressum/index.html
  datenschutz/index.html
  en/index.html           Landing Page englisch
  en/imprint/index.html
  en/privacy/index.html
  _assets/
    *.css *.js *.svg      Hash basierte Dateien, 1 Jahr cachebar
  favicon.svg
  robots.txt
  sitemap-index.xml
  sitemap-0.xml
  .htaccess               Apache Config (siehe oben)
  _headers.php            Optionaler PHP Header Fallback
```

Diesen Inhalt komplett ins Webspace Root hochladen.
