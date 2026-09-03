package sticksetup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The locale -> Bose sysLanguage mapping is the single source of truth for
// everywhere a language is sent to the box (lang.conf + setup-AP push), so
// pin the known mappings, the Chinese region-subtag split, and the English
// fallback for anything unknown. 0 (unset) and 14 (undefined) must never
// come out. Full enum in project_bose_language_enum.
func TestSysLanguageForLocale(t *testing.T) {
	tests := []struct {
		locale string
		want   int
	}{
		{"da", 1},
		{"de", 2},
		{"de-CH", 2},
		{"de_AT", 2},
		{"  DE-at  ", 2}, // case and whitespace must not matter
		{"en", 3},
		{"en-US", 3},
		{"es", 4},
		{"fr", 5},
		{"it", 6},
		{"nl", 7},
		{"sv", 8},
		{"ja", 9},
		{"ko", 12},
		{"th", 13},
		{"cs", 15},
		{"fi", 16},
		{"el", 17},
		{"nb", 18},
		{"nn", 18},
		{"no", 18},
		{"pl", 19},
		{"pt", 20},
		{"pt-BR", 20},
		{"ro", 21},
		{"ru", 22},
		{"uk", 22}, // no Ukrainian in the box enum, Russian is the fallback
		{"sl", 23},
		{"tr", 24},
		{"hu", 25},
		// Chinese: region subtag decides simplified vs traditional.
		{"zh", 10},
		{"zh-CN", 10},
		{"zh-Hans", 10},
		{"zh-TW", 11},
		{"zh-HK", 11},
		{"zh-MO", 11},
		{"zh-Hant", 11},
		{"ZH-tw", 11},
		// Unknown / empty floors to English, never 0 or 14.
		{"", 3},
		{"xx", 3},
		{"tlh-Latn", 3},
	}
	for _, tt := range tests {
		if got := SysLanguageForLocale(tt.locale); got != tt.want {
			t.Errorf("SysLanguageForLocale(%q) = %d, want %d", tt.locale, got, tt.want)
		}
	}
}

// localePrimary feeds the "deliberate non-English UI locale" decision in
// SuggestBoxLanguage, so both separators, casing, whitespace, and the empty
// input must behave.
func TestLocalePrimary(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{"de-CH", "de"},
		{"en_US", "en"},
		{"zh-Hant-TW", "zh"},
		{"EN", "en"},
		{"  fr  ", "fr"},
		{"de", "de"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		if got := localePrimary(tt.locale); got != tt.want {
			t.Errorf("localePrimary(%q) = %q, want %q", tt.locale, got, tt.want)
		}
	}
}

// Pre-selection rule: a deliberate non-English UI locale wins, otherwise the
// chosen country decides, and English (3) is the floor when neither signal
// helps. An "en" locale is NOT deliberate (it is the bundle fallback for
// users whose language ships no app translation).
func TestSuggestBoxLanguage(t *testing.T) {
	tests := []struct {
		name    string
		locale  string
		country string
		want    int
	}{
		{"non-English locale beats country", "de", "US", 2},
		{"English locale defers to country", "en", "DE", 2},
		{"regional English defers to country", "en-US", "FR", 5},
		{"empty locale defers to country", "", "SE", 8},
		{"locale wins with matching country", "fr", "FR", 5},
		{"Ukrainian locale falls back to Russian", "uk", "UA", 22},
		{"traditional Chinese via locale", "zh-TW", "US", 11},
		{"English locale, unknown country floors", "en", "XX", 3},
		{"empty locale, unknown country floors", "", "", 3},
	}
	for _, tt := range tests {
		if got := SuggestBoxLanguage(tt.locale, tt.country); got != tt.want {
			t.Errorf("%s: SuggestBoxLanguage(%q, %q) = %d, want %d",
				tt.name, tt.locale, tt.country, got, tt.want)
		}
	}
}

// An unknown but non-English locale is still treated as deliberate: the
// locale branch runs and floors to English (3) even when the country would
// have a language. Documented behaviour, pinned so a change is conscious.
func TestSuggestBoxLanguageUnknownLocaleWins(t *testing.T) {
	if got := SuggestBoxLanguage("xx", "JP"); got != 3 {
		t.Errorf("SuggestBoxLanguage(\"xx\", \"JP\") = %d, want 3 (unknown locale floors to English)", got)
	}
}

// WriteLangConfig writes lang.conf for run.sh: locale defaults to "en",
// country is uppercased, and an invalid sysLanguage (<=0, 14, >25) is reset
// to SuggestBoxLanguage so the shell side can trust the integer verbatim.
func TestWriteLangConfig(t *testing.T) {
	tests := []struct {
		name        string
		locale      string
		country     string
		sysLanguage int
		want        LangConfig
	}{
		{
			name: "valid value passes through", locale: "de", country: "de", sysLanguage: 2,
			want: LangConfig{Locale: "de", Country: "DE", SysLanguage: 2},
		},
		{
			name: "zero is reset via suggestion", locale: "en", country: "FR", sysLanguage: 0,
			want: LangConfig{Locale: "en", Country: "FR", SysLanguage: 5},
		},
		{
			name: "undefined 14 is reset via suggestion", locale: "sv", country: "SE", sysLanguage: 14,
			want: LangConfig{Locale: "sv", Country: "SE", SysLanguage: 8},
		},
		{
			name: "out of range is reset via suggestion", locale: "ja", country: "JP", sysLanguage: 26,
			want: LangConfig{Locale: "ja", Country: "JP", SysLanguage: 9},
		},
		{
			name: "negative is reset via suggestion", locale: "pl", country: "PL", sysLanguage: -1,
			want: LangConfig{Locale: "pl", Country: "PL", SysLanguage: 19},
		},
		{
			name: "empty locale defaults to en, country decides", locale: "", country: "se", sysLanguage: 0,
			want: LangConfig{Locale: "en", Country: "SE", SysLanguage: 8},
		},
		{
			name: "everything empty floors to English", locale: "", country: "", sysLanguage: 0,
			want: LangConfig{Locale: "en", Country: "", SysLanguage: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := WriteLangConfig(dir, tt.locale, tt.country, tt.sysLanguage); err != nil {
				t.Fatalf("WriteLangConfig: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "lang.conf"))
			if err != nil {
				t.Fatalf("read lang.conf: %v", err)
			}
			var got LangConfig
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("lang.conf is not valid JSON: %v\n%s", err, data)
			}
			if got != tt.want {
				t.Errorf("lang.conf = %+v, want %+v (raw: %s)", got, tt.want, data)
			}
			// The atomic-write tmp file must not be left behind.
			if _, statErr := os.Stat(filepath.Join(dir, "lang.conf.new")); !os.IsNotExist(statErr) {
				t.Error("temporary lang.conf.new left behind")
			}
		})
	}
}
