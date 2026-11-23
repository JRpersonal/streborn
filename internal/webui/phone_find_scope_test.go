package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Discussion #1710: on the phone's Find page the station hits came at once
// and the music library's 15 to 20 s later, with nothing on screen in
// between, so people left before their music turned up. They also asked to
// search the stations or the library alone. The library section now says it
// is still searching while a music server is known, and the "Search in"
// picker (album, artist, track since #1325) moved under the search field and
// also offers stations only and the music library only.

func TestPhoneFindScopePickerSitsUnderTheSearchField(t *testing.T) {
	srch := strings.Index(indexHTML, `id="btnSearch"`)
	row := strings.Index(indexHTML, `id="libFieldRow"`)
	results := strings.Index(indexHTML, `<div id="results"></div>`)
	if srch < 0 || row < 0 || results < 0 || !(srch < row && row < results) {
		t.Fatalf("the Search in picker must sit between the search field and the station results (btnSearch %d, libFieldRow %d, results %d)", srch, row, results)
	}
	for _, v := range []string{"any", "stations", "lib", "album", "artist", "title"} {
		if !strings.Contains(indexHTML, `<option value="`+v+`">`) {
			t.Errorf("the Search in picker has no %q option", v)
		}
	}
	// The old inline handler re-ran the library alone; the picker now re-runs
	// the whole search, so a stations-only choice can take the library away.
	if strings.Contains(indexHTML, "onchange=\"if (libLastQ)") {
		t.Error("the picker still carries the old library-only onchange handler")
	}
}

// Every new string exists in every locale bundle, and none carries a dash
// the house style rules out.
func TestPhoneFindScopeStringsInEveryLocale(t *testing.T) {
	bundles := strings.Count(indexHTML, "now:\"")
	if bundles == 0 {
		t.Fatal("could not find any locale bundle in indexHTML")
	}
	for _, key := range []string{
		"libSearching", "findAll", "findStations", "findLib", "findAlbum", "findArtist", "findTrack",
	} {
		ms := regexp.MustCompile(`[,{]`+key+`:"([^"]*)"`).FindAllStringSubmatch(indexHTML, -1)
		if len(ms) != bundles {
			t.Errorf("%s: %d locale bundles but %d keys", key, bundles, len(ms))
		}
		for _, m := range ms {
			if m[1] == "" || strings.ContainsAny(m[1], "–—") {
				t.Errorf("%s: bad value %q", key, m[1])
			}
		}
	}
	for _, want := range []string{
		`libSearching:"Searching your music library…"`,
		`libSearching:"Deine Musikbibliothek wird durchsucht…"`,
		`findStations:"Nur Sender"`,
		`findLib:"Nur Musikbibliothek"`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("the EN or DE bundle is missing %s", want)
		}
	}
}

// findSource cuts the Find search out of the page: the radio search, the
// scope picker and the library search, up to the library play handler.
func findSource(t *testing.T) string {
	t.Helper()
	a := strings.Index(indexHTML, "var RB_MIRRORS = ")
	b := strings.Index(indexHTML, "async function playLibTrack(")
	if a < 0 || b < a {
		t.Fatal("cannot find the Find search in index.html")
	}
	return indexHTML[a:b]
}

// findPrelude is a fake page for that code: elements by id, a radio fetch
// that answers at once with no stations, and a library api whose answers the
// test hands out one by one.
const findPrelude = `
function el(id) {
  var kids = [], html = '';
  var e = { id: id, hidden: false, textContent: '', value: '', className: '', ls: {},
    children: kids,
    appendChild: function(c) { kids.push(c); return c; },
    replaceWith: function() {},
    setAttribute: function() {},
    addEventListener: function(n, f) { (this.ls[n] = this.ls[n] || []).push(f); },
    fire: function(n) { (this.ls[n] || []).forEach(function(f) { f({}); }); } };
  Object.defineProperty(e, 'innerHTML', { get: function() { return html; },
    set: function(v) { html = v; kids.length = 0; } });
  return e;
}
var els = {};
var document = {
  getElementById: function(id) { return els[id] || (els[id] = el(id)); },
  createElement: function(tag) { return el(''); },
  querySelector: function() { return null; }
};
['lblLibSec', 'libMsg', 'searchMsg'].forEach(function(id) { document.getElementById(id).hidden = true; });
document.getElementById('libFieldRow').hidden = true;
document.getElementById('libField').value = 'any';
var store = { strFindScope: 'stations' };
var localStorage = {
  getItem: function(k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
  setItem: function(k, v) { store[k] = String(v); }
};
var T = { libSec: 'From your music library', libSearching: 'Searching your music library…',
  libNone: 'Nothing found', libNoServers: 'No server', libOffline: 'off {name}', libPartial: 'partial',
  searching: 'Searching…', noRes: 'No stations', netFail: 'net', play: 'Play', saveKey: 'Save', favAdd: 'Add' };
function fmt(s, v) { return String(s).replace(/\{(\w+)\}/g, function(_, k) { return v[k]; }); }
function deezerLinkOf() { return null; }
function browseMarkRow() {}
var favStations = [];
function favKey(st) { return st.stationuuid || ''; }
var fetches = 0;
function fetch() { fetches++; return Promise.resolve({ ok: true, json: function() { return Promise.resolve([]); } }); }
var apiCalls = [];
function api(url) { return new Promise(function(res) { apiCalls.push({ url: url, answer: res }); }); }
function tick() { return new Promise(function(r) { setTimeout(r, 0); }); }
var fails = [];
function check(name, got, want) { if (String(got) !== String(want)) fails.push(name + ': got ' + got + ', want ' + want); }
`

