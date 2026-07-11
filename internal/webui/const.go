package webui

import (
	_ "embed"
	"time"
)

const shutdownTimeout = 5 * time.Second

// iconPNG is the STR app icon, served at /icon.png for the favicon, the iOS
// apple-touch-icon and the PWA manifest, so a phone that saves the page to its
// home screen gets a proper STR icon.
//
//go:embed assets/icon.png
var iconPNG []byte

// indexHTML is the self-contained controller page the agent serves on "/". It is
// the phone remote: a mobile-first page (no desktop app needed) that drives the
// box over the same REST API the desktop app uses. It is PWA-capable (save to
// home screen), shows volume + input + presets + transport, links to the other
// STR speakers on the network, and is branded as ST Reborn.
const indexHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="theme-color" content="#1a1a1a">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-status-bar-style" content="black-translucent">
<meta name="apple-mobile-web-app-title" content="ST Reborn">
<link rel="manifest" href="/manifest.webmanifest">
<link rel="icon" href="/icon.png">
<link rel="apple-touch-icon" href="/icon.png">
<title>ST Reborn</title>
<style>
:root {
  color-scheme:dark;
  --bg:#1a1a1a; --card:#242424; --card2:#2a2a2a; --line:#3a3a3a; --fg:#eee; --muted:#9e9e9e; --accent:#e88;
  --hover:#333; --press:#3d3d3d; --nowgrad1:#2c2c2c; --nowgrad2:#242424;
}
/* Light theme: the greyscale tokens flip to a light palette and the coral
   accent is darkened so it keeps contrast on white. color-scheme:light also
   makes the native volume slider track render light. */
html.a11y-light {
  color-scheme:light;
  --bg:#f4f4f5; --card:#ffffff; --card2:#ececef; --line:#d4d4d8; --fg:#1a1a1a; --muted:#5a5e66; --accent:#bd3c2c;
  --hover:#e6e6ea; --press:#dadade; --nowgrad1:#ffffff; --nowgrad2:#f0f0f3;
}
/* High-contrast theme: black base, pure-white text and borders, a bright
   accent. Also seeded automatically on first run when the OS asks for more
   contrast (prefers-contrast: more). */
html.a11y-contrast {
  --bg:#000; --card:#000; --card2:#000; --line:#fff; --fg:#fff; --muted:#fff; --accent:#ffe066;
  --hover:#1a1a1a; --press:#333; --nowgrad1:#000; --nowgrad2:#000;
}
/* Text size. The page uses pixel sizes throughout, so scaling root font-size
   would not reach them; zoom scales the whole rendered page (text, controls,
   hit targets) uniformly. */
