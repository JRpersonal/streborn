// Central translation table. Each entry has a key in both languages.
// Add more locales (fr, es, it, nl, etc.) by appending blocks below.

export const languages = {
  en: 'English',
  de: 'Deutsch',
} as const;

export const defaultLang = 'en';
export type Lang = keyof typeof languages;

export const ui = {
  de: {
    'nav.features': 'Funktionen',
    'nav.download': 'Download',
    'nav.donate': 'Spenden',
    'nav.faq': 'FAQ',
    'nav.github': 'GitHub',

    'hero.tagline': 'Deine SoundTouch lebt weiter',
    'hero.headline': 'Internetradio für SoundTouch ohne Bose Cloud',
    'hero.subline': 'Bose hat den Cloud-Dienst eingestellt. Mit STR laufen deine SoundTouch-Lautsprecher autonom weiter. Internetradio, Preset-Tasten, alles über einen USB-Stick. Kein Konto, keine App, keine Anmeldung.',
    'hero.cta_download': 'Jetzt herunterladen',
    'hero.cta_github': 'Quellcode auf GitHub',
    'hero.badge': 'Open Source, Made in Germany',

    'features.heading': 'Was die App kann',
    'features.subheading': 'Ein kleiner USB-Stick steckt einmalig im Lautsprecher und installiert den Agent in dessen internen Speicher. Danach läuft alles ohne Stick weiter: beim Einschalten startet der Agent automatisch, redet über das Heimnetz mit dem Lautsprecher und macht ihn wieder voll nutzbar.',
    'features.f1.title': 'Internetradio direkt am Lautsprecher',
    'features.f1.body': 'Suche aus zehntausenden Radiosendern weltweit. Sender-Datenbank automatisch aktuell, kein Konto nötig.',
    'features.f2.title': 'Hardware-Preset-Tasten 1 bis 6',
    'features.f2.body': 'Die Tasten oben auf dem Lautsprecher funktionieren wie früher. Tastendruck spielt sofort deinen Lieblingssender.',
    'features.f3.title': 'Desktop-App für Windows und Mac',
    'features.f3.body': 'Sender verwalten, Presets zuweisen, mehrere Lautsprecher gleichzeitig im Heimnetz steuern.',
    'features.f4.title': 'Plug and Play',
    'features.f4.body': 'Stick reinstecken, Lautsprecher einschalten, fertig. Kein Löten, keine Firmware-Änderung, kein Cloud-Konto.',
    'features.f5.title': 'Komplett offline',
    'features.f5.body': 'Keine Datensammlung. Kein Bose-Server. Alles läuft lokal in deinem WLAN.',
    'features.f6.title': 'Frei und Open Source',
    'features.f6.body': 'MIT-Lizenz, Quellcode auf GitHub. Du kannst prüfen, was auf deinen Geräten läuft.',

    'compat.heading': 'Funktioniert mit',
    'compat.subheading': 'Aktuell getestet mit SoundTouch 10. Weitere Modelle in Vorbereitung.',
    'compat.tested': 'Getestet und stabil',
    'compat.in_progress': 'In Entwicklung',
    'compat.planned': 'Geplant',
    'compat.disclaimer': 'Bose und SoundTouch sind Marken der Bose Corporation. Dieses Projekt steht in keiner Verbindung zu Bose.',

    'download.heading': 'Download',
    'download.subheading': 'Lade dir den Setup-Wizard. Er bereitet einen USB-Stick vor, mit dem du den Agent einmalig auf den Lautsprecher installierst.',
    'download.win.title': 'Windows Setup-Wizard',
    'download.win.body': 'Führt dich durch den Setup-Prozess. Erkennt deinen SoundTouch automatisch im Netzwerk.',
    'download.win.cta': 'Windows-Setup herunterladen',
    'download.mac.title': 'macOS Setup-Wizard',
    'download.mac.body': 'Universal Binary für Apple Silicon und Intel.',
    'download.mac.cta': 'macOS-Setup herunterladen',
    'download.size': 'Größe',
    'download.checksum': 'SHA256',
    'download.source': 'Du möchtest selbst kompilieren? Quellcode und Bauanleitung auf GitHub.',
    'verify.heading': 'Echtheit der Datei prüfen',
    'verify.intro': 'Jede heruntergeladene Datei kann verifiziert werden. So weißt du, dass die Datei aus der offiziellen GitHub Build-Pipeline kommt und unterwegs nicht manipuliert wurde.',
    'verify.method1.title': 'Methode 1: SHA256-Prüfsumme',
    'verify.method1.body': 'Berechne den SHA256-Hash deiner heruntergeladenen Datei und vergleiche ihn mit dem Wert auf der Release-Seite. Stimmt der Hash überein, ist die Datei unverändert.',
    'verify.method2.title': 'Methode 2: GitHub Build Attestation',
    'verify.method2.body': 'Mit der GitHub CLI kannst du beweisen, dass die Binary aus der offiziellen Build-Pipeline stammt. Das funktioniert auch, wenn jemand später die Prüfsumme manipuliert.',
    'verify.note': 'Lade Binaries ausschließlich von der offiziellen GitHub-Release-Seite oder dieser Webseite. Niemals von Drittanbietern.',

    'donate.heading': 'Spenden',
    'donate.subheading': 'STR ist kostenlos und Open Source. Wenn dir das Projekt gefällt, freue ich mich über eine Spende. Jeder Beitrag hilft, die nächsten Lautsprecher-Modelle zu unterstützen.',
    'donate.github': 'GitHub Sponsors',
    'donate.bmc': 'Buy Me a Coffee',
    'donate.kofi': 'Ko-Fi',
    'donate.liberapay': 'Liberapay',
    'donate.paypal': 'PayPal',
    'donate.opencollective': 'Open Collective',
    'donate.crypto': 'Krypto',
    'donate.transparency': 'Spenden werden dazu verwendet, die App weiterzuentwickeln, neue Lautsprecher-Modelle zu testen und die Hosting-Kosten der Webseite zu decken. Bilanzen werden einmal im Jahr veröffentlicht.',

    'faq.heading': 'Häufige Fragen',
    'faq.q1.q': 'Ist meine SoundTouch danach noch sicher?',
    'faq.q1.a': 'STR ändert nichts an der Originalfirmware. Es täuscht dem Lautsprecher nur einen lokalen Cloud-Server auf 127.0.0.1 vor, damit er wie gewohnt funktioniert. Ein Werks-Reset löscht den eigenen STR-Anteil unter /mnt/nv/streborn/ allerdings nicht; dafür ist ein separater Uninstall-Schritt vorgesehen.',
    'faq.q2.q': 'Brauche ich technisches Wissen?',
    'faq.q2.a': 'Nein. Der Setup-Wizard führt dich Schritt für Schritt durch den Prozess. Du brauchst einen USB-Stick (mindestens 4 GB) und ein paar Minuten Zeit.',
    'faq.q3.q': 'Warum hat Bose den Cloud-Dienst eingestellt?',
    'faq.q3.a': 'Bose hat den SoundTouch-Cloud-Dienst im Februar 2026 abgeschaltet. Damit fielen Streaming-Dienste, Preset-Tasten und die Bose-App weitgehend aus. Die Lautsprecher wurden über Nacht teilweise nutzlos.',
    'faq.q4.q': 'Welche Sender kann ich hören?',
    'faq.q4.a': 'Alle Sender mit öffentlicher Stream-URL. Die Sender-Datenbank kommt von radio-browser.info mit über 30.000 Sendern weltweit. Du kannst auch eigene Stream-URLs hinzufügen.',
    'faq.q5.q': 'Werden meine Daten gesammelt?',
    'faq.q5.a': 'Nein. Es gibt keinen zentralen Server. Alles läuft in deinem Heimnetz. Die Webseite selbst nutzt eine datensparsame Statistik ohne Cookies und ohne Identifikation einzelner Besucher.',
    'faq.q6.q': 'Wann werden andere Modelle unterstützt?',
    'faq.q6.a': 'Sobald genug Testgeräte vorhanden sind. Mit deiner Spende kann ich gebrauchte SoundTouch 20, 30 und Portable anschaffen und durchtesten.',
    'faq.q7.q': 'Ich brauche Hilfe, wo melden?',
    'faq.q7.a': 'Issue auf GitHub eröffnen oder per E-Mail. Antworten oft am gleichen Tag.',

    'footer.about': 'Über das Projekt',
    'footer.docs': 'Dokumentation',
    'footer.github': 'GitHub-Repository',
    'footer.imprint': 'Impressum',
    'footer.privacy': 'Datenschutz',
    'footer.disclaimer': 'Nicht mit der Bose Corporation verbunden. Bose und SoundTouch sind eingetragene Marken der Bose Corporation.',
    'footer.builtwith': 'Gebaut mit Astro, Tailwind und viel Liebe für alte Hardware.',
  },
  en: {
    'nav.features': 'Features',
    'nav.download': 'Download',
    'nav.donate': 'Donate',
    'nav.faq': 'FAQ',
    'nav.github': 'GitHub',

    'hero.tagline': 'Your SoundTouch lives on',
    'hero.headline': 'Internet Radio for SoundTouch without the Bose Cloud',
    'hero.subline': 'Bose shut down their cloud service. With STR your SoundTouch speakers keep running on their own. Internet Radio, preset buttons, all through a tiny USB stick. No account, no app, no sign up.',
    'hero.cta_download': 'Download now',
    'hero.cta_github': 'Source on GitHub',
    'hero.badge': 'Open Source, made in Germany',

    'features.heading': 'What the app does',
    'features.subheading': 'A small USB stick stays plugged into the back of your speaker. When you turn the speaker on, the app starts automatically, talks to your box over the home network, and makes it fully usable again.',
    'features.f1.title': 'Internet Radio straight from the speaker',
    'features.f1.body': 'Search across tens of thousands of stations worldwide. Station database stays current automatically. No account required.',
    'features.f2.title': 'Hardware preset buttons 1 to 6',
    'features.f2.body': 'The buttons on top of the speaker work like they used to. Press one and your favorite station plays instantly.',
    'features.f3.title': 'Desktop app for Windows and Mac',
    'features.f3.body': 'Manage stations, assign presets, control multiple speakers across your network from one place.',
    'features.f4.title': 'Plug and play',
    'features.f4.body': 'Stick in, power on, done. No soldering, no firmware modification, no cloud account.',
    'features.f5.title': 'Fully offline',
    'features.f5.body': 'No data collection. No Bose server. Everything runs locally inside your WiFi.',
    'features.f6.title': 'Free and Open Source',
    'features.f6.body': 'MIT licensed, source on GitHub. You can inspect every line that runs on your devices.',

    'compat.heading': 'Works with',
    'compat.subheading': 'Currently tested with SoundTouch 10. More models on the roadmap.',
    'compat.tested': 'Tested and stable',
    'compat.in_progress': 'In development',
    'compat.planned': 'Planned',
    'compat.disclaimer': 'Bose and SoundTouch are trademarks of Bose Corporation. This project is not affiliated with Bose.',

    'download.heading': 'Download',
    'download.subheading': 'Get the Setup Wizard. It prepares a USB stick that lives permanently inside your speaker.',
    'download.win.title': 'Windows Setup Wizard',
    'download.win.body': 'Guided setup, auto detects your SoundTouch on the network.',
    'download.win.cta': 'Download for Windows',
    'download.mac.title': 'macOS Setup Wizard',
    'download.mac.body': 'Universal binary for Apple Silicon and Intel.',
    'download.mac.cta': 'Download for macOS',
    'download.size': 'Size',
    'download.checksum': 'SHA256',
    'download.source': 'Want to build from source? Code and build instructions on GitHub.',
    'verify.heading': 'Verify your download',
    'verify.intro': 'Every download can be verified. This way you know the file came from the official GitHub build pipeline and was not tampered with in transit.',
    'verify.method1.title': 'Method 1: SHA256 checksum',
    'verify.method1.body': 'Compute the SHA256 hash of your downloaded file and compare it with the value on the Release page. If hashes match, the file is unchanged.',
    'verify.method2.title': 'Method 2: GitHub build attestation',
    'verify.method2.body': 'Using the GitHub CLI you can cryptographically prove the binary was produced by the official build pipeline. This still works even if someone tampered with the checksum afterwards.',
    'verify.note': 'Only download binaries from the official GitHub release page or this website. Never from third party sources.',

    'donate.heading': 'Donate',
    'donate.subheading': 'STR is free and Open Source. If the project helped you, please consider a donation. Every contribution supports more speaker models.',
    'donate.github': 'GitHub Sponsors',
    'donate.bmc': 'Buy Me a Coffee',
    'donate.kofi': 'Ko-Fi',
    'donate.liberapay': 'Liberapay',
    'donate.paypal': 'PayPal',
    'donate.opencollective': 'Open Collective',
    'donate.crypto': 'Crypto',
    'donate.transparency': 'Donations are used to develop the app further, test additional speaker models, and cover hosting costs. Yearly transparency report published.',

    'faq.heading': 'Frequently Asked Questions',
    'faq.q1.q': 'Is my SoundTouch still safe afterwards?',
    'faq.q1.a': 'Yes. The stick does not modify the speaker firmware. It only pretends to be a cloud server so the speaker behaves normally. Pull the stick out and your speaker is back to factory state.',
    'faq.q2.q': 'Do I need technical knowledge?',
    'faq.q2.a': 'No. The Setup Wizard guides you step by step. You need a USB stick, a Micro USB adapter and ten minutes.',
    'faq.q3.q': 'Why did Bose shut down their cloud?',
    'faq.q3.a': 'Bose ended the SoundTouch cloud service at the end of 2024. Streaming services, preset buttons and the Bose app stopped working. The speakers became partially useless overnight.',
    'faq.q4.q': 'Which stations can I listen to?',
    'faq.q4.a': 'Any station with a public stream URL. The station catalog comes from radio-browser.info with over 30,000 stations worldwide. You can also add your own stream URLs.',
    'faq.q5.q': 'Is my data being collected?',
    'faq.q5.a': 'No. There is no central server. Everything runs in your home network. The website itself uses a minimal cookie-free analytics that does not identify individual visitors.',
    'faq.q6.q': 'When will other models be supported?',
    'faq.q6.a': 'As soon as I have test devices for them. Your donation helps me purchase used SoundTouch 30, 300 and Wave units for integration work.',
    'faq.q7.q': 'I need help, where do I ask?',
    'faq.q7.a': 'Open an issue on GitHub or send me an email. Usually answered same day.',

    'footer.about': 'About the project',
    'footer.docs': 'Documentation',
    'footer.github': 'GitHub repository',
    'footer.imprint': 'Imprint',
    'footer.privacy': 'Privacy',
    'footer.disclaimer': 'Not affiliated with Bose Corporation. Bose and SoundTouch are registered trademarks of Bose Corporation.',
    'footer.builtwith': 'Built with Astro, Tailwind and a lot of love for old hardware.',
  },
} as const;

export type UIKey = keyof (typeof ui)['de'];

export function useTranslations(lang: Lang) {
  return function t(key: UIKey): string {
    return (ui[lang] as Record<string, string>)[key] ?? (ui[defaultLang] as Record<string, string>)[key] ?? key;
  };
}

export function getLangFromUrl(url: URL): Lang {
  const [, maybeLang] = url.pathname.split('/');
  if (maybeLang && maybeLang in languages) return maybeLang as Lang;
  return defaultLang;
}

// Builds a path for the requested locale. The default locale
// (English) stays at "/", every other locale goes under "/<lang>/".
export function localizedPath(lang: Lang, path: string): string {
  const clean = path.startsWith('/') ? path : `/${path}`;
  if (lang === defaultLang) return clean;
  if (clean === '/') return `/${lang}/`;
  return `/${lang}${clean}`;
}