const findHarness = `
(async function() {
  var head = document.getElementById('lblLibSec'), msg = document.getElementById('libMsg');
  var box = document.getElementById('libResults'), row = document.getElementById('libFieldRow');
  var sel = document.getElementById('libField'), q = document.getElementById('q');
  function reset() { fetches = 0; apiCalls = []; }

  // 0. The kept choice comes back on load, but counts only once the picker shows.
  check('kept choice restored', sel.value, 'stations');
  check('hidden picker searches both', findScope(), 'any');

  // 1. No music server known: both searches run, the library says nothing
  // until it answers, then the no-server note as before.
  reset(); q.value = 'jazz';
  await searchStations();
  check('no server: radio searched', fetches, 1);
  check('no server: library searched', apiCalls.length, 1);
  check('no server: heading hidden while searching', head.hidden, true);
  check('no server: no searching line', msg.hidden, true);
  apiCalls[0].answer({ registered: 0, results: [] });
  await tick();
  check('no server: note after the answer', msg.textContent, T.libNoServers);
  check('no server: picker stays hidden', row.hidden, true);

  // 2. A server is known: the picker shows, the section is there at once
  // with the searching line, and the hits replace it.
  setLibServersKnown(2);
  check('server known: picker shows', row.hidden, false);
  sel.value = 'any'; reset();
  var run = searchStations();
  check('searching: heading shown', head.hidden, false);
  check('searching: heading text', head.textContent, T.libSec);
  check('searching: line shown', msg.hidden, false);
  check('searching: line text', msg.textContent, T.libSearching);
  await run;
  check('searching: radio searched too', fetches, 1);
  check('searching: whole library', apiCalls[0].url.indexOf('&field=any') > 0, true);
  check('searching: line survives the radio answer', msg.textContent, T.libSearching);
  apiCalls[0].answer({ registered: 2, results: [{ title: 'Song', url: 'http://192.0.2.1/a.flac' }] });
  await tick();
  check('hits: line gone', msg.hidden, true);
  check('hits: one row', box.children.length, 1);
  check('hits: heading kept', head.hidden, false);

  // 3. A miss replaces the searching line with the existing nothing-found note.
  reset(); await searchStations();
  check('miss: searching line', msg.textContent, T.libSearching);
  apiCalls[0].answer({ registered: 2, results: [] });
  await tick();
  check('miss: nothing-found note', msg.textContent, T.libNone);
  check('miss: note shown', msg.hidden, false);

  // 4. Stations only: no library search, the section goes away.
  sel.value = 'stations'; reset();
  await searchStations();
  check('stations only: radio searched', fetches, 1);
  check('stations only: library not searched', apiCalls.length, 0);
  check('stations only: heading hidden', head.hidden, true);
  check('stations only: line hidden', msg.hidden, true);

  // 5. Music library only, and by album: no radio search, the field is passed on.
  sel.value = 'lib'; reset();
  await searchStations();
  check('library only: radio not searched', fetches, 0);
  check('library only: whole library', apiCalls.length === 1 && apiCalls[0].url.indexOf('&field=any') > 0, true);
  check('library only: no station line', document.getElementById('searchMsg').textContent, '');
  sel.value = 'album'; reset();
  await searchStations();
  check('album: radio not searched', fetches, 0);
  check('album: field passed', apiCalls.length === 1 && apiCalls[0].url.indexOf('&field=album') > 0, true);

  // 6. Changing the picker keeps the choice and runs the typed search again.
  sel.value = 'stations'; reset();
  sel.fire('change');
  await tick();
  check('change: kept', store.strFindScope, 'stations');
  check('change: search ran again', fetches, 1);

  // 7. A library answer still on its way when the user picks stations only
  // is dropped, not painted.
  sel.value = 'any'; reset();
  await searchStations();
  var late = apiCalls[0];
  sel.value = 'stations';
  await searchStations();
  late.answer({ registered: 2, results: [{ title: 'Late', url: 'http://192.0.2.1/b.flac' }] });
  await tick();
  check('late answer: no rows', box.children.length, 0);
  check('late answer: heading hidden', head.hidden, true);

  // 8. An unreachable agent takes the searching line away again.
  sel.value = 'any'; reset();
  await searchStations();
  apiCalls[0].answer(null);
  await tick();
  check('no answer: line gone', msg.hidden, true);
  check('no answer: heading gone', head.hidden, true);

  if (fails.length) { console.log(fails.join('\n')); process.exit(1); }
  console.log('ok');
})().catch(function(e) { console.log(e && e.stack || e); process.exit(1); });
`

func TestPhoneFindScopeBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	f := filepath.Join(t.TempDir(), "find.js")
	if err := os.WriteFile(f, []byte(findPrelude+findSource(t)+findHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("Find search behaviour: %v\n%s", err, out)
	}
}
