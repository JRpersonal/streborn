# STR website copy, v0.7.2 (2026-06-06)

Handoff for the web team (st-reborn.de). One section per UI language,
identical block structure. Canonical slogan is German:
"Dein SoundTouch lebt wieder. Ohne Bose-Cloud." All other languages
carry a faithful translation of it.

Block order per language:
1. Hero: slogan, subline, CTA buttons, trust line
2. Three hero cards: Internet radio, Spotify Connect (Beta), Hardware buttons
3. Feature blocks: Local, One app, Guided install, Eleven languages
4. Verify block
5. Trademark disclaimer (footer, every page)

Notes for implementation:
- Spotify card always shows a visible "Beta" pill plus the one-line
  expectation note.
- Stability fixes (Portable loop, BCO loop, macOS crash) go in the
  changelog, not on these pages.
- The disclaimer is mandatory and must be removable by no one.

---

## Deutsch (de)  -  canonical

**Hero slogan:** Dein SoundTouch lebt wieder. Ohne Bose-Cloud.
**Subline:** Internetradio, Presets und Fernsteuerung fuer SoundTouch 10, 20, 30 und Portable. Lokal, kostenlos, ohne Konto, ohne Cloud.
**CTA:** Herunterladen  /  So funktioniert's  /  Windows  -  macOS  -  Linux
**Trust line:** Open Source  -  keine Telemetrie  -  verifizierbare Downloads

**Card 1, Internetradio zurueck**
Tausende Sender durchsuchen und abspielen. Ersetzt das abgeschaltete Bose-TuneIn, ohne API-Schluessel, ohne Konto.

**Card 2, Spotify Connect [Beta]**
Spotify direkt auf dem Lautsprecher. Playlists auf die Hardware-Tasten legen und per Knopfdruck starten.
Beta-Hinweis: Spotify Premium noetig, einzelne Funktionen noch in Arbeit.

**Card 3, Hardware-Tasten 1 bis 6**
Die Preset-Tasten am Geraet funktionieren wieder, genau wie frueher. Kaltstart, Standby, WLAN-Ausfall: bleiben belegt.

**Feature, Komplett lokal, nichts verlaesst dein Netz**
Keine Cloud, keine Telemetrie, kein Konto. Audio geht direkt per UPnP an den Lautsprecher. Pro Geraet eine lokale TLS-CA, beim ersten Start erzeugt, nie uebertragen.

**Feature, Eine App fuer alles**
Windows, macOS und Linux. Findet deine Lautsprecher von selbst im Netzwerk. Browsen, Presets verwalten, Wiedergabe steuern, alles an einem Ort.

**Feature, Gefuehrte Erstinstallation per USB-Stick**
Die App fuehrt dich Schritt fuer Schritt durch die Einrichtung. Danach laeuft alles auf dem Geraet, der Stick kann raus.

**Feature, Elf Sprachen**
Die Oberflaeche spricht Englisch, Deutsch, Niederlaendisch, Polnisch, Litauisch, Lettisch, Tuerkisch, Spanisch, Franzoesisch, Ukrainisch und Japanisch. Weitere Sprachen sind als Beitrag willkommen.

**Verify, Verifizierbare Downloads**
Jeder Download hat eine SHA256-Summe und eine Sigstore-Attestierung. Die Verify-Seite zeigt den genauen Klickpfad fuer Windows-SmartScreen und macOS-Gatekeeper.

**Disclaimer:** "SoundTouch" und "Bose" sind eingetragene Marken der Bose Corporation. STR ist ein inoffizielles Community-Projekt, nicht mit Bose verbunden, von Bose nicht unterstuetzt oder autorisiert. Nutzung auf eigenes Risiko.

---

## English (en)

**Hero slogan:** Your SoundTouch lives again. Without the Bose cloud.
**Subline:** Internet radio, presets and remote control for SoundTouch 10, 20, 30 and Portable. Local, free, no account, no cloud.
**CTA:** Download  /  How it works  /  Windows  -  macOS  -  Linux
**Trust line:** Open source  -  no telemetry  -  verifiable downloads

