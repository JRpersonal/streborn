package webui

import (
	"strings"
	"testing"
)

// #1663 and #1409 on the phone: the Pandora list draws each station's cover
// the way a key's cover is drawn, and has a star that keeps the station in
// Favourites; a Pandora favourite is listed and plays through the Pandora call.
func TestPhoneRemotePandoraCoverAndStar(t *testing.T) {
	list := pageSection(t, "function renderPandora(", "async function loadPandora(")
	for _, want := range []string{
		"presetArtImg(presetArtCands({ art: st.art || '' }), st.name || st.itemName)",
		"btns.appendChild(pandoraStar(st, row))",
		"var key = 'pandora:' + st.location;",
		"source: 'pandora'",
		"location: st.location",
		"itemType: st.type || 'stationurl'",
		"favicon: st.art || ''",
	} {
		if !strings.Contains(list, want) {
			t.Errorf("the Pandora list is missing %q", want)
		}
	}
	load := pageSection(t, "async function loadFavorites(", "function favKey(")
	if !strings.Contains(load, "isStreamURL(st.url_resolved || st.url) || isPandoraFav(st)") {
		t.Error("the favourites list drops Pandora stations")
	}
	play := pageSection(t, "async function playFavorite(", "press(btn);")
	if !strings.Contains(play, "startPandora({ location: st.location, name: st.name || '', type: st.itemType || '' }, btn)") {
		t.Error("a Pandora favourite does not play through the Pandora call")
	}
	if !strings.Contains(indexHTML, ".station .art {") {
		t.Error("the Pandora cover has no size on a station row")
	}
}
