package webui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Discussion #2140: a user asked for "STAR FM Berlin" and "BH Eins" to be
// added to the station search. Both were in the directory all along, as
// "StarFM Berlin" and "BHeins", which the plain name match never reached.
const spellingHarness = `
(async function() {
  var asked = [];
  var dir = {
    'star fm': [{ stationuuid: 's1', name: 'Star FM 101.9', url: 'http://radio.example/gd' },
                { stationuuid: 's2', name: 'Star FM Eesti', url: 'http://radio.example/ee' }],
    'starfm': [{ stationuuid: 'b1', name: 'StarFM Berlin', url: 'http://radio.example/berlin' },
               { stationuuid: 's2', name: 'Star FM Eesti', url: 'http://radio.example/ee' }],
    'berlin': [{ stationuuid: 'x1', name: 'Radio Berlin', url: 'http://radio.example/rb' },
               { stationuuid: 'b1', name: 'StarFM Berlin', url: 'http://radio.example/berlin' }],
    'eins': [{ stationuuid: 'e1', name: 'Radio Eins', url: 'http://radio.example/eins' },
             { stationuuid: 'h1', name: 'BHeins 95.3 FM', url: 'http://radio.example/bh' }],
    'bheins': [{ stationuuid: 'h1', name: 'BHeins 95.3 FM', url: 'http://radio.example/bh' }]
  };
  fetch = function(url) {
    var name = decodeURIComponent(/[?&]name=([^&]*)/.exec(url)[1]).toLowerCase();
    asked.push(name);
    return Promise.resolve({ ok: true, json: function() { return Promise.resolve(dir[name] || []); } });
  };
  var q = document.getElementById('q');
  document.getElementById('libField').value = 'stations';
  setLibServersKnown(1);
  function names() { return lastResults.map(function(s) { return s.name; }).join(' | '); }

  asked = []; q.value = 'Star FM';
  await searchStations();
  check('written together goes first, no station twice', names(), 'StarFM Berlin | Star FM Eesti | Star FM 101.9');

  asked = []; q.value = 'STAR FM Berlin';
  await searchStations();
  check('full name spelled apart', names(), 'StarFM Berlin');

  asked = []; q.value = 'BH Eins';
  await searchStations();
  check('two words for a one-word name', names(), 'BHeins 95.3 FM');

  asked = []; q.value = 'starfm';
  await searchStations();
  check('one word asks once', asked.length, 1);

  check('rbNorm keeps umlauts and digits', rbNorm('Österreich 1'), 'österreich1');

  fetch = function() { return Promise.reject(new Error('offline')); };
  q.value = 'star fm';
  await searchStations();
  check('no mirror answers', document.getElementById('searchMsg').textContent, T.netFail);

  if (fails.length) { console.log(fails.join('\n')); process.exit(1); }
  console.log('ok');
})().catch(function(e) { console.log(e && e.stack || e); process.exit(1); });
`

func TestPhoneStationSearchFindsOtherSpellings(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	f := filepath.Join(t.TempDir(), "spelling.js")
	if err := os.WriteFile(f, []byte(findPrelude+findSource(t)+spellingHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, f).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("station search spellings: %v\n%s", err, out)
	}
}