html.a11y-scale-l  body { zoom:1.15; }
html.a11y-scale-xl body { zoom:1.30; }
* { box-sizing: border-box; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
:focus-visible { outline:2px solid var(--accent); outline-offset:2px; }
html.a11y-contrast :focus-visible { outline-color:#fff; }
/* Pad every edge by the device safe-area inset on top of the base 16px. The
   page is PWA "standalone" with a black-translucent status bar, so on a notch /
   Dynamic Island phone (e.g. iPhone) iOS draws the content behind the status
   bar; without the top inset the header collides with the clock. The insets are
   0 on hardware that has none, so the base 16px is unchanged there. */
body { margin:0; padding:calc(16px + env(safe-area-inset-top)) calc(16px + env(safe-area-inset-right)) calc(16px + env(safe-area-inset-bottom)) calc(16px + env(safe-area-inset-left)); background:var(--bg); color:var(--fg); max-width:620px; margin:0 auto; }
header { display:flex; align-items:center; gap:10px; margin-bottom:14px; }
header img { width:30px; height:30px; border-radius:7px; }
header .brand { font-size:18px; font-weight:700; letter-spacing:.2px; }
header .brand span { color:var(--accent); }
/* The speaker name is the page's identity (one saved app per speaker), so it
   reads as a headline: bright, semi-bold, and it WRAPS instead of getting cut
   off - multi-word room names were ellipsized before. */
header .dev { margin-left:auto; min-width:0; font-size:15px; font-weight:600; color:var(--fg); text-align:right; overflow-wrap:anywhere; line-height:1.25; }
.card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:12px; margin:12px 0; }
.nowcard { padding:14px 16px; background:linear-gradient(180deg,var(--nowgrad1),var(--nowgrad2)); }
.nowcard .now { display:block; color:var(--accent); font-weight:600; font-size:18px; line-height:1.25; }
.nowcard .st { font-size:13px; color:var(--muted); margin-top:3px; }
.nowcard.loading { opacity:.6; }
@media (prefers-reduced-motion:no-preference) { .nowcard.loading { animation:pulse 1.2s ease-in-out infinite; } @keyframes pulse { 50% { opacity:.3; } } }
.label { font-size:11px; text-transform:uppercase; letter-spacing:.5px; color:var(--muted); margin-bottom:8px; }
.row { display:grid; gap:8px; }
.row.c2 { grid-template-columns:1fr 1fr; }
.row.c3 { grid-template-columns:1fr 1fr 1fr; }
button.btn, a.btn { display:flex; align-items:center; justify-content:center; min-height:44px; background:var(--card2); color:var(--fg); border:1px solid var(--line); border-radius:8px; padding:10px; font-size:14px; cursor:pointer; text-decoration:none; transition:background .15s,border-color .15s,color .15s; }
button.btn.active, a.btn.active { border-color:var(--accent); color:var(--accent); }
button.btn:active { background:var(--press); }
@media (hover:hover) { button.btn:hover, a.btn:hover { background:var(--hover); } .preset:hover { background:var(--hover); } }
.vol { display:flex; align-items:center; gap:12px; }
.vol input[type=range] { flex:1; accent-color:var(--accent); height:44px; }
.vol input[type=range]::-webkit-slider-thumb { -webkit-appearance:none; width:24px; height:24px; border-radius:50%; background:var(--accent); }
.vol input[type=range]::-moz-range-thumb { width:24px; height:24px; border:0; border-radius:50%; background:var(--accent); }
.vol .val { width:36px; text-align:right; font-variant-numeric:tabular-nums; color:var(--fg); }
.grid { display:grid; grid-template-columns:repeat(2,1fr); gap:8px; }
.preset { background:var(--card2); border:1px solid var(--line); border-radius:10px; padding:14px; cursor:pointer; min-height:80px; display:flex; flex-direction:column; justify-content:center; transition:background .15s; }
.preset:active { background:var(--press); }
.preset:focus-visible { outline-offset:-2px; }
.preset .num { font-size:11px; color:var(--muted); }
.preset .name { font-size:15px; font-weight:600; margin-top:4px; }
.preset.empty { color:var(--muted); border-style:dashed; cursor:default; }
.preset.active { border-color:transparent; box-shadow:0 0 0 2px var(--accent) inset; }
.preset.active .num { color:var(--accent); }
#peersCard { display:none; }
.peer { display:flex; align-items:center; gap:8px; }
.peer .dot { width:8px; height:8px; border-radius:50%; background:var(--accent); flex:none; }
.sponsors { display:grid; grid-template-columns:repeat(3,1fr); gap:8px; max-width:340px; margin:0 auto 8px; }
.sponsors a.btn { min-height:40px; font-size:13px; background:transparent; border-color:var(--line); color:var(--muted); font-weight:500; }
@media (hover:hover) { .sponsors a.btn:hover { background:var(--card2); color:var(--fg); } }
footer { margin-top:18px; text-align:center; font-size:12px; color:var(--muted); }
footer .web { display:inline-block; margin-top:4px; color:var(--accent); text-decoration:none; }
footer .web:hover { text-decoration:underline; }
footer .ver { display:block; margin-top:8px; }
footer .hint { display:block; margin-top:6px; color:var(--muted); opacity:.7; }
/* Power on/off toggle, pinned to the header's right next to "Aa". */
.pwr { display:inline-flex; align-items:center; justify-content:center; flex:none; min-height:36px; min-width:40px; padding:6px 10px; margin-right:8px; background:var(--card2); color:var(--muted); border:1px solid var(--line); border-radius:8px; cursor:pointer; transition:background .15s,border-color .15s,color .15s; }
.pwr svg { display:block; }
.pwr.on { border-color:var(--accent); color:var(--accent); }
.pwr.active { background:var(--press); }
.pwr:active { background:var(--press); }
@media (hover:hover) { .pwr:hover { background:var(--hover); } }
/* "Aa" display-options menu (text size + theme), pinned to the header's right. */
.a11y { position:relative; flex:none; }
.a11y-trigger { display:inline-flex; align-items:center; min-height:36px; padding:6px 11px; background:var(--card2); color:var(--fg); border:1px solid var(--line); border-radius:8px; font-size:15px; font-weight:700; letter-spacing:-.5px; cursor:pointer; }
.a11y-trigger:active { background:var(--press); }
.a11y-menu { position:absolute; top:calc(100% + 6px); right:0; z-index:50; padding:12px; min-width:228px; background:var(--card); border:1px solid var(--line); border-radius:10px; box-shadow:0 10px 30px rgba(0,0,0,.5); }
.a11y-menu[hidden] { display:none; }
.a11y-group + .a11y-group { margin-top:12px; }
.a11y-glabel { font-size:11px; text-transform:uppercase; letter-spacing:.5px; color:var(--muted); margin-bottom:6px; }
.a11y-seg { display:flex; gap:6px; }
.a11y-seg button { flex:1; min-height:44px; padding:8px 6px; background:var(--card2); color:var(--fg); border:1px solid var(--line); border-radius:8px; font-size:13px; line-height:1.15; cursor:pointer; }
.a11y-seg button[aria-pressed="true"] { border-color:var(--accent); color:var(--accent); font-weight:600; }
@media (hover:hover) { .a11y-trigger:hover, .a11y-seg button:hover { background:var(--hover); } }
/* Pull-to-refresh indicator: a home-screen PWA has no browser reload button,
   so a pull down from the top re-fetches everything. */
#ptr { position:fixed; top:env(safe-area-inset-top); left:0; right:0; display:flex; justify-content:center; z-index:60; pointer-events:none; }
.ptr-spin { margin-top:8px; width:22px; height:22px; border-radius:50%; border:2.5px solid var(--line); border-top-color:var(--accent); opacity:0; transform:translateY(-40px); }
#ptr.snap .ptr-spin { transition:transform .25s ease, opacity .25s ease; }
#ptr.armed .ptr-spin { border-color:var(--accent); }
#ptr.spinning .ptr-spin { opacity:1; transform:none; animation:ptr-rot .8s linear infinite; }
@keyframes ptr-rot { from { transform:rotate(0); } to { transform:rotate(360deg); } }
/* Respect the OS "reduce motion" setting. */
@media (prefers-reduced-motion:reduce) { *, *::before, *::after { animation-duration:.001ms !important; animation-iteration-count:1 !important; transition-duration:.001ms !important; } }
</style>
<script>
// Display preferences (text size + theme). Applied to <html> here in <head>,
// before the body paints, so the chosen theme/size is in effect on first paint
// with no flash. Stored in localStorage (a phone-local UI preference, like the
// language), so it costs the speaker nothing. On first run the theme is seeded
// from the OS accessibility settings.
(function(){
  var SCALE_KEY='a11yScale', THEME_KEY='a11yTheme';
  function osTheme(){
    try {
      if (window.matchMedia('(prefers-contrast: more)').matches) return 'contrast';
      if (window.matchMedia('(prefers-color-scheme: light)').matches) return 'light';
    } catch (e) {}
    return 'dark';
  }
  window.a11yGetScale=function(){
    try { var n=Number(localStorage.getItem(SCALE_KEY)); if (n===2||n===3) return n; } catch (e) {}
    return 1;
  };
  window.a11yGetTheme=function(){
    try { var v=localStorage.getItem(THEME_KEY); if (v==='dark'||v==='light'||v==='contrast') return v; } catch (e) {}
    return osTheme();
  };
  window.a11yApply=function(){
    var el=document.documentElement;
    el.classList.remove('a11y-scale-l','a11y-scale-xl','a11y-light','a11y-contrast');
    var s=window.a11yGetScale();
    if (s===2) el.classList.add('a11y-scale-l'); else if (s===3) el.classList.add('a11y-scale-xl');
    var t=window.a11yGetTheme();
    if (t==='light') el.classList.add('a11y-light'); else if (t==='contrast') el.classList.add('a11y-contrast');
    // Keep the mobile browser chrome / PWA status bar in step with the theme.
    var meta=document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', t==='light' ? '#f4f4f5' : (t==='contrast' ? '#000000' : '#1a1a1a'));
  };
  window.a11ySetScale=function(n){ try { localStorage.setItem(SCALE_KEY,String(n)); } catch (e) {} window.a11yApply(); };
  window.a11ySetTheme=function(t){ try { localStorage.setItem(THEME_KEY,t); } catch (e) {} window.a11yApply(); };
  window.a11yApply();
})();
</script>
</head>
<body>
<div id="ptr" aria-hidden="true"><div class="ptr-spin"></div></div>
<header>
<img src="/icon.png" alt="STR">
<div class="brand">ST <span>Reborn</span></div>
<div class="dev" id="dev"></div>
<button type="button" class="pwr" id="powerBtn" onclick="togglePower()" aria-label="Power" aria-pressed="true" title="Power"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><line x1="12" y1="2.5" x2="12" y2="12"></line><path d="M7.5 6.3a7 7 0 1 0 9 0"></path></svg></button>
<div class="a11y">
<button type="button" class="a11y-trigger" id="a11yTrigger" aria-haspopup="dialog" aria-expanded="false" aria-label="Display &amp; accessibility" title="Display &amp; accessibility">Aa</button>
<div class="a11y-menu" id="a11yMenu" role="dialog" aria-label="Display &amp; accessibility" hidden>
<div class="a11y-group">
<div class="a11y-glabel" id="a11ySizeLabel">Text size</div>
<div class="a11y-seg" role="group" aria-labelledby="a11ySizeLabel">
<button type="button" data-scale="1" aria-pressed="true">Normal</button>
<button type="button" data-scale="2" aria-pressed="false">Large</button>
<button type="button" data-scale="3" aria-pressed="false">Extra large</button>
</div>
</div>
<div class="a11y-group">
<div class="a11y-glabel" id="a11yThemeLabel">Theme</div>
<div class="a11y-seg" role="group" aria-labelledby="a11yThemeLabel">
<button type="button" data-theme="dark" aria-pressed="true">Dark</button>
<button type="button" data-theme="light" aria-pressed="false">Light</button>
<button type="button" data-theme="contrast" aria-pressed="false">High contrast</button>
</div>
</div>
</div>
</div>
</header>

<main>
<div class="card" id="wedgeCard" style="display:none; border-color:#d97706;">
<div class="label" id="lblWedge">Speaker not responding</div>
<div id="wedgeText" style="font-size:13px; line-height:1.4;"></div>
</div>

<div class="card nowcard loading" id="statusCard">
<div class="label" id="lblNow">Now playing</div>
<div id="status"><span class="now">Loading&hellip;</span></div>
</div>

<div class="card">
<div class="label" id="lblVol">Volume</div>
<div class="vol"><input type="range" id="vol" min="0" max="100" value="0" aria-label="Volume" oninput="onVol(this.value)"><span class="val" id="volval">0</span></div>
</div>

<div class="card">
<div class="label" id="lblInput">Input</div>
<div class="row c2" id="inputs">
<button class="btn" onclick="setSource('BLUETOOTH',this)">Bluetooth</button>
<button class="btn" onclick="setSource('AUX',this)">AUX</button>
</div>
</div>

<div class="card">
<div class="label" id="lblPlayback">Playback</div>
<div class="row c2" style="margin-bottom:8px">
<button class="btn" id="btnPrev" onclick="skip(this,'/api/prev')" style="gap:6px" aria-label="Previous" title="Previous"><span aria-hidden="true" style="font-size:19px">&#9198;</span><span id="btnPrevLbl">Previous</span></button>
<button class="btn" id="btnNext" onclick="skip(this,'/api/next')" style="gap:6px" aria-label="Next" title="Next"><span id="btnNextLbl">Next</span><span aria-hidden="true" style="font-size:19px">&#9197;</span></button>
</div>
<div class="row c2">
<button class="btn" id="btnPause" onclick="togglePlayPause(this)" style="gap:6px" aria-label="Pause" title="Pause"><span id="btnPauseIcon" aria-hidden="true" style="font-size:19px">&#9208;</span><span id="btnPauseLbl">Pause</span></button>
<button class="btn" id="btnStop" onclick="pp(this,'/api/stop')" style="gap:6px" aria-label="Stop" title="Stop"><span aria-hidden="true" style="font-size:19px">&#9209;</span><span id="btnStopLbl">Stop</span></button>
</div>
</div>

<div class="label" id="lblPresets" style="margin:18px 12px 8px">Presets</div>
<div class="grid" id="presets"></div>

<div class="card" id="peersCard">
<div class="label" id="lblPeers">Other speakers</div>
<div class="row" id="peers"></div>
</div>

<div class="card" id="bassCard" style="display:none">
<div class="label" id="lblBass">Bass</div>
<div class="vol"><input type="range" id="bass" min="-9" max="0" value="0" aria-label="Bass" oninput="onBass(this.value)"><span class="val" id="bassval">0</span></div>
</div>
</main>

<footer>
<div class="label" id="lblSupport" style="margin-bottom:8px">You like ST Reborn?</div>
<div class="sponsors">
<a class="btn" href="https://github.com/sponsors/JRpersonal" target="_blank" rel="noopener">&#9829; GitHub</a>
<a class="btn" href="https://ko-fi.com/streborn" target="_blank" rel="noopener">&#9749; Ko-fi</a>
<a class="btn" href="https://paypal.me/JR31337" target="_blank" rel="noopener">PayPal</a>
</div>
<button class="btn" id="shareTrigger" onclick="toggleShare(this)" aria-expanded="false" style="width:100%;margin-top:10px;border-style:dashed;border-color:var(--accent);color:var(--accent);gap:6px">&#9829; <span id="lblShareTrigger">Share ST Reborn</span> <span id="shareChev">&#9662;</span></button>
<div id="shareRow" style="display:none;margin-top:8px">
<div class="label" id="lblShareHead" style="margin-bottom:8px">…and if you really love it?</div>
<div class="sponsors" id="shareBtns"></div>
</div>
<a class="web" href="https://st-reborn.de" target="_blank" rel="noopener">st-reborn.de</a>
<span class="ver" id="ver"></span>
<span class="hint" id="lblTip">Tip: use your browser menu and "Add to Home Screen" to keep this as an app.</span>
</footer>

<script>
// Page localization. The whole remote is translated client-side from the phone's
// language (navigator.languages), so it costs the speaker nothing: the strings
// ship inside this one embedded page and never touch the box. English is the
// fallback for any locale or key we don't cover. Brand names (ST Reborn,
// Bluetooth, AUX) and the box's own device name stay as-is. Keep this language
// set in step with the "Aa" menu dictionary (A11Y_I18N) below.
var I18N = {
  en:{now:"Now playing",loading:"Loading…",vol:"Volume",bass:"Bass",wedgeTitle:"Speaker not responding",wedgeText:"The speaker is not accepting playback right now. Unplug it from power for a moment, plug it back in, and it will work normally again.",input:"Input",standby:"Standby",playback:"Playback",pause:"Pause",play:"Play",stop:"Stop",presets:"Presets",peers:"Other speakers",support:"Support ST Reborn",tip:"Tip: use your browser menu and \"Add to Home Screen\" to keep this as an app.",empty:"empty",presetWord:"Preset",starting:"Starting",pleaseWait:"please wait",cantStart:"Could not start",tapAgain:"tap again",idle:"Idle",playing:"Playing",paused:"Paused",stopped:"Stopped",buffering:"Buffering",power:"Power",prev:"Previous",next:"Next",shareDonate:"You like ST Reborn?",shareTrigger:"Share ST Reborn",shareHead:"…and if you really love it?",shareMsg:"ST Reborn revives my Bose SoundTouch without the Bose cloud."},
  de:{now:"Wird gespielt",loading:"Lädt…",vol:"Lautstärke",bass:"Bass",wedgeTitle:"Lautsprecher reagiert nicht",wedgeText:"Der Lautsprecher nimmt gerade keine Wiedergabe an. Zieh kurz den Netzstecker, steck ihn wieder ein - danach funktioniert er wieder normal.",input:"Eingang",standby:"Standby",playback:"Wiedergabe",pause:"Pause",play:"Wiedergabe",stop:"Stopp",presets:"Voreinstellungen",peers:"Andere Lautsprecher",support:"ST Reborn unterstützen",tip:"Tipp: Über das Browser-Menü und „Zum Home-Bildschirm“ als App speichern.",empty:"leer",presetWord:"Voreinstellung",starting:"Starte",pleaseWait:"bitte warten",cantStart:"Start fehlgeschlagen",tapAgain:"nochmal tippen",idle:"Bereit",playing:"Wiedergabe",paused:"Pausiert",stopped:"Gestoppt",buffering:"Puffert",power:"Ein/Aus",prev:"Zurück",next:"Weiter",shareDonate:"Dir gefällt STR?",shareTrigger:"STR teilen",shareHead:"…und wenn es dir so richtig gefällt?",shareMsg:"ST Reborn erweckt meine Bose SoundTouch ohne die Bose-Cloud wieder zum Leben."},
  nl:{now:"Speelt nu",loading:"Laden…",vol:"Volume",bass:"Bas",wedgeTitle:"Speaker reageert niet",wedgeText:"De speaker accepteert momenteel geen weergave. Haal de stekker even uit het stopcontact en steek hem er weer in - daarna werkt hij weer normaal.",input:"Ingang",standby:"Stand-by",playback:"Afspelen",pause:"Pauze",play:"Afspelen",stop:"Stop",presets:"Presets",peers:"Andere speakers",support:"Steun ST Reborn",tip:"Tip: gebruik het browsermenu en \"Zet op beginscherm\" om dit als app te bewaren.",empty:"leeg",presetWord:"Preset",starting:"Starten",pleaseWait:"even geduld",cantStart:"Kan niet starten",tapAgain:"tik opnieuw",idle:"Inactief",playing:"Speelt af",paused:"Gepauzeerd",stopped:"Gestopt",buffering:"Bufferen",power:"Aan/uit",prev:"Vorige",next:"Volgende",shareDonate:"Vind je ST Reborn leuk?",shareTrigger:"STR delen",shareHead:"…en als je het echt geweldig vindt?",shareMsg:"ST Reborn brengt mijn Bose SoundTouch weer tot leven zonder de Bose-cloud."},
  fr:{now:"Lecture en cours",loading:"Chargement…",vol:"Volume",bass:"Graves",wedgeTitle:"L'enceinte ne répond pas",wedgeText:"L'enceinte n'accepte pas la lecture pour le moment. Débranchez-la un instant, rebranchez-la, et elle fonctionnera à nouveau normalement.",input:"Entrée",standby:"Veille",playback:"Lecture",pause:"Pause",play:"Lecture",stop:"Arrêt",presets:"Préréglages",peers:"Autres enceintes",support:"Soutenir ST Reborn",tip:"Astuce : utilisez le menu du navigateur et « Ajouter à l'écran d'accueil » pour garder ceci comme une app.",empty:"vide",presetWord:"Préréglage",starting:"Démarrage",pleaseWait:"veuillez patienter",cantStart:"Démarrage impossible",tapAgain:"appuyez à nouveau",idle:"Inactif",playing:"Lecture",paused:"En pause",stopped:"Arrêté",buffering:"Mise en mémoire tampon",power:"Marche/Arrêt",prev:"Précédent",next:"Suivant",shareDonate:"Vous aimez ST Reborn ?",shareTrigger:"Partager STR",shareHead:"…et si vous l'adorez vraiment ?",shareMsg:"ST Reborn redonne vie à mes Bose SoundTouch sans le cloud Bose."},
  es:{now:"Reproduciendo",loading:"Cargando…",vol:"Volumen",bass:"Graves",wedgeTitle:"El altavoz no responde",wedgeText:"El altavoz no acepta reproducción ahora mismo. Desenchúfalo un momento, vuelve a enchufarlo y funcionará con normalidad.",input:"Entrada",standby:"Reposo",playback:"Reproducción",pause:"Pausa",play:"Reproducir",stop:"Detener",presets:"Presintonías",peers:"Otros altavoces",support:"Apoya ST Reborn",tip:"Consejo: usa el menú del navegador y «Añadir a pantalla de inicio» para conservarlo como app.",empty:"vacío",presetWord:"Presintonía",starting:"Iniciando",pleaseWait:"espera",cantStart:"No se pudo iniciar",tapAgain:"toca de nuevo",idle:"Inactivo",playing:"Reproduciendo",paused:"En pausa",stopped:"Detenido",buffering:"Almacenando en búfer",power:"Encendido",prev:"Anterior",next:"Siguiente",shareDonate:"¿Te gusta ST Reborn?",shareTrigger:"Compartir STR",shareHead:"…¿y si te encanta de verdad?",shareMsg:"ST Reborn revive mis Bose SoundTouch sin la nube de Bose."},
  pl:{now:"Teraz odtwarzane",loading:"Ładowanie…",vol:"Głośność",bass:"Basy",wedgeTitle:"Głośnik nie odpowiada",wedgeText:"Głośnik obecnie nie przyjmuje odtwarzania. Odłącz go na chwilę od zasilania i podłącz ponownie - potem będzie działał normalnie.",input:"Wejście",standby:"Czuwanie",playback:"Odtwarzanie",pause:"Pauza",play:"Odtwórz",stop:"Stop",presets:"Presety",peers:"Inne głośniki",support:"Wesprzyj ST Reborn",tip:"Wskazówka: użyj menu przeglądarki i „Dodaj do ekranu głównego”, aby zachować to jako aplikację.",empty:"puste",presetWord:"Preset",starting:"Uruchamianie",pleaseWait:"proszę czekać",cantStart:"Nie udało się uruchomić",tapAgain:"dotknij ponownie",idle:"Bezczynny",playing:"Odtwarzanie",paused:"Wstrzymano",stopped:"Zatrzymano",buffering:"Buforowanie",power:"Zasilanie",prev:"Poprzedni",next:"Następny",shareDonate:"Podoba Ci się ST Reborn?",shareTrigger:"Udostępnij STR",shareHead:"…a jeśli naprawdę je pokochasz?",shareMsg:"ST Reborn przywraca moje Bose SoundTouch do życia bez chmury Bose."},
  tr:{now:"Şimdi çalıyor",loading:"Yükleniyor…",vol:"Ses",bass:"Bas",wedgeTitle:"Hoparlör yanıt vermiyor",wedgeText:"Hoparlör şu anda oynatmayı kabul etmiyor. Fişini kısa süre çekip tekrar takın - ardından normal çalışır.",input:"Giriş",standby:"Bekleme",playback:"Oynatma",pause:"Duraklat",play:"Oynat",stop:"Durdur",presets:"Ön ayarlar",peers:"Diğer hoparlörler",support:"ST Reborn'a destek ol",tip:"İpucu: tarayıcı menüsünü ve \"Ana Ekrana Ekle\" seçeneğini kullanarak bunu uygulama olarak tutun.",empty:"boş",presetWord:"Ön ayar",starting:"Başlatılıyor",pleaseWait:"lütfen bekleyin",cantStart:"Başlatılamadı",tapAgain:"tekrar dokunun",idle:"Boşta",playing:"Çalıyor",paused:"Duraklatıldı",stopped:"Durduruldu",buffering:"Arabelleğe alınıyor",power:"Güç",prev:"Önceki",next:"Sonraki",shareDonate:"ST Reborn'u beğendin mi?",shareTrigger:"STR'yi paylaş",shareHead:"…ya gerçekten bayıldıysan?",shareMsg:"ST Reborn, Bose SoundTouch'ımı Bose bulutu olmadan yeniden hayata döndürüyor."},
  ar:{now:"قيد التشغيل الآن",loading:"جارٍ التحميل…",vol:"مستوى الصوت",bass:"الجهير",wedgeTitle:"السمّاعة لا تستجيب",wedgeText:"السمّاعة لا تقبل التشغيل حالياً. افصلها عن الكهرباء للحظة ثم أعد توصيلها، وستعمل بشكل طبيعي مجدداً.",input:"المدخل",standby:"وضع الاستعداد",playback:"التشغيل",pause:"إيقاف مؤقت",play:"تشغيل",stop:"إيقاف",presets:"الإعدادات المسبقة",peers:"مكبرات صوت أخرى",support:"ادعم ST Reborn",tip:"نصيحة: استخدم قائمة المتصفح و\"إضافة إلى الشاشة الرئيسية\" للاحتفاظ بهذا كتطبيق.",empty:"فارغ",presetWord:"إعداد مسبق",starting:"جارٍ البدء",pleaseWait:"يرجى الانتظار",cantStart:"تعذّر البدء",tapAgain:"انقر مرة أخرى",idle:"خامل",playing:"قيد التشغيل",paused:"متوقف مؤقتًا",stopped:"متوقف",buffering:"جارٍ التخزين المؤقت",power:"الطاقة",prev:"السابق",next:"التالي",shareDonate:"هل يعجبك ST Reborn؟",shareTrigger:"مشاركة STR",shareHead:"…وإذا أعجبك حقًا؟",shareMsg:"‏ST Reborn يعيد إحياء مكبرات Bose SoundTouch لديّ دون سحابة Bose."},
  ja:{now:"再生中",loading:"読み込み中…",vol:"音量",bass:"低音",wedgeTitle:"スピーカーが応答しません",wedgeText:"スピーカーが再生を受け付けていません。電源プラグを一度抜いて挿し直すと、通常どおり動作します。",input:"入力",standby:"スタンバイ",playback:"再生",pause:"一時停止",play:"再生",stop:"停止",presets:"プリセット",peers:"他のスピーカー",support:"ST Reborn を支援",tip:"ヒント：ブラウザのメニューから「ホーム画面に追加」を使うと、アプリとして保存できます。",empty:"空き",presetWord:"プリセット",starting:"開始中",pleaseWait:"お待ちください",cantStart:"開始できませんでした",tapAgain:"もう一度タップ",idle:"待機中",playing:"再生中",paused:"一時停止中",stopped:"停止",buffering:"バッファ中",power:"電源",prev:"前へ",next:"次へ",shareDonate:"ST Reborn は気に入りましたか？",shareTrigger:"STR をシェア",shareHead:"…そして本当に気に入ったら？",shareMsg:"ST Reborn は Bose クラウドなしで私の Bose SoundTouch を復活させます。"},
  lt:{now:"Dabar grojama",loading:"Įkeliama…",vol:"Garsumas",bass:"Žemieji dažniai",wedgeTitle:"Garsiakalbis nereaguoja",wedgeText:"Garsiakalbis šiuo metu nepriima atkūrimo. Trumpam ištraukite maitinimo kištuką ir vėl įkiškite - tada jis vėl veiks normaliai.",input:"Įvestis",standby:"Budėjimas",playback:"Atkūrimas",pause:"Pristabdyti",play:"Groti",stop:"Stabdyti",presets:"Išankstiniai nustatymai",peers:"Kitos kolonėlės",support:"Paremkite ST Reborn",tip:"Patarimas: naudokite naršyklės meniu ir „Pridėti į pradžios ekraną“, kad išsaugotumėte tai kaip programėlę.",empty:"tuščia",presetWord:"Nustatymas",starting:"Paleidžiama",pleaseWait:"palaukite",cantStart:"Nepavyko paleisti",tapAgain:"bakstelėkite dar kartą",idle:"Neaktyvus",playing:"Grojama",paused:"Pristabdyta",stopped:"Sustabdyta",buffering:"Buferiuojama",power:"Maitinimas",prev:"Ankstesnis",next:"Kitas",shareDonate:"Patinka ST Reborn?",shareTrigger:"Dalytis STR",shareHead:"…o jei tikrai jį pamėgai?",shareMsg:"ST Reborn atgaivina mano Bose SoundTouch be Bose debesies."},
  lv:{now:"Tagad atskaņo",loading:"Ielādē…",vol:"Skaļums",bass:"Basi",wedgeTitle:"Skaļrunis nereaģē",wedgeText:"Skaļrunis pašlaik nepieņem atskaņošanu. Uz brīdi atvienojiet to no strāvas un pievienojiet atkal - pēc tam tas darbosies normāli.",input:"Ievade",standby:"Gaidstāve",playback:"Atskaņošana",pause:"Pauze",play:"Atskaņot",stop:"Apturēt",presets:"Iepriekšiestatījumi",peers:"Citi skaļruņi",support:"Atbalstīt ST Reborn",tip:"Padoms: izmantojiet pārlūka izvēlni un \"Pievienot sākuma ekrānam\", lai saglabātu to kā lietotni.",empty:"tukšs",presetWord:"Iestatījums",starting:"Sākas",pleaseWait:"lūdzu, uzgaidiet",cantStart:"Neizdevās sākt",tapAgain:"pieskarieties vēlreiz",idle:"Dīkstāvē",playing:"Atskaņo",paused:"Pauzēts",stopped:"Apturēts",buffering:"Buferē",power:"Barošana",prev:"Iepriekšējais",next:"Nākamais",shareDonate:"Vai tev patīk ST Reborn?",shareTrigger:"Kopīgot STR",shareHead:"…un ja tas tev tiešām patīk?",shareMsg:"ST Reborn atdzīvina manas Bose SoundTouch bez Bose mākoņa."},
  uk:{now:"Зараз грає",loading:"Завантаження…",vol:"Гучність",bass:"Баси",wedgeTitle:"Динамік не відповідає",wedgeText:"Динамік зараз не приймає відтворення. На мить вимкніть його з розетки та увімкніть знову - після цього він працюватиме нормально.",input:"Вхід",standby:"Очікування",playback:"Відтворення",pause:"Пауза",play:"Відтворити",stop:"Стоп",presets:"Пресети",peers:"Інші колонки",support:"Підтримати ST Reborn",tip:"Порада: скористайтеся меню браузера та «Додати на головний екран», щоб зберегти це як застосунок.",empty:"порожньо",presetWord:"Пресет",starting:"Запуск",pleaseWait:"зачекайте",cantStart:"Не вдалося запустити",tapAgain:"торкніться ще раз",idle:"Очікування",playing:"Відтворення",paused:"Призупинено",stopped:"Зупинено",buffering:"Буферизація",power:"Живлення",prev:"Назад",next:"Далі",shareDonate:"Тобі подобається ST Reborn?",shareTrigger:"Поділитися STR",shareHead:"…а якщо він тобі справді до вподоби?",shareMsg:"ST Reborn повертає до життя мої Bose SoundTouch без хмари Bose."}
};
// Pick the best matching locale from the phone and build T (chosen strings with
// English fall-through per key). Runs immediately so the dynamic helpers below
// can use T as soon as they execute.
var T = (function(){
  var langs = (navigator.languages && navigator.languages.length) ? navigator.languages : [navigator.language || 'en'];
  var code = 'en';
  for (var i = 0; i < langs.length; i++) {
    var pri = String(langs[i] || '').toLowerCase().split('-')[0];
    if (I18N[pri]) { code = pri; break; }
  }
  try { document.documentElement.lang = code; } catch (e) {}
  var base = I18N.en, sel = I18N[code] || base, out = {};
  for (var k in base) out[k] = (sel[k] != null ? sel[k] : base[k]);
  return out;
})();
// applyStaticI18n fills the fixed labels/buttons from T. The English text stays
// in the HTML as the no-JS fallback; this overwrites it once on load.
function applyStaticI18n() {
  var set = function(id, v){ var el = document.getElementById(id); if (el) el.textContent = v; };
  set('lblNow', T.now); set('lblVol', T.vol); set('lblInput', T.input);
  set('lblPlayback', T.playback);
  // Pause/Stop pair a media glyph with a localized text label, like Prev/Next
  // (#382). Only the label span is translated; the glyph stays in its own
  // aria-hidden span. applyTransportUI swaps the pause glyph to play when paused.
  set('btnPauseLbl', T.pause); set('btnStopLbl', T.stop);
  set('lblPresets', T.presets); set('lblPeers', T.peers);
  set('lblSupport', T.support); set('lblTip', T.tip);
  set('lblBass', T.bass);
  set('lblWedge', T.wedgeTitle);
  var v = document.getElementById('vol'); if (v) v.setAttribute('aria-label', T.vol);
  var bs = document.getElementById('bass'); if (bs) bs.setAttribute('aria-label', T.bass);
  var pb = document.getElementById('powerBtn'); if (pb) { pb.setAttribute('aria-label', T.power); pb.title = T.power; }
  // Prev/Next pair a media glyph with a visible text label (like the desktop
  // app). The glyph stays put in its own aria-hidden span; only the sibling
  // label span is localized, and the aria-label + title mirror it.
  set('btnPrevLbl', T.prev); set('btnNextLbl', T.next);
  var pv = document.getElementById('btnPrev'); if (pv) { pv.setAttribute('aria-label', T.prev); pv.title = T.prev; }
  var nx = document.getElementById('btnNext'); if (nx) { nx.setAttribute('aria-label', T.next); nx.title = T.next; }
  var st = document.getElementById('status'); if (st) st.innerHTML = '<span class="now">' + escapeHtml(T.loading) + '</span>';
  set('lblSupport', T.shareDonate); set('lblShareTrigger', T.shareTrigger); set('lblShareHead', T.shareHead);
  buildShareButtons();
}

// Social share (phone remote): no QR here - the user is already on their phone,
// so a tap opens the platform's app directly with the post pre-filled. The
// message is localized; the target is the project site, never this speaker.
var SHARE_PLATFORMS = [
  {n:'WhatsApp', u:function(url,msg,ttl){ return 'https://wa.me/?text=' + encodeURIComponent(msg); }},
  {n:'Facebook', u:function(url,msg,ttl){ return 'https://www.facebook.com/sharer/sharer.php?u=' + encodeURIComponent(url); }},
  {n:'Telegram', u:function(url,msg,ttl){ return 'https://t.me/share/url?url=' + encodeURIComponent(url) + '&text=' + encodeURIComponent(ttl); }},
  {n:'Bluesky', u:function(url,msg,ttl){ return 'https://bsky.app/intent/compose?text=' + encodeURIComponent(msg); }},
  {n:'LinkedIn', u:function(url,msg,ttl){ return 'https://www.linkedin.com/sharing/share-offsite/?url=' + encodeURIComponent(url); }},
  {n:'Reddit', u:function(url,msg,ttl){ return 'https://www.reddit.com/submit?url=' + encodeURIComponent(url) + '&title=' + encodeURIComponent(ttl); }}
];
function buildShareButtons() {
  var box = document.getElementById('shareBtns');
  if (!box) return;
  var site = 'https://st-reborn.de';
  var msg = (T.shareMsg || 'ST Reborn revives my Bose SoundTouch without the Bose cloud.') + ' ' + site;
  box.innerHTML = '';
  SHARE_PLATFORMS.forEach(function(p){
    var a = document.createElement('a');
    a.className = 'btn'; a.target = '_blank'; a.rel = 'noopener';
    a.href = p.u(site, msg, 'ST Reborn');
    a.textContent = p.n;
    box.appendChild(a);
  });
}
function toggleShare(btn) {
  var row = document.getElementById('shareRow');
  var chev = document.getElementById('shareChev');
  if (!row) return;
  var open = row.style.display === 'none';
  row.style.display = open ? 'block' : 'none';
  if (btn) btn.setAttribute('aria-expanded', open ? 'true' : 'false');
  if (chev) chev.style.transform = open ? 'rotate(180deg)' : '';
}

async function api(path, method, body) {
  const r = await fetch(path, { method: method || 'GET', headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
  if (!r.ok) { console.error(path, r.status); return null; }
  const ct = r.headers.get('content-type') || '';
  return ct.includes('json') ? r.json() : r.text();
}
function escapeHtml(s){ return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[c]); }
// decodeEntities turns the HTML/XML entities the box emits in its now_playing
// track text (e.g. &apos; &#39; &amp;) back into their characters. The box
// serves the title entity-encoded inside the now_playing XML; without decoding,
// escapeHtml below would re-escape the leading &, so an apostrophe surfaced as a
// literal "&apos;" in the remote (#295). A detached <textarea> decodes text-only
// (no markup is executed), then setNow re-escapes the result, so this stays
// safe against injection.
function decodeEntities(s){ var el = document.createElement('textarea'); el.innerHTML = String(s); return el.value; }

var volTimer = null, volLast = -1;
function onVol(v) {
  document.getElementById('volval').textContent = v;
  if (volTimer) clearTimeout(volTimer);
  volTimer = setTimeout(function(){ if (+v !== volLast) { volLast = +v; api('/api/box/volume','PUT',{value:+v}); } }, 250);
}
// Bass mirrors the volume slider pattern (debounced PUT). The row only shows
// once /api/box/settings reports bass.available, so models without a bass
// stage never render a dead control.
var bassTimer = null, bassLast = null;
function onBass(v) {
  document.getElementById('bassval').textContent = v;
  if (bassTimer) clearTimeout(bassTimer);
  bassTimer = setTimeout(function(){ if (+v !== bassLast) { bassLast = +v; api('/api/box/bass','PUT',{value:+v}); } }, 250);
}
// setNow renders the now-playing card and clears its loading state.
function setNow(name, state) {
  document.getElementById('status').innerHTML = '<span class="now">' + escapeHtml(name) + '</span>' + (state ? '<div class="st">' + escapeHtml(state) + '</div>' : '');
  document.getElementById('statusCard').classList.remove('loading');
}
// press gives a momentary tap highlight on a control button.
function press(btn) { if (!btn) return; btn.classList.add('active'); setTimeout(function(){ btn.classList.remove('active'); }, 600); }
// pp = press + POST + refresh, for the Pause/Stop controls.
async function pp(btn, path) { press(btn); await api(path, 'POST'); setTimeout(refreshStatus, 1200); }
// skip = press + POST + a couple of refreshes, for the Previous/Next controls.
// The POST is fired without awaiting it: go-librespot performs the skip but can
// hold its /player/next response for a few seconds while the next track loads,
// so awaiting would make the button feel sluggish. The scheduled refreshes pick
// up the new track. A no-op on a non-skippable source (radio, aux, Bluetooth)
// just flashes the button, like a hardware remote key.
function skip(btn, path) { press(btn); api(path, 'POST'); setTimeout(refreshStatus, 1500); setTimeout(refreshStatus, 3500); }
// The Pause button doubles as Play/Pause: when the box is paused it offers
// Play (resume from the paused position via /api/resume, like the Bose remote),
// otherwise Pause. Without a resume affordance a stream paused from the remote
// could only be restarted from the app or the physical remote (#294, mirrors
// the desktop app's #202 toggle). paused tracks the live transport state so the
// tap sends the right command even between status polls.
var paused = false;
function applyTransportUI(state) {
  paused = (state === 'PAUSE_STATE');
  var b = document.getElementById('btnPause');
  if (!b) return;
  // Swap both the glyph and the label so a paused stream offers Play (#382): the
  // label lives in its own span (kept next to the icon), the aria-label mirrors it.
  var lbl = document.getElementById('btnPauseLbl');
  if (lbl) lbl.textContent = paused ? T.play : T.pause;
  var ic = document.getElementById('btnPauseIcon');
  if (ic) ic.innerHTML = paused ? '&#9205;' : '&#9208;';
  b.setAttribute('aria-label', paused ? T.play : T.pause);
  b.title = paused ? T.play : T.pause;
}
async function togglePlayPause(btn) { await pp(btn, paused ? '/api/resume' : '/api/pause'); }
// Power on/off. The box has no "off" for a stream (Stop only pauses the
// transport, the speaker stays on), so this is a real standby toggle: off -> Bose
// standby, on -> wake + resume the last station. boxOn tracks the live state,
// refreshed from /api/status, so the button reflects the speaker even when it is
// switched at the box itself.
var boxOn = true;
function applyPowerUI() {
  var b = document.getElementById('powerBtn');
  if (b) { b.classList.toggle('on', boxOn); b.setAttribute('aria-pressed', String(boxOn)); }
}
async function togglePower() {
  var target = !boxOn;
  boxOn = target; applyPowerUI(); press(document.getElementById('powerBtn'));
  await api('/api/box/power', 'POST', { on: target });
  setTimeout(refreshStatus, 1500); setTimeout(refreshStatus, 4000);
}
// setSource selects an input and keeps that button highlighted until another is chosen.
async function setSource(s, btn) {
  document.querySelectorAll('#inputs .btn').forEach(function(e){ e.classList.remove('active'); });
  if (btn) btn.classList.add('active');
  await api('/api/box/source','PUT',{source:s});
  setTimeout(refreshStatus, 1200);
}

async function loadSettings() {
  const s = await api('/api/box/settings');
  if (!s) return;
  if (s.info && s.info.name) { var d = document.getElementById('dev'); d.textContent = s.info.name; d.title = s.info.name; }
  if (s.volume && typeof s.volume.actual === 'number') {
    volLast = s.volume.actual;
    var el = document.getElementById('vol'); el.value = s.volume.actual;
    document.getElementById('volval').textContent = s.volume.actual;
  }
  // Wedged-control hint: the agent latches boxHealth=wedged when the box
  // accepts transport pushes but never pulls the stream (only a power-cycle
  // clears that state). Tell the user standing at the speaker what to do.
  try {
    const av = await api('/api/agent/version');
    const card = document.getElementById('wedgeCard');
    if (card) {
      const wedged = av && av.boxHealth === 'wedged';
      card.style.display = wedged ? '' : 'none';
      if (wedged) document.getElementById('wedgeText').textContent = T.wedgeText;
    }
  } catch (e) {}
  if (s.bass && s.bass.available && typeof s.bass.actual === 'number') {
    var b = document.getElementById('bass');
    if (typeof s.bass.min === 'number') b.min = s.bass.min;
    if (typeof s.bass.max === 'number') b.max = s.bass.max;
    bassLast = s.bass.actual;
    b.value = s.bass.actual;
    document.getElementById('bassval').textContent = s.bass.actual;
    document.getElementById('bassCard').style.display = '';
  }
}

async function loadPresets() {
  const list = await api('/api/presets') || [];
  const grid = document.getElementById('presets');
  grid.innerHTML = '';
  for (let i = 1; i <= 6; i++) {
    const p = list.find(x => x.slot === i);
    const div = document.createElement('div');
    div.className = 'preset' + (p ? '' : ' empty');
    if (p) {
      const nm = p.name || (T.presetWord + ' ' + i);
      div.setAttribute('role','button'); div.tabIndex = 0;
      div.onclick = () => playSlot(i, div, nm);
      div.onkeydown = (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); playSlot(i, div, nm); } };
      div.innerHTML = '<div class="num">#' + i + '</div><div class="name">' + escapeHtml(nm) + '</div>';
    }
    else { div.innerHTML = '<div class="num">#' + i + '</div><div class="name">' + escapeHtml(T.empty) + '</div>'; }
    grid.appendChild(div);
  }
}
// playSlot gives instant client-side feedback (highlight the tapped tile + a
// "Starting..." status) so the user sees the press land, then confirms via the
// existing 5s status poll. No extra box request beyond the play + one refresh,
// so it adds no polling load on the speaker.
async function playSlot(n, tile, name) {
  document.querySelectorAll('.preset.active').forEach(function(e){ e.classList.remove('active'); });
  if (tile) tile.classList.add('active');
  setNow((name ? T.starting + ' ' + name : T.starting), T.pleaseWait);
  const r = await api('/api/play/' + n, 'POST');
  if (r) { setTimeout(refreshStatus, 1200); setTimeout(refreshStatus, 3000); }
  else { setNow(T.cantStart, T.tapAgain); if (tile) tile.classList.remove('active'); }
}