**Card 1, Internet radio is back**
Search and play thousands of stations. Replaces the discontinued Bose TuneIn, no API key, no account.

**Card 2, Spotify Connect [Beta]**
Spotify straight to your speaker. Put playlists on the hardware buttons and start them with one press.
Beta note: Spotify Premium required, some features still in progress.

**Card 3, Hardware buttons 1 to 6**
The preset buttons on the speaker work again, just like before. Cold boot, standby, Wi-Fi outage: they stay assigned.

**Feature, Completely local, nothing leaves your network**
No cloud, no telemetry, no account. Audio goes straight to the speaker over UPnP. One local TLS CA per device, created on first boot, never transmitted.

**Feature, One app for everything**
Windows, macOS and Linux. Finds your speakers by itself on the network. Browse, manage presets, control playback, all in one place.

**Feature, Guided USB-stick install**
The app walks you through setup step by step. After that everything runs on the device and the stick can come out.

**Feature, Eleven languages**
The interface speaks English, German, Dutch, Polish, Lithuanian, Latvian, Turkish, Spanish, French, Ukrainian and Japanese. More languages welcome as a contribution.

**Verify, Verifiable downloads**
Every download has a SHA256 sum and a Sigstore attestation. The Verify page shows the exact click path for Windows SmartScreen and macOS Gatekeeper.

**Disclaimer:** "SoundTouch" and "Bose" are registered trademarks of Bose Corporation. STR is an unofficial, community-built project, not affiliated with, endorsed by, or authorized by Bose. Use at your own risk.

---

## Espanol (es)

**Hero slogan:** Tu SoundTouch vuelve a la vida. Sin la nube de Bose.
**Subline:** Radio por internet, presets y control remoto para SoundTouch 10, 20, 30 y Portable. Local, gratis, sin cuenta, sin nube.
**CTA:** Descargar  /  Como funciona  /  Windows  -  macOS  -  Linux
**Trust line:** Codigo abierto  -  sin telemetria  -  descargas verificables

**Card 1, La radio por internet vuelve**
Busca y reproduce miles de emisoras. Sustituye al desaparecido Bose TuneIn, sin clave de API, sin cuenta.

**Card 2, Spotify Connect [Beta]**
Spotify directo en el altavoz. Asigna playlists a los botones fisicos y arrancalas con una pulsacion.
Nota beta: Requiere Spotify Premium, algunas funciones aun en desarrollo.

**Card 3, Botones fisicos 1 a 6**
Los botones de presets del altavoz vuelven a funcionar, como antes. Arranque en frio, reposo, caida de wifi: siguen asignados.

**Feature, Totalmente local, nada sale de tu red**
Sin nube, sin telemetria, sin cuenta. El audio va directo al altavoz por UPnP. Una CA TLS local por dispositivo, creada en el primer arranque, nunca transmitida.

**Feature, Una sola app para todo**
Windows, macOS y Linux. Encuentra tus altavoces sola en la red. Explora, gestiona presets y controla la reproduccion, todo en un sitio.

**Feature, Instalacion guiada por memoria USB**
La app te guia paso a paso en la configuracion. Despues todo corre en el dispositivo y puedes quitar la memoria.

**Feature, Once idiomas**
La interfaz habla ingles, aleman, neerlandes, polaco, lituano, leton, turco, espanol, frances, ucraniano y japones. Mas idiomas son bienvenidos como contribucion.

**Verify, Descargas verificables**
Cada descarga tiene una suma SHA256 y una atestacion Sigstore. La pagina Verify muestra la ruta exacta de clics para Windows SmartScreen y macOS Gatekeeper.

**Disclaimer:** "SoundTouch" y "Bose" son marcas registradas de Bose Corporation. STR es un proyecto comunitario no oficial, sin afiliacion, respaldo ni autorizacion de Bose. Uso bajo tu propia responsabilidad.

---

## Francais (fr)

**Hero slogan:** Ton SoundTouch revit. Sans le cloud Bose.
**Subline:** Radio internet, preselections et telecommande pour SoundTouch 10, 20, 30 et Portable. Local, gratuit, sans compte, sans cloud.
**CTA:** Telecharger  /  Comment ca marche  /  Windows  -  macOS  -  Linux
**Trust line:** Open source  -  sans telemetrie  -  telechargements verifiables

