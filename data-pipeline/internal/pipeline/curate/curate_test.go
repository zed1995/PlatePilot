package curate

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
)

func ptr[T any](v T) *T { return &v }

func TestIsFoodPlace(t *testing.T) {
	cases := map[string]bool{
		"Joe's Pizza":       true,
		"Sushi Bar":         true,
		"Local Coffee Shop": true,
		"Downtown Bakery":   true,
		"City Museum":       false,
		"Central Park":      false,
	}
	for name, want := range cases {
		categories := []string{name}
		if got := IsFoodPlace(categories); got != want {
			t.Errorf("IsFoodPlace(%q) = %v want %v", name, got, want)
		}
	}
	if IsFoodPlace(nil) {
		t.Error("IsFoodPlace(nil) = true")
	}
}

// Regression: substring matching wrongly accepted barbers, libraries, and
// couriers because "bar", "pub", and "deli" are substrings of longer words.
func TestIsFoodPlaceRejectsSubstringFalsePositives(t *testing.T) {
	falsePositives := map[string][]string{
		"Barber shop":                  {"Barber shop"},
		"Hair salon + Barber":          {"Hair salon", "Barber shop", "Beauty salon"},
		"Public library":               {"Public library"},
		"Public swimming pool":         {"Public swimming pool"},
		"Shipping and mailing service": {"Shipping and mailing service", "Freight forwarding service"},
		"Flower delivery":              {"Florist", "Flower delivery", "Flower designer"},
		"Publisher":                    {"Newspaper publisher"},
		"Cosmetics store":              {"Beauty supply store", "Cosmetics store"},
	}
	for name, categories := range falsePositives {
		if IsFoodPlace(categories) {
			t.Errorf("IsFoodPlace(%v) = true for %s; want false", categories, name)
		}
	}

	// Genuine food places must still pass, including the plural bar form.
	truePositives := [][]string{
		{"Barber shop", "Pizza restaurant"},
		{"Bar"},
		{"Bars"},
		{"Cocktail bar"},
		{"Pub"},
		{"Cafe"},
		{"Café"},
		{"Ice cream shop"},
		{"Coffee shop"},
		{"Fast food restaurant"},
	}
	for _, categories := range truePositives {
		if !IsFoodPlace(categories) {
			t.Errorf("IsFoodPlace(%v) = false, want true", categories)
		}
	}
}

func TestCuisineTags(t *testing.T) {
	got := CuisineTags([]string{"Pizza restaurant", "Italian restaurant", "Restaurant"})
	want := []string{"pizza", "italian"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CuisineTags = %v want %v", got, want)
	}
	// An unmapped "<x> restaurant" category derives a slug; the bare
	// "Restaurant" category is too generic to become a cuisine.
	got = CuisineTags([]string{"Ethiopian restaurant", "Restaurant"})
	if !reflect.DeepEqual(got, []string{"ethiopian"}) {
		t.Fatalf("CuisineTags generic = %v", got)
	}
}