async function refreshStatus() {
  const r = await fetch('/api/status'); const t = await r.text();
  const m = t.match(/<itemName>([^<]+)<\/itemName>/) || t.match(/<track>([^<]+)<\/track>/);
  const src = (t.match(/source="([^"]+)"/) || [])[1] || '';
  const state = (t.match(/<playStatus>([^<]+)<\/playStatus>/) || [])[1] || '';
  const name = m ? decodeEntities(m[1]) : '';
  // Track power state for the header toggle: the box is "on" unless it is in
  // standby (a stopped-but-awake box still counts as on, so the button can switch
  // it off, which was the whole point of the request).
  boxOn = (state === 'PLAY_STATE' || state === 'PAUSE_STATE' || state === 'BUFFERING_STATE') || (!!src && src.toUpperCase() !== 'STANDBY');
  applyPowerUI();
  applyTransportUI(state);
  const human = { PLAY_STATE:T.playing, PAUSE_STATE:T.paused, STOP_STATE:T.stopped, BUFFERING_STATE:T.buffering, INVALID_SOURCE:T.stopped };
  // A stopped/idle box reports source INVALID_SOURCE or STANDBY and carries no
  // track name. Never show that raw firmware string as the "now playing" title
  // (#384): fall through to the friendly idle text instead. A real named source
  // (radio/library/AUX) is still shown as-is.
  const upSrc = src.toUpperCase();
  const idleSrc = upSrc === 'INVALID_SOURCE' || upSrc === 'STANDBY' || upSrc === '';
  const bigName = name || (idleSrc ? '' : src) || T.idle;
  setNow(bigName, human[state] || (state ? state.replace('_STATE','').toLowerCase() : T.stopped));
}