**Card 1, La radio internet est de retour**
Cherche et ecoute des milliers de stations. Remplace Bose TuneIn, disparu, sans cle d'API, sans compte.

**Card 2, Spotify Connect [Beta]**
Spotify directement sur l'enceinte. Associe des playlists aux boutons physiques et lance-les d'une pression.
Note beta: Spotify Premium requis, certaines fonctions encore en cours.

**Card 3, Boutons physiques 1 a 6**
Les boutons de preselection de l'enceinte refonctionnent, comme avant. Demarrage a froid, veille, coupure Wi-Fi: ils restent affectes.

**Feature, Tout en local, rien ne quitte ton reseau**
Pas de cloud, pas de telemetrie, pas de compte. L'audio va directement a l'enceinte via UPnP. Une autorite TLS locale par appareil, creee au premier demarrage, jamais transmise.

**Feature, Une seule app pour tout**
Windows, macOS et Linux. Trouve tes enceintes toute seule sur le reseau. Parcours, gestion des preselections, controle de la lecture, le tout au meme endroit.

**Feature, Installation guidee par cle USB**
L'app te guide pas a pas dans la configuration. Ensuite tout tourne sur l'appareil et tu peux retirer la cle.

**Feature, Onze langues**
L'interface parle anglais, allemand, neerlandais, polonais, lituanien, letton, turc, espagnol, francais, ukrainien et japonais. D'autres langues sont les bienvenues en contribution.

**Verify, Telechargements verifiables**
Chaque telechargement a une somme SHA256 et une attestation Sigstore. La page Verify montre le chemin de clics exact pour Windows SmartScreen et macOS Gatekeeper.

**Disclaimer:** "SoundTouch" et "Bose" sont des marques deposees de Bose Corporation. STR est un projet communautaire non officiel, sans affiliation, approbation ni autorisation de Bose. Utilisation a tes propres risques.

---

## Nederlands (nl)

**Hero slogan:** Je SoundTouch leeft weer. Zonder de Bose-cloud.
**Subline:** Internetradio, presets en afstandsbediening voor SoundTouch 10, 20, 30 en Portable. Lokaal, gratis, zonder account, zonder cloud.
**CTA:** Downloaden  /  Hoe het werkt  /  Windows  -  macOS  -  Linux
**Trust line:** Open source  -  geen telemetrie  -  verifieerbare downloads

**Card 1, Internetradio is terug**
Doorzoek en speel duizenden zenders. Vervangt het gestopte Bose TuneIn, zonder API-sleutel, zonder account.

**Card 2, Spotify Connect [Beta]**
Spotify rechtstreeks op de speaker. Zet playlists op de fysieke knoppen en start ze met een druk.
Beta-opmerking: Spotify Premium vereist, sommige functies nog in ontwikkeling.

**Card 3, Fysieke knoppen 1 tot 6**
De presetknoppen op de speaker werken weer, net als vroeger. Koude start, stand-by, wifi-storing: ze blijven toegewezen.

**Feature, Volledig lokaal, niets verlaat je netwerk**
Geen cloud, geen telemetrie, geen account. Audio gaat rechtstreeks naar de speaker via UPnP. Een lokale TLS-CA per apparaat, aangemaakt bij de eerste start, nooit verzonden.

**Feature, Een app voor alles**
Windows, macOS en Linux. Vindt je speakers zelf op het netwerk. Bladeren, presets beheren, afspelen regelen, alles op een plek.

**Feature, Begeleide eerste installatie via USB-stick**
De app loodst je stap voor stap door de installatie. Daarna draait alles op het apparaat en kan de stick eruit.

**Feature, Elf talen**
De interface spreekt Engels, Duits, Nederlands, Pools, Litouws, Lets, Turks, Spaans, Frans, Oekraiens en Japans. Meer talen welkom als bijdrage.

**Verify, Verifieerbare downloads**
Elke download heeft een SHA256-som en een Sigstore-attestatie. De Verify-pagina toont het exacte klikpad voor Windows SmartScreen en macOS Gatekeeper.

