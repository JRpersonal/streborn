package presets

import (
	"encoding/xml"
	"strings"
)

// NativeSourceAccounts reads the speaker's own source list (GET :8090/sources)
// into source -> the sourceAccount values it lists for that source, READY
// entries first. Sources are folded with NormalizeNativeSource. An unreadable
// document is an empty map.
func NativeSourceAccounts(sourcesXML []byte) map[string][]string {
	var doc struct {
		Items []struct {
			Source  string `xml:"source,attr"`
			Account string `xml:"sourceAccount,attr"`
			Status  string `xml:"status,attr"`
		} `xml:"sourceItem"`
	}
	out := map[string][]string{}
	if err := xml.Unmarshal(sourcesXML, &doc); err != nil {
		return out
	}
	later := map[string][]string{}
	for _, it := range doc.Items {
		src := NormalizeNativeSource(it.Source)
		acct := strings.TrimSpace(it.Account)
		if acct == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(it.Status), "READY") {
			out[src] = append(out[src], acct)
		} else {
			later[src] = append(later[src], acct)
		}
	}
	for src, accts := range later {
		out[src] = append(out[src], accts...)
	}
	return out
}

// NativeSourceUnavailable reports whether the speaker's own source list (GET
// :8090/sources) shows the service as unusable: it lists the source, at least
// one entry says UNAVAILABLE, and none is READY. A source the speaker does not
// list, or an unreadable document, is not reported unavailable (unknown is not
// proven dead), and one READY account among stale ones is enough (#1630).
//
// For a native music service this is the firmware's own verdict on its login:
// a Pandora the speaker lists UNAVAILABLE answers every /select with 500
// 1008 GENERAL_PANDORA_ERROR (#1763).
func NativeSourceUnavailable(sourcesXML []byte, source string) bool {
	var doc struct {
		Items []struct {
			Source string `xml:"source,attr"`
			Status string `xml:"status,attr"`
		} `xml:"sourceItem"`
	}
	if err := xml.Unmarshal(sourcesXML, &doc); err != nil {
		return false
	}
	want := NormalizeNativeSource(source)
	unavailable := false
	for _, it := range doc.Items {
		if NormalizeNativeSource(it.Source) != want {
			continue
		}
		switch st := strings.TrimSpace(it.Status); {
		case strings.EqualFold(st, "READY"):
			return false
		case strings.EqualFold(st, "UNAVAILABLE"):
			unavailable = true
		}
	}
	return unavailable
}

// ResolveNativeAccount returns the sourceAccount a native item must carry on
// this speaker. The stored account wins when the speaker lists it for the
// item's source; otherwise the speaker's own account for that source is used
// (the first READY one). With nothing listed the stored value is kept.
//
// The speaker's hold-to-store record names no account, and STR up to v1.0.5
// stored the record's label there instead (#1101): a "Chris Stapleton Radio"
// account the speaker answers with 1005 UNKNOWN_SOURCE_ERROR on /select and
// that its command line cannot even carry. Resolving against the live list
// heals such a preset without the user saving it again.
func ResolveNativeAccount(item NativeItem, listed map[string][]string) string {
	accts := listed[NormalizeNativeSource(item.Source)]
	for _, a := range accts {
		if strings.EqualFold(a, item.SourceAccount) {
			return item.SourceAccount
		}
	}
	if len(accts) > 0 {
		return accts[0]
	}
	return item.SourceAccount
}