func TestPriceLevel(t *testing.T) {
	cases := []struct {
		in   *string
		want *int
	}{
		{nil, nil},
		{ptr(""), nil},
		{ptr("$"), ptr(1)},
		{ptr("$$"), ptr(2)},
		{ptr("$$$$"), ptr(4)},
		{ptr("$$$$$"), nil},
		{ptr("₩"), nil},
		{ptr("$$ - $$$"), nil},
	}
	for _, tc := range cases {
		got := PriceLevel(tc.in)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("PriceLevel(%v) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseHours(t *testing.T) {
	raw := [][]string{
		{"Monday", "11AM\u201310PM"},
		{"Tuesday", "Closed"},
		{"Wednesday", "10PM\u20132AM"},
		{"Thursday", "Open 24 hours"},
		{"Notaday", "9AM-5PM"},
		{"Friday"},
	}
	got := ParseHours(raw)
	want := []restaurant.HoursEntry{
		{Weekday: 1, OpenMinute: 660, CloseMinute: 1320, Raw: "11AM\u201310PM"},
		{Weekday: 2, IsClosed: true, Raw: "Closed"},
		{Weekday: 3, OpenMinute: 1320, CloseMinute: 1560, Raw: "10PM\u20132AM"},
		{Weekday: 4, OpenMinute: 0, CloseMinute: 1440, Raw: "Open 24 hours"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseHours = %#v want %#v", got, want)
	}
}

func TestParseHoursHandlesMultipleIntervalsPerDay(t *testing.T) {
	got := ParseHours([][]string{{"Monday", "11AM\u20132PM", "5PM\u201310PM"}})
	if len(got) != 2 {
		t.Fatalf("got %d intervals want 2: %#v", len(got), got)
	}
	if got[1].OpenMinute != 17*60 || got[1].CloseMinute != 22*60 {
		t.Errorf("second interval = %#v", got[1])
	}
}

func TestNormalizeAttributesTriState(t *testing.T) {
	misc := raw.MISC{
		"Service options": {"Outdoor seating", "Takeout", "no delivery"},
		"Atmosphere":      {"Casual", "Cozy"},
		"Popular for":     {"Lunch"},
	}
	attrs := NormalizeAttributes(misc)
	if attrs.TriStates["outdoor_seating"] != restaurant.TriStateTrue {
		t.Errorf("outdoor_seating = %q", attrs.TriStates["outdoor_seating"])
	}
	if attrs.TriStates["delivery"] != restaurant.TriStateFalse {
		t.Errorf("delivery = %q want false", attrs.TriStates["delivery"])
	}
	// An absent label must stay "unknown", never "false".
	if attrs.TriStates["accepts_reservations"] != restaurant.TriStateUnknown {
		t.Errorf("accepts_reservations = %q want unknown", attrs.TriStates["accepts_reservations"])
	}
	if !reflect.DeepEqual(attrs.AtmosphereTags, []string{"casual", "cozy"}) {
		t.Errorf("atmosphere = %v", attrs.AtmosphereTags)
	}
	if !reflect.DeepEqual(attrs.PopularForTags, []string{"lunch"}) {
		t.Errorf("popular_for = %v", attrs.PopularForTags)
	}
}

func TestValidCoordinatesAndBorough(t *testing.T) {
	if ValidCoordinates(ptr(0.0), ptr(0.0)) {
		t.Error("null island should be invalid")
	}
	if ValidCoordinates(nil, ptr(1.0)) {
		t.Error("nil latitude should be invalid")
	}
	if ValidCoordinates(ptr(91.0), ptr(0.0)) {
		t.Error("out of range latitude should be invalid")
	}
	if !ValidCoordinates(ptr(40.73), ptr(-74.00)) {
		t.Error("NYC coordinates should be valid")
	}
	if got := BoroughGuess(40.73, -74.00); got != "manhattan" {
		t.Errorf("BoroughGuess = %q want manhattan", got)
	}
	if got := BoroughGuess(0, 0); got != "" {
		t.Errorf("BoroughGuess off-map = %q", got)
	}
}

func TestScrubPII(t *testing.T) {
	in := "Great food! Email me at jane.doe@example.com or call (212) 555-1234."
	got := ScrubPII(in)
	if strings.Contains(got, "jane.doe@example.com") || strings.Contains(got, "555-1234") {
		t.Fatalf("PII survived scrubbing: %q", got)
	}
	if !strings.Contains(got, "[redacted-email]") || !strings.Contains(got, "[redacted-phone]") {
		t.Fatalf("expected redaction markers in %q", got)
	}
}

func TestReviewDedupKeyIsDeterministic(t *testing.T) {
	hash := TextHash("Great pizza")
	a := ReviewDedupKey("gmap-1", "user-1", 1614600000000, hash)
	b := ReviewDedupKey("gmap-1", "user-1", 1614600000000, hash)
	if a != b {
		t.Fatalf("ReviewDedupKey not deterministic: %q vs %q", a, b)
	}
	if c := ReviewDedupKey("gmap-1", "user-2", 1614600000000, hash); c == a {
		t.Error("different user_id must produce a different dedup key")
	}
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Errorf("unexpected dedup key shape: %q", a)
	}
	// Whitespace-only differences must not change the text hash.
	if TextHash("a  b\n c") != TextHash("a b c") {
		t.Error("text hash should ignore whitespace differences")
	}
}

func sampleMeta() raw.Meta {
	return raw.Meta{
		Name:            "Joe's Pizza",
		Address:         ptr("7 Carmine St, New York, NY"),
		GmapID:          "gmap-1",
		Description:     ptr("Slice shop"),
		Latitude:        ptr(40.730),
		Longitude:       ptr(-74.002),
		Category:        []string{"Pizza restaurant", "Restaurant"},
		AvgRating:       ptr(4.5),
		NumOfReviews:    ptr(9998),
		Price:           ptr("$$"),
		Hours:           [][]string{{"Monday", "11AM\u201310PM"}},
		MISC:            raw.MISC{"Service options": {"Outdoor seating"}},
		State:           ptr("Open"),
		RelativeResults: []string{"rel-1", "rel-2"},
		URL:             "https://maps.example/x",
	}
}

func TestNormalizeMeta(t *testing.T) {
	observed := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	r, err := NormalizeMeta(sampleMeta(), MetaOptions{ObservedAt: observed, ServiceArea: NYCServiceArea})
	if err != nil {
		t.Fatalf("NormalizeMeta: %v", err)
	}
	if r.ID != 0 || r.SourceRecordID != "gmap-1" || r.Name != "Joe's Pizza" {
		t.Fatalf("identity = %+v", r)
	}
	if r.Location == nil || r.Location.Longitude != -74.002 || r.BoroughGuess != "manhattan" {
		t.Errorf("location = %+v borough=%q", r.Location, r.BoroughGuess)
	}
	if r.Price.Level == nil || *r.Price.Level != 2 {
		t.Errorf("price = %+v", r.Price)
	}
	if r.SnapshotStatus != restaurant.StatusOpen {
		t.Errorf("snapshot status = %q", r.SnapshotStatus)
	}
	if !r.ReviewStats.SourceReviewCountCapped || r.ReviewStats.SourceReviewCount != 9998 {
		t.Errorf("source review count = %+v", r.ReviewStats)
	}
	if !reflect.DeepEqual(r.CuisineTags, []string{"pizza", "italian"}) {
		t.Errorf("cuisines = %v", r.CuisineTags)
	}
	// Hours, the raw MISC object, and relative results are embedded on the same
	// document rather than split into a side collection.
	if len(r.Hours) != 1 || r.Hours[0].OpenMinute != 660 || r.Hours[0].Raw == "" {
		t.Errorf("hours not embedded: %+v", r.Hours)
	}
	if len(r.AttributesRaw["Service options"]) != 1 {
		t.Errorf("raw attributes not embedded: %+v", r.AttributesRaw)
	}
	if len(r.RelativeResults) != 2 || r.RelativeResults[0] != "rel-1" {
		t.Errorf("relative results not embedded: %+v", r.RelativeResults)
	}
}

func TestNormalizeMetaFilterAndReject(t *testing.T) {
	observed := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

	notFood := sampleMeta()
	notFood.Category = []string{"Museum"}
	if _, err := NormalizeMeta(notFood, MetaOptions{ObservedAt: observed, ServiceArea: NYCServiceArea}); !errors.Is(err, ErrFiltered) {
		t.Errorf("non-food want ErrFiltered, got %v", err)
	}

	noGmap := sampleMeta()
	noGmap.GmapID = ""
	if _, err := NormalizeMeta(noGmap, MetaOptions{ObservedAt: observed, ServiceArea: NYCServiceArea}); err == nil {
		t.Error("missing gmap_id should be rejected")
	}

	badCoords := sampleMeta()
	badCoords.Latitude = nil
	if _, err := NormalizeMeta(badCoords, MetaOptions{ObservedAt: observed, ServiceArea: NYCServiceArea}); err == nil {
		t.Error("missing coordinates should be rejected")
	}

	noName := sampleMeta()
	noName.Name = "  "
	if _, err := NormalizeMeta(noName, MetaOptions{ObservedAt: observed, ServiceArea: NYCServiceArea}); err == nil {
		t.Error("missing name should be rejected")
	}
}

func TestNormalizeMetaFiltersOutsideServiceArea(t *testing.T) {
	observed := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	opts := MetaOptions{ObservedAt: observed, ServiceArea: NYCServiceArea}

	inside := sampleMeta()
	if _, err := NormalizeMeta(inside, opts); err != nil {
		t.Fatalf("NYC place should be accepted: %v", err)
	}

	for name, coords := range map[string][2]float64{
		"los_angeles": {34.05, -118.24},
		"chicago":     {41.88, -87.63},
		"miami":       {25.76, -80.19},
	} {
		outside := sampleMeta()
		outside.Latitude = &coords[0]
		outside.Longitude = &coords[1]
		if _, err := NormalizeMeta(outside, opts); !errors.Is(err, ErrFiltered) {
			t.Errorf("%s place want ErrFiltered, got %v", name, err)
		}
	}

	// A zero area disables the filter.
	unscoped := sampleMeta()
	unscoped.Latitude = ptr(34.05)
	unscoped.Longitude = ptr(-118.24)
	if _, err := NormalizeMeta(unscoped, MetaOptions{ObservedAt: observed}); err != nil {
		t.Errorf("zero area should disable filtering, got %v", err)
	}
}

func TestParseServiceArea(t *testing.T) {
	area, err := ParseServiceArea("")
	if err != nil || area != NYCServiceArea {
		t.Fatalf("empty should default to NYC: %v %+v", err, area)
	}
	area, err = ParseServiceArea("40.0,-74.5,41.0,-73.0")
	if err != nil || !area.Contains(40.5, -74.0) || area.Contains(39.0, -74.0) {
		t.Fatalf("parse: %v %+v", err, area)
	}
	for _, bad := range []string{"1,2,3", "a,b,c,d", "41,,-74,40,-73"} {
		if _, err := ParseServiceArea(bad); err == nil {
			t.Errorf("ParseServiceArea(%q) should fail", bad)
		}
	}
}

func TestNormalizeReviewBlanksShortText(t *testing.T) {
	observed := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	at := time.Date(2021, 3, 1, 12, 0, 0, 0, time.UTC)
	opts := ReviewOptions{MinTextChars: 20, ObservedAt: observed}

	long := raw.Review{UserID: "u1", GmapID: "gmap-1", Rating: 5, Time: at.UnixMilli(), Text: ptr("Great pizza and very fast service, would return.")}
	got, err := NormalizeReview(long, 1, opts)
	if err != nil {
		t.Fatalf("NormalizeReview: %v", err)
	}
	if got.Text == "" || got.Rating != 5 || !got.ReviewedAt.Equal(at) {
		t.Fatalf("review = %+v", got)
	}
	if got.ID != 0 || got.TextHash == "" {
		t.Errorf("review id should be DB-assigned (zero) and text hash present: %+v", got)
	}
	if got.RestaurantID != 1 {
		t.Errorf("restaurant id = %d want 1", got.RestaurantID)
	}
	// The database assigns reviews.id, so an unresolved restaurant must be
	// rejected rather than written with a zero foreign key.
	if _, err := NormalizeReview(long, 0, opts); err == nil {
		t.Error("review with unresolved restaurant_id was accepted")
	}

	short := raw.Review{UserID: "u1", GmapID: "gmap-1", Rating: 4, Time: at.UnixMilli(), Text: ptr("ok")}
	got, err = NormalizeReview(short, 1, opts)
	if err != nil {
		t.Fatalf("NormalizeReview short: %v", err)
	}
	if got.Text != "" {
		t.Errorf("short text should be blanked, got %q", got.Text)
	}
	if got.Rating != 4 {
		t.Errorf("short review should still keep its rating, got %d", got.Rating)
	}

	for _, bad := range []raw.Review{
		{UserID: "u", GmapID: "gmap-1", Rating: 9, Time: at.UnixMilli()},
		{UserID: "u", GmapID: "gmap-1", Rating: 3, Time: 0},
		{UserID: "u", Rating: 3, Time: at.UnixMilli()},
	} {
		if _, err := NormalizeReview(bad, 1, opts); err == nil {
			t.Errorf("invalid review accepted: %+v", bad)
		}
	}
}

func TestSnapshotStatus(t *testing.T) {
	cases := map[string]restaurant.SnapshotStatus{
		"Open":               restaurant.StatusOpen,
		"open":               restaurant.StatusOpen,
		"Closed":             restaurant.StatusClosed,
		"Permanently closed": restaurant.StatusPermanentlyClosed,
		"Something else":     restaurant.StatusUnknown,
	}
	for in, want := range cases {
		if got := SnapshotStatus(ptr(in)); got != want {
			t.Errorf("SnapshotStatus(%q) = %q want %q", in, got, want)
		}
	}
	if got := SnapshotStatus(nil); got != restaurant.StatusUnknown {
		t.Errorf("SnapshotStatus(nil) = %q", got)
	}
}

func TestKnowledgeScoreAndSelection(t *testing.T) {
	rich := restaurant.Restaurant{
		ID:             1,
		SourceRecordID: "g-1",
		Description:    "Nice place",
		Address:        "1 St",
		Location:       &restaurant.GeoPoint{},
		CuisineTags:    []string{"pizza"},
		SnapshotStatus: restaurant.StatusOpen,
		ReviewStats:    restaurant.ReviewStats{TextReviewCount: 500},
	}
	poor := restaurant.Restaurant{
		ID:             2,
		SourceRecordID: "g-2",
		Name:           "Bare",
		SnapshotStatus: restaurant.StatusOpen,
		Location:       &restaurant.GeoPoint{},
	}
	closed := restaurant.Restaurant{
		ID:             3,
		SourceRecordID: "g-3",
		SnapshotStatus: restaurant.StatusPermanentlyClosed,
		Location:       &restaurant.GeoPoint{},
	}
	if KnowledgeScore(rich).Score <= KnowledgeScore(poor).Score {
		t.Error("richer restaurant should score higher")
	}
	if KnowledgeScore(closed).Score != -100 {
		t.Errorf("permanently closed score = %v", KnowledgeScore(closed).Score)
	}

	all := []restaurant.Restaurant{rich, poor, closed}
	active := SelectActiveForDemo(all, 1)
	if len(active) != 2 {
		t.Fatalf("active = %v want 2 (closed excluded, target below the minimum)", active)
	}
	if !containsID(active, 1) || !containsID(active, 2) {
		t.Errorf("active = %v", active)
	}
	if containsID(active, 3) {
		t.Error("permanently closed restaurant must not be selected")
	}
	// Determinism.
	if !reflect.DeepEqual(active, SelectActiveForDemo(all, 1)) {
		t.Error("selection is not deterministic")
	}
}

func TestBuildReviewStatsAndComputedRating(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	last := time.Date(2021, 8, 1, 0, 0, 0, 0, time.UTC)
	avg := 4.33
	previous := restaurant.ReviewStats{
		SourceReviewCount:       9998,
		SourceReviewCountCapped: true,
		EmbeddedReviewCount:     7,
	}
	counts := review.Counts{StoredCount: 3, TextCount: 2, RepresentativeCount: 1, LastReviewedAt: &last, ComputedAvg: &avg}
	stats := BuildReviewStats(previous, counts, now)
	if stats.StoredReviewCount != 3 || stats.TextReviewCount != 2 || stats.RepresentativeReviewCount != 1 {
		t.Errorf("stats = %+v", stats)
	}
	// Fields owned by other stages must be preserved.
	if stats.SourceReviewCount != 9998 || !stats.SourceReviewCountCapped || stats.EmbeddedReviewCount != 7 {
		t.Errorf("source/embedded stats not preserved: %+v", stats)
	}
	if !stats.StatsUpdatedAt.Equal(now) {
		t.Errorf("stats_updated_at = %v", stats.StatsUpdatedAt)
	}

	source := restaurant.Rating{SourceAvg: ptr(4.5)}
	computed := ComputedRating(source, counts)
	if computed.SourceAvg == nil || *computed.SourceAvg != 4.5 {
		t.Errorf("source avg overwritten: %+v", computed)
	}
	if computed.ComputedAvg == nil || *computed.ComputedAvg != 4.33 || computed.RatingCountForComputedAvg != 3 {
		t.Errorf("computed rating = %+v", computed)
	}
}

func TestMissingMetaFields(t *testing.T) {
	got := MissingMetaFields(sampleMeta())
	if len(got) != 0 {
		t.Errorf("complete meta reported missing %v", got)
	}
	sparse := raw.Meta{GmapID: "g", Name: "n", Category: []string{"Cafe"}, Latitude: ptr(1.0), Longitude: ptr(2.0)}
	got = MissingMetaFields(sparse)
	for _, want := range []string{"address", "description", "price", "hours", "misc", "avg_rating", "num_of_reviews"} {
		if !contains(got, want) {
			t.Errorf("missing fields %v should contain %q", got, want)
		}
	}
}

func containsID(values []int64, want int64) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// Regression: a review contained a literal NUL byte, which PostgreSQL rejects
// (SQLSTATE 22021) while a document store accepts silently. Scrubbing happens in
// the curation layer so the rule applies to every backend.
func TestScrubPIIRemovesControlCharacters(t *testing.T) {
	cases := map[string]string{
		"LAST RESORT—\x00Worst service": "LAST RESORT—Worst service",
		"line\nbreak\ttab":              "line\nbreak\ttab",
		"carriage\r\nreturn":            "carriage\r\nreturn",
		"bell\a and del\x7f":            "bell and del",
		"vertical\x0btab and form\x0c":  "verticaltab and form",
		"paragraph sep\u2028here":       "paragraph sephere",
		"clean text":                    "clean text",
		"":                              "",
	}
	for in, want := range cases {
		if got := ScrubPII(in); got != want {
			t.Errorf("ScrubPII(%q) = %q want %q", in, got, want)
		}
	}
}

// ScrubPII must still mask PII after the control-character pass, so neither rule
// can shadow the other.
func TestScrubPIIStillMasksAfterStripping(t *testing.T) {
	got := ScrubPII("mail me at a\x00b@example.com or call 212-555-0199")
	if strings.Contains(got, "a@b.example.com") {
		t.Errorf("email survived scrubbing: %q", got)
	}
	if strings.Contains(got, "212-555-0199") {
		t.Errorf("phone survived scrubbing: %q", got)
	}
	if strings.ContainsRune(got, '\x00') {
		t.Errorf("NUL survived scrubbing: %q", got)
	}
}