async function loadPeers() {
  // Forward-compatible: the /api/peers endpoint is added with the peer-browse
  // step; until then this 404s and the section stays hidden.
  const list = await api('/api/peers');
  if (!list || !list.length) return;
  const box = document.getElementById('peers'); box.innerHTML = '';
  list.forEach(function(p){
    const a = document.createElement('a'); a.className = 'btn peer'; a.href = p.url; a.rel = 'noopener';
    a.innerHTML = '<span class="dot"></span>' + escapeHtml(p.name || p.url);
    box.appendChild(a);
  });
  document.getElementById('peersCard').style.display = 'block';
}

async function loadVersion() {
  const v = await api('/api/agent/version');
  if (v && v.version) document.getElementById('ver').textContent = 'ST Reborn ' + v.version;
}

// Wire the "Aa" display-options menu. The preferences are applied instantly by
// the helpers defined in the <head> (toggling classes on <html>), so there is
// no reload and no request to the speaker.
(function wireA11y() {
  var trigger = document.getElementById('a11yTrigger');
  var menu = document.getElementById('a11yMenu');
  if (!trigger || !menu) return;
  // Localize the menu labels to the phone's language, reusing the same strings
  // as the desktop app. The rest of the page is localized by the I18N block at
  // the top of this script; this dictionary covers only the "Aa" menu. Done
  // client-side from navigator.language, so it costs the speaker nothing.
  var A11Y_I18N = {
    en:{t:"Display & accessibility",sz:"Text size",n:"Normal",l:"Large",x:"Extra large",th:"Theme",d:"Dark",li:"Light",c:"High contrast"},
    de:{t:"Anzeige und Barrierefreiheit",sz:"Textgröße",n:"Normal",l:"Groß",x:"Sehr groß",th:"Darstellung",d:"Dunkel",li:"Hell",c:"Hoher Kontrast"},
    nl:{t:"Weergave en toegankelijkheid",sz:"Tekstgrootte",n:"Normaal",l:"Groot",x:"Extra groot",th:"Thema",d:"Donker",li:"Licht",c:"Hoog contrast"},
    fr:{t:"Affichage et accessibilité",sz:"Taille du texte",n:"Normale",l:"Grande",x:"Très grande",th:"Thème",d:"Sombre",li:"Clair",c:"Contraste élevé"},
    es:{t:"Pantalla y accesibilidad",sz:"Tamaño del texto",n:"Normal",l:"Grande",x:"Muy grande",th:"Tema",d:"Oscuro",li:"Claro",c:"Alto contraste"},
    pl:{t:"Wyświetlanie i dostępność",sz:"Rozmiar tekstu",n:"Normalny",l:"Duży",x:"Bardzo duży",th:"Motyw",d:"Ciemny",li:"Jasny",c:"Wysoki kontrast"},
    tr:{t:"Görüntü ve erişilebilirlik",sz:"Yazı boyutu",n:"Normal",l:"Büyük",x:"Çok büyük",th:"Tema",d:"Koyu",li:"Açık",c:"Yüksek kontrast"},
    ar:{t:"العرض وإمكانية الوصول",sz:"حجم النص",n:"عادي",l:"كبير",x:"كبير جدًا",th:"السمة",d:"داكن",li:"فاتح",c:"تباين عالٍ"},
    ja:{t:"表示とアクセシビリティ",sz:"文字サイズ",n:"標準",l:"大",x:"特大",th:"テーマ",d:"ダーク",li:"ライト",c:"ハイコントラスト"},
    lt:{t:"Rodymas ir prieinamumas",sz:"Teksto dydis",n:"Normalus",l:"Didelis",x:"Labai didelis",th:"Tema",d:"Tamsi",li:"Šviesi",c:"Didelis kontrastas"},
    lv:{t:"Attēlojums un pieejamība",sz:"Teksta izmērs",n:"Normāls",l:"Liels",x:"Ļoti liels",th:"Motīvs",d:"Tumšs",li:"Gaišs",c:"Augsts kontrasts"},
    uk:{t:"Відображення та доступність",sz:"Розмір тексту",n:"Звичайний",l:"Великий",x:"Дуже великий",th:"Тема",d:"Темна",li:"Світла",c:"Високий контраст"}
  };
  var langs = (navigator.languages && navigator.languages.length) ? navigator.languages : [navigator.language || 'en'];
  var code = 'en';
  for (var i = 0; i < langs.length; i++) {
    var pri = String(langs[i] || '').toLowerCase().split('-')[0];
    if (A11Y_I18N[pri]) { code = pri; break; }
  }
  var L = A11Y_I18N[code];
  menu.setAttribute('lang', code);
  if (code === 'ar') menu.setAttribute('dir', 'rtl');
  trigger.setAttribute('aria-label', L.t); trigger.title = L.t; menu.setAttribute('aria-label', L.t);
  document.getElementById('a11ySizeLabel').textContent = L.sz;
  document.getElementById('a11yThemeLabel').textContent = L.th;
  var setTxt = function(sel, v) { var el = menu.querySelector(sel); if (el) el.textContent = v; };
  setTxt('button[data-scale="1"]', L.n); setTxt('button[data-scale="2"]', L.l); setTxt('button[data-scale="3"]', L.x);
  setTxt('button[data-theme="dark"]', L.d); setTxt('button[data-theme="light"]', L.li); setTxt('button[data-theme="contrast"]', L.c);
  function close() { menu.hidden = true; trigger.setAttribute('aria-expanded', 'false'); }
  function open() { menu.hidden = false; trigger.setAttribute('aria-expanded', 'true'); }
  trigger.onclick = function(e) { e.stopPropagation(); if (menu.hidden) open(); else close(); };
  function sync(attr, val) {
    menu.querySelectorAll('button[' + attr + ']').forEach(function(b) {
      b.setAttribute('aria-pressed', String(b.getAttribute(attr) === String(val)));
    });
  }
  menu.querySelectorAll('button[data-scale]').forEach(function(b) {
    b.onclick = function() { var n = Number(b.dataset.scale); a11ySetScale(n); sync('data-scale', n); };
  });
  menu.querySelectorAll('button[data-theme]').forEach(function(b) {
    b.onclick = function() { a11ySetTheme(b.dataset.theme); sync('data-theme', b.dataset.theme); };
  });
  sync('data-scale', a11yGetScale());
  sync('data-theme', a11yGetTheme());
  document.addEventListener('click', function(e) { if (!e.target.closest('.a11y')) close(); });
  document.addEventListener('keydown', function(e) { if (e.key === 'Escape') close(); });
})();

