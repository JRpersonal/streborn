<?php
// Optionaler PHP Fallback fuer Sicherheits Header
//
// Nutzung: nur wenn dein Hoster mod_headers nicht aktiv hat und die .htaccess
// Anweisungen ignoriert. In dem Fall in die .htaccess oben ergaenzen:
//
//   <IfModule php_module>
//       php_value auto_prepend_file "/absoluter/pfad/zu/_headers.php"
//   </IfModule>
//
// Damit wird dieses Skript vor jeder PHP Datei eingebunden. Da die Seite aber
// statisch HTML ist, wuerde es nichts bringen ausser bei .php Endungen.
//
// Realistische Anwendung: pruefe nach dem ersten Upload mit
// https://securityheaders.com ob die .htaccess deine Header setzt.
// Wenn nicht, kontaktiere den Hoster Support und frage ob mod_headers
// aktiviert werden kann. Bei allen seriösen Hostern gehoert das zur
// Standardausstattung.

header_remove('X-Powered-By');
header('Strict-Transport-Security: max-age=31536000; includeSubDomains');
header('X-Frame-Options: DENY');
header('X-Content-Type-Options: nosniff');
header('Referrer-Policy: strict-origin-when-cross-origin');
header('Permissions-Policy: geolocation=(), microphone=(), camera=(), payment=(), usb=()');
header(
    "Content-Security-Policy: default-src 'self'; "
    . "script-src 'self' https://gc.zgo.at; "
    . "img-src 'self' data: https:; "
    . "style-src 'self' 'unsafe-inline'; "
    . "font-src 'self' data:; "
    . "connect-src 'self' https://*.goatcounter.com; "
    . "frame-ancestors 'none'; "
    . "base-uri 'self'; "
    . "form-action 'self'; "
    . "upgrade-insecure-requests"
);
