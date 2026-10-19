package remap

import (
	"context"
	"testing"
	"time"

	"github.com/Pasithea0/go-tmdb-episodes-fix/pkg/tvdb"
)

// The memo is proved by MUTATING the fixture, not by counting calls: a resolved
// answer that survives a change to the underlying record can only have come from the
// memo, and one that does not can only have been re-resolved. That works with the
// existing fakes and needs no instrumentation inside them.

// memoMapper builds a mapper whose only route is TMDB external_ids -> a corroborating
// TVDB record, so the fixture's name decides whether the verification succeeds.
func memoMapper(ttl time.Duration) (*Mapper, *fakeTVDB) {
	ft := &fakeTVDB{
		seriesByID: map[int]*tvdb.SeriesBaseRecord{73871: {ID: 73871, Name: "Futurama"}},
	}
	m := &Mapper{
		tvdb: ft,
		tmdb: &fakeTMDB{
			names:  map[int]string{900: "Futurama"},
			tvdbID: map[int]int{900: 73871},
		},
		tvdbSeasonType:      "default",
		fallbackSeasonTypes: defaultFallbackSeasonTypes,
		maxTVDBPages:        200,
		maxTMDBSeasons:      300,
		verifiedSeriesTTL:   ttl,
	}
	if ttl != 0 {
		m.verifiedSeries = make(map[int]verifiedSeriesEntry)
	}
	return m, ft
}

// TestVerifiedSeriesMemoServesTheRememberedAnswer is the whole point of the memo: the
// second episode of a series must not re-verify it. The underlying record is renamed
// between the two calls, so a second upstream resolution would now REFUSE the link --
// the remembered answer is the only way the second call can still succeed.
func TestVerifiedSeriesMemoServesTheRememberedAnswer(t *testing.T) {
	m, ft := memoMapper(time.Minute)
	ctx := context.Background()

	first, prov, err := m.resolveVerifiedTVDBSeries(ctx, 900)
	if err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	if first.Name != "Futurama" || prov != "tmdb_external_ids:name" {
		t.Fatalf("first = %+v / %q", first, prov)
	}

	ft.seriesByID[73871] = &tvdb.SeriesBaseRecord{ID: 73871, Name: "Totally Different Show"}

	second, secondProv, err := m.resolveVerifiedTVDBSeries(ctx, 900)
	if err != nil {
		t.Fatalf("second resolution must be served from the memo, got error: %v", err)
	}
	if second.Name != "Futurama" {
		t.Fatalf("second = %+v, want the remembered record (a re-resolution would refuse the renamed one)", second)
	}
	if secondProv != prov {
		t.Errorf("provenance = %q, want the remembered %q", secondProv, prov)
	}

	// The returned record must be a copy: a caller mutating it must not corrupt the
	// memo for every later caller (the Mapper is shared process-wide).
	second.Name = "mutation"
	third, _, err := m.resolveVerifiedTVDBSeries(ctx, 900)
	if err != nil || third.Name != "Futurama" {
		t.Fatalf("the memo must hand out copies, got %+v err=%v", third, err)
	}
}

// TestVerifiedSeriesMemoDisabledReResolves is the control: with no TTL the mapper is
// stateless, and the renamed record is refused on the second call.
func TestVerifiedSeriesMemoDisabledReResolves(t *testing.T) {
	m, ft := memoMapper(0)
	ctx := context.Background()

	if _, _, err := m.resolveVerifiedTVDBSeries(ctx, 900); err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	ft.seriesByID[73871] = &tvdb.SeriesBaseRecord{ID: 73871, Name: "Totally Different Show"}
	if _, _, err := m.resolveVerifiedTVDBSeries(ctx, 900); err == nil {
		t.Fatal("with the memo off, the renamed record must be re-verified and refused")
	}
}

// TestVerifiedSeriesMemoNeverRemembersFailures pins the rule the whole package keeps:
// a failed verification says nothing durable about the series. The record is fixed
// after the first refusal, and the second call must see the fix.
func TestVerifiedSeriesMemoNeverRemembersFailures(t *testing.T) {
	m, ft := memoMapper(time.Minute)
	ctx := context.Background()

	ft.seriesByID[73871] = &tvdb.SeriesBaseRecord{ID: 73871, Name: "隐身的名字"}
	if _, _, err := m.resolveVerifiedTVDBSeries(ctx, 900); err == nil {
		t.Fatal("expected a refusal for an uncorroborated link")
	}
	if len(m.verifiedSeries) != 0 {
		t.Fatalf("a refusal must not be memoised, map has %d entries", len(m.verifiedSeries))
	}

	ft.seriesByID[73871] = &tvdb.SeriesBaseRecord{ID: 73871, Name: "Futurama"}
	if _, _, err := m.resolveVerifiedTVDBSeries(ctx, 900); err != nil {
		t.Fatalf("the fixed record must resolve: %v", err)
	}
}

// TestVerifiedSeriesMemoExpires: a stale entry must be re-verified rather than served
// forever. A TTL of one nanosecond means the entry is already past by the next call.
func TestVerifiedSeriesMemoExpires(t *testing.T) {
	m, ft := memoMapper(time.Nanosecond)
	ctx := context.Background()

	if _, _, err := m.resolveVerifiedTVDBSeries(ctx, 900); err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	ft.seriesByID[73871] = &tvdb.SeriesBaseRecord{ID: 73871, Name: "Totally Different Show"}
	if _, _, err := m.resolveVerifiedTVDBSeries(ctx, 900); err == nil {
		t.Fatal("an expired entry must be re-verified, and the renamed record refused")
	}
}