**Disclaimer:** "SoundTouch" en "Bose" zijn geregistreerde handelsmerken van Bose Corporation. STR is een onofficieel, door de community gebouwd project, niet verbonden met, goedgekeurd door of geautoriseerd door Bose. Gebruik op eigen risico.

---

## Polski (pl)

**Hero slogan:** Twoj SoundTouch znow zyje. Bez chmury Bose.
**Subline:** Radio internetowe, presety i zdalne sterowanie dla SoundTouch 10, 20, 30 i Portable. Lokalnie, za darmo, bez konta, bez chmury.
**CTA:** Pobierz  /  Jak to dziala  /  Windows  -  macOS  -  Linux
**Trust line:** Otwarte zrodlo  -  brak telemetrii  -  weryfikowalne pliki

**Card 1, Radio internetowe wraca**
Przeszukuj i odtwarzaj tysiace stacji. Zastepuje wylaczone Bose TuneIn, bez klucza API, bez konta.

**Card 2, Spotify Connect [Beta]**
Spotify prosto na glosniku. Przypisz playlisty do przyciskow i wlaczaj je jednym nacisnieciem.
Uwaga beta: Wymaga Spotify Premium, czesc funkcji wciaz w przygotowaniu.

**Card 3, Przyciski sprzetowe 1 do 6**
Przyciski presetow na glosniku znow dzialaja, tak jak kiedys. Zimny start, czuwanie, awaria Wi-Fi: pozostaja przypisane.

**Feature, W pelni lokalnie, nic nie opuszcza twojej sieci**
Bez chmury, bez telemetrii, bez konta. Dzwiek trafia prosto do glosnika przez UPnP. Jeden lokalny TLS CA na urzadzenie, tworzony przy pierwszym uruchomieniu, nigdy nie wysylany.

**Feature, Jedna aplikacja do wszystkiego**
Windows, macOS i Linux. Sama znajduje twoje glosniki w sieci. Przegladaj, zarzadzaj presetami, steruj odtwarzaniem, wszystko w jednym miejscu.

**Feature, Prowadzona pierwsza instalacja przez pendrive**
Aplikacja przeprowadza cie krok po kroku przez konfiguracje. Potem wszystko dziala na urzadzeniu, a pendrive mozna wyjac.

**Feature, Jedenascie jezykow**
Interfejs mowi po angielsku, niemiecku, niderlandzku, polsku, litewsku, lotewsku, turecku, hiszpansku, francusku, ukrainsku i japonsku. Kolejne jezyki mile widziane jako wklad.

**Verify, Weryfikowalne pliki**
Kazdy plik ma sume SHA256 i atestacje Sigstore. Strona Verify pokazuje dokladna sciezke klikniec dla Windows SmartScreen i macOS Gatekeeper.

**Disclaimer:** "SoundTouch" i "Bose" to zastrzezone znaki towarowe Bose Corporation. STR to nieoficjalny projekt spolecznosciowy, niepowiazany z Bose, nie wspierany ani nie autoryzowany przez Bose. Korzystasz na wlasne ryzyko.

---

## Lietuviu (lt)

**Hero slogan:** Tavo SoundTouch vel gyvas. Be Bose debesies.
**Subline:** Interneto radijas, issankstiniai nustatymai ir nuotolinis valdymas SoundTouch 10, 20, 30 ir Portable. Vietinis, nemokamas, be paskyros, be debesies.
**CTA:** Atsisiusti  /  Kaip tai veikia  /  Windows  -  macOS  -  Linux
**Trust line:** Atviras kodas  -  jokios telemetrijos  -  patikrinami atsisiuntimai

**Card 1, Interneto radijas grizo**
Ieskok ir leisk tukstancius stociu. Pakeicia nebeveikianti Bose TuneIn, be API rakto, be paskyros.

**Card 2, Spotify Connect [Beta]**
Spotify tiesiai i garsiakalbi. Priskirk grojarascius mygtukams ir paleisk vienu paspaudimu.
Beta pastaba: Reikia Spotify Premium, kai kurios funkcijos dar kuriamos.

