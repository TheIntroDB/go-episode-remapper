package remap

import (
	"encoding/json"
	"testing"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// The alias check is the fix for the measured read-path failures: TVDB catalogues a
// show under its original-language title and TMDB carries the English one, so
// comparing the canonical name alone rejected correct links for Attack on Titan, My
// Hero Academia, Bluey, Archer, Horimiya and others.
func TestCorroboratesSeriesName(t *testing.T) {
	jpAliases := []tvdb.Alias{
		{Language: "jpn", Name: "進撃の巨人"},
		{Language: "eng", Name: "Attack on Titan"},
		{Language: "eng", Name: "Shingeki no Kyojin"},
	}

	cases := []struct {
		name      string
		want      string
		canonical string
		aliases   []tvdb.Alias
		expect    bool
	}{
		{
			name:      "canonical match still passes, unchanged behaviour",
			want:      "Futurama",
			canonical: "Futurama",
			expect:    true,
		},
		{
			name:      "translated title only in aliases now passes",
			want:      "Attack on Titan",
			canonical: "進撃の巨人",
			aliases:   jpAliases,
			expect:    true,
		},
		{
			name:      "case and punctuation do not defeat the alias match",
			want:      "attack on titan!",
			canonical: "進撃の巨人",
			aliases:   jpAliases,
			expect:    true,
		},
		{
			name:      "romanised alias passes",
			want:      "Shingeki no Kyojin",
			canonical: "進撃の巨人",
			aliases:   jpAliases,
			expect:    true,
		},
		{
			name:      "CJK is preserved, not normalised to nothing",
			want:      "進撃の巨人",
			canonical: "Attack on Titan",
			aliases:   jpAliases,
			expect:    true,
		},
		{
			name:      "an unrelated name is still refused -- the gate is still a gate",
			want:      "War and Remembrance",
			canonical: "American Dad!",
			aliases:   []tvdb.Alias{{Language: "eng", Name: "American Dad"}},
			expect:    false,
		},
		{
			name:      "no aliases and a different canonical name is refused",
			want:      "Bluey",
			canonical: "ブルーイ",
			expect:    false,
		},
		{
			name:      "an empty wanted name matches nothing",
			want:      "",
			canonical: "Futurama",
			aliases:   jpAliases,
			expect:    false,
		},
		{
			name:      "empty alias names cannot match an empty want",
			want:      "   ",
			canonical: "",
			aliases:   []tvdb.Alias{{Language: "eng", Name: ""}, {Language: "jpn", Name: "  "}},
			expect:    false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := corroboratesSeriesName(c.want, c.canonical, c.aliases); got != c.expect {
				t.Fatalf("corroboratesSeriesName(%q, %q, %d aliases) = %v, want %v",
					c.want, c.canonical, len(c.aliases), got, c.expect)
			}
		})
	}
}

// The extended record must actually PARSE the aliases field, or the fix is inert:
// the struct had no such field before, so every alias check would silently see nil
// and the gate would behave exactly as before.
func TestSeriesExtendedRecordParsesAliases(t *testing.T) {
	// The shape TVDB really returns for /series/{id}/extended.
	body := `{"id":267440,"name":"進撃の巨人",` +
		`"aliases":[{"language":"eng","name":"Attack on Titan"},{"language":"jpn","name":"進撃の巨人"}],` +
		`"remoteIds":[{"sourceName":"TheMovieDB.com","id":"1429"}]}`

	var rec tvdb.SeriesExtendedRecord
	if err := json.Unmarshal([]byte(body), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rec.Aliases) != 2 {
		t.Fatalf("aliases did not parse (got %d): %+v", len(rec.Aliases), rec.Aliases)
	}
	if rec.Aliases[0].Name != "Attack on Titan" {
		t.Fatalf("first alias name = %q, want %q", rec.Aliases[0].Name, "Attack on Titan")
	}
	if rec.Name != "進撃の巨人" {
		t.Fatalf("canonical name = %q", rec.Name)
	}
	// End to end on a payload TVDB really returns: this is the exact case that
	// 404'd on the tmdb_id path.
	if !corroboratesSeriesName("Attack on Titan", rec.Name, rec.Aliases) {
		t.Fatal("a real TVDB payload should now corroborate, and did not")
	}
}