// refreshAll re-fetches every panel at once (used by pull-to-refresh and on
// regaining foreground), with a short minimum so the spinner does not flash.
function refreshAll(){
  var work = Promise.all([loadSettings(), loadPresets(), refreshStatus(), loadPeers(), loadVersion()]).catch(function(){});
  var minSpin = new Promise(function(r){ setTimeout(r, 500); });
  return Promise.all([work, minSpin]);
}
// A saved-to-home-screen PWA keeps running in the background; its data would be
// stale on reopen, so re-fetch whenever the app returns to the foreground.
document.addEventListener('visibilitychange', function(){
  if (document.visibilityState === 'visible') refreshAll();
});
// Pull-to-refresh: in standalone PWA mode there is no browser reload button, so a
// pull down from the very top re-fetches everything. Touch-only; ignores pulls
// that start on a control or when the page is already scrolled.
(function ptrInit(){
  var ptr = document.getElementById('ptr');
  if (!ptr || !('ontouchstart' in window)) return;
  var spin = ptr.querySelector('.ptr-spin');
  var startY = 0, pulling = false, dist = 0, busy = false;
  var THRESHOLD = 70;
  function clearSpin(){ spin.style.opacity = ''; spin.style.transform = ''; }
  function retract(){ ptr.classList.add('snap'); spin.style.opacity = '0'; spin.style.transform = 'translateY(-40px)'; setTimeout(function(){ ptr.classList.remove('snap'); clearSpin(); busy = false; }, 300); }
  document.addEventListener('touchstart', function(e){
    if (busy || window.scrollY > 0 || (e.target.closest && e.target.closest('input,button,a,.preset,.a11y-menu'))) { startY = -1; return; }
    startY = e.touches[0].clientY; pulling = false; dist = 0; ptr.classList.remove('snap');
  }, {passive:true});
  document.addEventListener('touchmove', function(e){
    if (busy || startY < 0) return;
    var dy = e.touches[0].clientY - startY;
    if (dy <= 0 || window.scrollY > 0) return;
    pulling = true; dist = dy * 0.5; e.preventDefault();
    spin.style.opacity = Math.min(1, dist / THRESHOLD);
    spin.style.transform = 'translateY(' + (Math.min(dist, 56) - 40) + 'px) rotate(' + (dist * 3) + 'deg)';
    ptr.classList.toggle('armed', dist >= THRESHOLD);
  }, {passive:false});
  document.addEventListener('touchend', function(){
    if (busy || !pulling) { startY = 0; pulling = false; return; }
    var fire = dist >= THRESHOLD;
    pulling = false; startY = 0; ptr.classList.remove('armed');
    if (!fire) { retract(); return; }
    busy = true; clearSpin(); ptr.classList.add('spinning');
    refreshAll().then(function(){ ptr.classList.remove('spinning'); retract(); });
  }, {passive:true});
})();

applyStaticI18n(); loadSettings(); loadPresets(); refreshStatus(); loadPeers(); loadVersion();
setInterval(refreshStatus, 5000);
</script>
</body>
</html>
`