**Card 3, Aparatiniai mygtukai nuo 1 iki 6**
Garsiakalbio issankstiniu nustatymu mygtukai vel veikia kaip anksciau. Po saltojo paleidimo, budejimo ar Wi-Fi dingimo priskyrimai islieka.

**Feature, Viskas vietoje, niekas neiseina is tavo tinklo**
Jokio debesies, jokios telemetrijos, jokios paskyros. Garsas keliauja tiesiai i garsiakalbi per UPnP. Kiekvienam irenginiui sukuriama vietine TLS CA per pirma paleidima ir niekada nesiunciama.

**Feature, Viena programa viskam**
Windows, macOS ir Linux. Pati randa tavo garsiakalbius tinkle. Narsyk, tvarkyk issankstinius nustatymus, valdyk grojima, viskas vienoje vietoje.

**Feature, Vedama pirmine diegtis per USB atmintuka**
Programa zingsnis po zingsnio padeda viska nustatyti. Po to viskas veikia irenginyje, o atmintuka gali istraukti.

**Feature, Vienuolika kalbu**
Sasaja kalba angliskai, vokiskai, olandiskai, lenkiskai, lietuviskai, latviskai, turkiskai, ispaniskai, prancuziskai, ukrainietiskai ir japoniskai. Kitos kalbos laukiamos kaip indelis.

**Verify, Patikrinami atsisiuntimai**
Kiekvienas atsisiuntimas turi SHA256 suma ir Sigstore patvirtinima. Verify puslapyje rodomas tikslus paspaudimu kelias Windows SmartScreen ir macOS Gatekeeper.

**Disclaimer:** "SoundTouch" ir "Bose" yra registruoti Bose Corporation prekiu zenklai. STR yra neoficialus bendruomenes projektas, nesusijes su Bose, jos nepatvirtintas ir neigaliotas. Naudojiesi savo paties rizika.

---

## Latviesu (lv)

**Hero slogan:** Tavs SoundTouch atkal dzivo. Bez Bose makona.
**Subline:** Interneta radio, prieksiestatijumi un talvadiba SoundTouch 10, 20, 30 un Portable. Lokals, bezmaksas, bez konta, bez makona.
**CTA:** Lejupieladet  /  Ka tas darbojas  /  Windows  -  macOS  -  Linux
**Trust line:** Atvertais kods  -  bez telemetrijas  -  parbaudamas lejupielades

**Card 1, Interneta radio ir atpakal**
Mekle un atskano tukstosiem staciju. Aizstaj slegto Bose TuneIn, bez API atslegas, bez konta.

**Card 2, Spotify Connect [Beta]**
Spotify tiesi skalruni. Pieskir atskanosanas sarakstus pogam un palaid ar vienu spiedienu.
Beta piezime: Nepieciesams Spotify Premium, dazas funkcijas vel tapsana.

**Card 3, Aparaturas pogas no 1 lidz 6**
Skalruna prieksiestatijumu pogas atkal darbojas tapat ka agrak. Pec aukstas palaisanas, gaidstaves vai Wi-Fi parrauma pieskirumi saglabajas.

**Feature, Pilniba lokali, nekas neatstaj tavu tiklu**
Bez makona, bez telemetrijas, bez konta. Audio nonak tiesi skalruni caur UPnP. Katrai iericei lokala TLS CA, izveidota pirmaja palaisana, nekad netiek parraidita.

**Feature, Viena lietotne visam**
Windows, macOS un Linux. Pati atrod tavus skalrunus tikla. Parluko, parvaldi prieksiestatijumus, vadi atskanosanu, viss vienuviet.

**Feature, Vadita pirma uzstadisana ar USB zibatminu**
Lietotne soli pa solim izved tevi cauri iestatisanai. Pec tam viss darbojas ierice, un zibatminu var iznemt.

**Feature, Vienpadsmit valodas**
Saskarne runa anglu, vacu, holandiesu, polu, lietuviesu, latviesu, turku, spanu, francu, ukrainu un japanu valoda. Citas valodas ir gaiditas ka ieguldijums.

**Verify, Parbaudamas lejupielades**
Katrai lejupieladei ir SHA256 summa un Sigstore apliecinajums. Verify lapa redzams precizs klikslu cels Windows SmartScreen un macOS Gatekeeper.

**Disclaimer:** "SoundTouch" un "Bose" ir Bose Corporation registretas precu zimes. STR ir neoficials kopienas projekts, kas nav saistits ar Bose, nav tas atbalstits vai pilnvarots. Lietosana uz pasa atbildibu.

---

## Turkce (tr)

**Hero slogan:** SoundTouch'un yeniden hayatta. Bose bulutu olmadan.
**Subline:** SoundTouch 10, 20, 30 ve Portable icin internet radyosu, on ayarlar ve uzaktan kumanda. Yerel, ucretsiz, hesap yok, bulut yok.
**CTA:** Indir  /  Nasil calisir  /  Windows  -  macOS  -  Linux
**Trust line:** Acik kaynak  -  telemetri yok  -  dogrulanabilir indirmeler

**Card 1, Internet radyosu geri dondu**
Binlerce istasyonu ara ve cal. Kapatilan Bose TuneIn'in yerini alir, API anahtari yok, hesap yok.

**Card 2, Spotify Connect [Beta]**
Spotify dogrudan hoparlorde. Calma listelerini fiziksel tuslara ata, tek dokunusla baslat.
Beta notu: Spotify Premium gerekir, bazi ozellikler hala gelistiriliyor.

**Card 3, Fiziksel tuslar 1 ila 6**
Hoparlordeki on ayar tuslari eskisi gibi yine calisiyor. Soguk baslatma, bekleme, Wi-Fi kesintisi: atamalar kalici.

**Feature, Tamamen yerel, aginizdan hicbir sey cikmaz**
Bulut yok, telemetri yok, hesap yok. Ses, UPnP ile dogrudan hoparlore gider. Her cihaz icin yerel bir TLS CA, ilk acilista olusturulur ve hicbir zaman aktarilmaz.

**Feature, Her sey icin tek uygulama**
Windows, macOS ve Linux. Hoparlorlerini agda kendi bulur. Gozat, on ayarlari yonet, oynatmayi denetle, hepsi tek yerde.

**Feature, USB bellekle rehberli ilk kurulum**
Uygulama kurulumda adim adim sana yol gosterir. Sonrasinda her sey cihazda calisir ve bellegi cikarabilirsin.

**Feature, On bir dil**
Arayuz Ingilizce, Almanca, Felemenkce, Lehce, Litvanca, Letonca, Turkce, Ispanyolca, Fransizca, Ukraynaca ve Japonca konusur. Yeni diller katki olarak memnuniyetle karsilanir.

**Verify, Dogrulanabilir indirmeler**
Her indirmenin bir SHA256 toplami ve bir Sigstore tasdiki vardir. Verify sayfasi Windows SmartScreen ve macOS Gatekeeper icin tam tiklama yolunu gosterir.

**Disclaimer:** "SoundTouch" ve "Bose", Bose Corporation'in tescilli ticari markalaridir. STR resmi olmayan, toplulukca gelistirilen bir projedir; Bose ile baglantili degildir, onaylanmamis ve yetkilendirilmemistir. Kullanim kendi sorumlulugundadir.

---

## Ukrainska (uk)

**Hero slogan:** Tvii SoundTouch znovu zhyvyi. Bez khmary Bose.
(Cyrillic: Твій SoundTouch знову живий. Без хмари Bose.)
**Subline:** Інтернет-радіо, пресети та дистанційне керування для SoundTouch 10, 20, 30 і Portable. Локально, безкоштовно, без облікового запису, без хмари.
**CTA:** Завантажити  /  Як це працює  /  Windows  -  macOS  -  Linux
**Trust line:** Відкритий код  -  без телеметрії  -  перевірені завантаження

**Card 1, Інтернет-радіо повернулося**
Шукай і відтворюй тисячі станцій. Замінює вимкнений Bose TuneIn, без API-ключа, без облікового запису.

**Card 2, Spotify Connect [Beta]**
Spotify прямо на колонці. Признач плейлисти на апаратні кнопки й запускай одним натисканням.
Beta-примітка: Потрібен Spotify Premium, деякі функції ще в розробці.

**Card 3, Апаратні кнопки 1 до 6**
Кнопки пресетів на колонці знову працюють, як раніше. Холодний старт, очікування, збій Wi-Fi: призначення зберігаються.

**Feature, Повністю локально, ніщо не залишає твою мережу**
Без хмари, без телеметрії, без облікового запису. Аудіо йде прямо на колонку через UPnP. На кожен пристрій локальний TLS CA, створений під час першого запуску, ніколи не передається.

**Feature, Один застосунок для всього**
Windows, macOS і Linux. Сам знаходить твої колонки в мережі. Переглядай, керуй пресетами та відтворенням, усе в одному місці.

**Feature, Кероване перше встановлення через USB-флешку**
Застосунок крок за кроком проведе тебе через налаштування. Далі все працює на пристрої, а флешку можна вийняти.

**Feature, Одинадцять мов**
Інтерфейс володіє англійською, німецькою, нідерландською, польською, литовською, латиською, турецькою, іспанською, французькою, українською та японською. Інші мови вітаються як внесок.

**Verify, Перевірені завантаження**
Кожне завантаження має суму SHA256 та підтвердження Sigstore. Сторінка Verify показує точний шлях кліків для Windows SmartScreen і macOS Gatekeeper.

**Disclaimer:** «SoundTouch» і «Bose» є зареєстрованими торговими марками Bose Corporation. STR є неофіційним проєктом спільноти, не пов'язаним із Bose, не схваленим і не авторизованим Bose. Використання на власний ризик.

---

## Nihongo (ja)

**Hero slogan:** あなたのSoundTouchが、また動き出す。Boseクラウドなしで。
**Subline:** SoundTouch 10、20、30、Portable のためのインターネットラジオ、プリセット、リモート操作。ローカルで動作、無料、アカウント不要、クラウド不要。
**CTA:** ダウンロード  /  使い方  /  Windows  -  macOS  -  Linux
**Trust line:** オープンソース  -  テレメトリなし  -  検証可能なダウンロード

**Card 1, インターネットラジオが復活**
何千もの放送局を検索して再生。終了した Bose TuneIn の代わりに、APIキーもアカウントも不要。

**Card 2, Spotify Connect [Beta]**
Spotify をスピーカーに直接。プレイリストを本体のボタンに割り当て、ワンプッシュで再生。
ベータ注記: Spotify Premium が必要です。一部の機能は開発中です。

**Card 3, 本体のボタン 1〜6**
本体のプリセットボタンが以前と同じように再び使えます。コールドブート、スタンバイ、Wi-Fi 切断のあとも割り当ては保持。

**Feature, 完全にローカル、ネットワークの外に何も出ません**
クラウドなし、テレメトリなし、アカウントなし。音声は UPnP でスピーカーへ直接。デバイスごとにローカル TLS CA を初回起動時に生成し、外部へ送信しません。

**Feature, すべてをこなす一つのアプリ**
Windows、macOS、Linux 対応。ネットワーク上のスピーカーを自動で見つけます。閲覧、プリセット管理、再生操作をひとつの場所で。

**Feature, USB メモリーによるガイド付き初期設定**
アプリが設定を一歩ずつ案内します。完了後はすべてデバイス上で動作し、USB メモリーは抜けます。

**Feature, 11の言語**
インターフェースは英語、ドイツ語、オランダ語、ポーランド語、リトアニア語、ラトビア語、トルコ語、スペイン語、フランス語、ウクライナ語、日本語に対応。他の言語も貢献として歓迎します。

**Verify, 検証可能なダウンロード**
すべてのダウンロードに SHA256 ハッシュと Sigstore の証明が付きます。Verify ページに Windows SmartScreen と macOS Gatekeeper の正確な操作手順を掲載しています。

**Disclaimer:** 「SoundTouch」および「Bose」は Bose Corporation の登録商標です。STR は非公式のコミュニティプロジェクトであり、Bose と提携・承認・許可された関係にはありません。ご利用は自己責任でお願いします。
