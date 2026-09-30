package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testBoundarySHA is the checksum of the synthetic geometry in
// writeTestBoundaries, computed the same way LoadBoundaries does.
func loadRealBoundaries(t *testing.T) *Boundaries {
	t.Helper()
	b, err := LoadBoundaries(DefaultBoundaryFile)
	if err != nil {
		t.Skipf("boundary geometry not present (%v); see data/boundaries/README.md", err)
	}
	return b
}

// Landmarks are the check that matters: each point is a place whose borough is
// not in dispute, so a wrong label here is a real error rather than a matter of
// interpretation. The two New Jersey points guard the failure mode that made the
// bounding boxes unusable.
func TestBoroughAtKnownLandmarks(t *testing.T) {
	b := loadRealBoundaries(t)
	if b.AreaCount() != 5 {
		t.Fatalf("AreaCount = %d want 5", b.AreaCount())
	}
	cases := []struct {
		name string
		lat  float64
		lon  float64
		want string
	}{
		{"Times Square", 40.7580, -73.9855, "manhattan"},
		{"Midtown Manhattan", 40.7549, -73.9840, "manhattan"},
		{"Lower East Side", 40.7180, -73.9870, "manhattan"},
		{"Harlem", 40.8116, -73.9465, "manhattan"},
		{"Yankee Stadium", 40.8296, -73.9262, "bronx"},
		{"Bronx Zoo", 40.8501, -73.8769, "bronx"},
		{"Coney Island", 40.5729, -73.9796, "brooklyn"},
		{"Williamsburg", 40.7081, -73.9571, "brooklyn"},
		{"Greenpoint", 40.7305, -73.9546, "brooklyn"},
		{"Flushing", 40.7590, -73.8295, "queens"},
		{"S Jamaica", 40.6984, -73.8076, "queens"},
		{"Rockaway", 40.5831, -73.8154, "queens"},
		{"St George", 40.6437, -74.0736, "staten_island"},
		{"Tottenville", 40.4928, -74.2492, "staten_island"},
		// Outside all five boroughs: the bounding boxes called these boroughs.
		{"Hoboken NJ", 40.7440, -74.0324, ""},
		{"Newark NJ", 40.7357, -74.1724, ""},
		{"Jersey City NJ", 40.7178, -74.0431, ""},
		// The water-inclusive Manhatten reaches east into the Harlem and East
		// rivers, so a point that a land-only map reads as Long Island is
		// Manhattan here. That is the intended consequence of this dataset.
		{"East Harlem river shore", 40.7810, -73.9660, "manhattan"},
		{"Newark DE", 39.7000, -75.7500, ""},
	}
	for _, tc := range cases {
		if got := b.BoroughAt(tc.lat, tc.lon); got != tc.want {
			t.Errorf("%s (%v,%v) = %q want %q", tc.name, tc.lat, tc.lon, got, tc.want)
		}
	}
}

// The bounding boxes and the real boundaries disagree on about a tenth of
// restaurants. These are the concrete disagreements, pinned so a regression to
// box-based labelling is caught.
func TestBoundariesDisagreeWithBoundingBoxes(t *testing.T) {
	b := loadRealBoundaries(t)
	cases := []struct {
		name   string
		lat    float64
		lon    float64
		exact  string
		approx string
	}{
		{"Manhattan east side is Queens", 40.74273, -73.91763, "queens", "manhattan"},
		{"Lower Manhattan is Brooklyn", 40.70565, -73.97794, "brooklyn", "manhattan"},
		{"Long Island City is Queens", 40.71113, -73.87375, "queens", "brooklyn"},
		// New Jersey: Jersey City sits inside the Brooklyn box and Far Rockaway
		// inside the Queens box, both of which the real boundaries exclude.
		{"Jersey City is not a borough", 40.7178, -74.0431, "", "brooklyn"},
		{"Far Rockaway is not a borough", 40.79246, -73.73285, "", "queens"},
	}
	for _, tc := range cases {
		if got := b.BoroughAt(tc.lat, tc.lon); got != tc.exact {
			t.Errorf("%s: boundary = %q want %q", tc.name, got, tc.exact)
		}
		if got := newBoroughResolver(nil).borough(tc.lat, tc.lon); got != tc.approx {
			t.Errorf("%s: fallback box = %q want %q", tc.name, got, tc.approx)
		}
	}
}

// With geometry loaded the fallback must not widen a point that sits just
// outside a boundary back into a borough.
func TestBoroughResolverPrefersBoundariesOverFallback(t *testing.T) {
	b := loadRealBoundaries(t)
	resolver := newBoroughResolver(b)
	if !resolver.Exact() {
		t.Error("Exact() = false with boundaries loaded")
	}
	if got := resolver.borough(40.7440, -74.0324); got != "" {
		t.Errorf("Hoboken = %q want empty; the fallback box leaked", got)
	}
	if got := resolver.version(); got != DefaultBoundaryVersion {
		t.Errorf("version = %q want %q", got, DefaultBoundaryVersion)
	}
	if newBoroughResolver(nil).Exact() {
		t.Error("Exact() = true without boundaries")
	}
	if newBoroughResolver(nil).version() != "" {
		t.Error("fallback reported a boundary version")
	}
}

// Every vertex and edge midpoint of every borough must resolve to that borough.
// This is the property that makes the labels trustworthy at the boundary, where
// restaurants on a waterfront sit.
// Every borough's ring must enclose a usable amount of area, and points sampled
// from inside each ring's bounds must resolve to that borough far more often
// than to any other. This is the invariant that makes the labels trustworthy:
// a borough is a real region, not a sliver.
func TestBoundaryInteriorsDominate(t *testing.T) {
	b := loadRealBoundaries(t)
	for _, area := range b.areas {
		for _, poly := range area.polygons {
			mine, other := 0, 0
			const steps = 40
			for i := 0; i < steps; i++ {
				for j := 0; j < steps; j++ {
					// Sample the cell interior rather than the exact bound so a
					// boundary-hugging point is not counted against the region.
					lat := poly.minLat + (poly.maxLat-poly.minLat)*(float64(i)+0.5)/steps
					lon := poly.minLon + (poly.maxLon-poly.minLon)*(float64(j)+0.5)/steps
					switch got := b.BoroughAt(lat, lon); got {
					case area.name:
						mine++
					case "":
					default:
						other++
					}
				}
			}
			if mine == 0 {
				t.Errorf("%s: no sampled point inside its own bounds resolved to it", area.name)
			}
			// A concave borough (Manhattan, Staten Island) legitimately loses
			// samples to the water around it, but it must not lose its own
			// interior to a neighbouring borough.
			if other > mine {
				t.Errorf("%s: %d sampled points resolved to another borough, %d to itself",
					area.name, other, mine)
			}
		}
	}
}

// Manhattan is three polygons, so the loader must keep all of them rather than
// collapsing to the first.
func TestMultiPolygonPartsAreAllLoaded(t *testing.T) {
	b := loadRealBoundaries(t)
	counts := map[string]int{}
	for _, area := range b.areas {
		counts[area.name] = len(area.polygons)
	}
	if counts["manhattan"] < 3 {
		t.Errorf("manhattan polygons = %d want at least 3 (main island plus islets)",
			counts["manhattan"])
	}
	for name, n := range counts {
		if n == 0 {
			t.Errorf("%s has no polygons", name)
		}
	}
}

// The Manhattan islets only matter if a point on one resolves. Roosevelt
// Island sits in the East River between Manhattan and Queens.
func TestWaterInclusiveManhattanReachesIntoRiver(t *testing.T) {
	b := loadRealBoundaries(t)
	// A point in the East River that a land-only boundary would not claim.
	lat, lon := 40.7484, -73.9455
	got := b.BoroughAt(lat, lon)
	if got == "" {
		t.Errorf("(%v,%v) resolved to no borough; water-inclusive geometry should reach it", lat, lon)
	}
}

func TestLoadBoundariesRejectsChecksumMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.geojson")
	writeTestBoundaries(t, path)
	if _, err := LoadBoundaries(path); err == nil {
		t.Fatal("LoadBoundaries accepted geometry that does not match the pinned checksum")
	}
}

func TestLoadBoundariesAcceptsPinnedChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.geojson")
	sum := writeTestBoundaries(t, path)
	b, err := loadBoundaries(path, sum, "test-version")
	if err != nil {
		t.Fatalf("loadBoundaries: %v", err)
	}
	if b.Version() != "test-version" {
		t.Errorf("Version = %q", b.Version())
	}
	if b.AreaCount() != 1 {
		t.Errorf("AreaCount = %d want 1", b.AreaCount())
	}
	// A unit square from (0,0) to (1,1), unclosed.
	if got := b.BoroughAt(0.5, 0.5); got != "test_area" {
		t.Errorf("interior = %q want test_area", got)
	}
	for _, outside := range [][2]float64{{-0.1, 0.5}, {1.1, 0.5}, {0.5, -0.1}, {0.5, 1.1}} {
		if got := b.BoroughAt(outside[0], outside[1]); got != "" {
			t.Errorf("(%v,%v) = %q want empty", outside[0], outside[1], got)
		}
	}
}

// A hole must subtract, otherwise a boundary file that gains one starts
// labelling the water or park inside it.
func TestPolygonHoleIsExcluded(t *testing.T) {
	outer := ring{{0, 0}, {0, 10}, {10, 10}, {10, 0}, {0, 0}}
	hole := ring{{4, 4}, {4, 6}, {6, 6}, {6, 4}, {4, 4}}
	poly := polygon{exterior: outer, holes: []ring{hole}}
	poly.minLat, poly.minLon = 0, 0
	poly.maxLat, poly.maxLon = 10, 10
	if !poly.contains(1, 1) {
		t.Error("exterior interior was rejected")
	}
	if poly.contains(5, 5) {
		t.Error("point inside the hole was claimed")
	}
	// Winding is deliberately reversed here: even-odd fill must not care.
	reversed := ring{{4, 4}, {6, 4}, {6, 6}, {4, 6}, {4, 4}}
	poly.holes = []ring{reversed}
	if poly.contains(5, 5) {
		t.Error("hole with reversed winding was not subtracted")
	}
}

func TestPolygonBoundsRejectFast(t *testing.T) {
	// A right triangle, so its bounding box is strictly larger than its area and
	// the bounds pre-check has something to reject.
	triangle := ring{{10, 10}, {10, 30}, {30, 10}, {10, 10}}
	poly := polygon{exterior: triangle, minLat: 10, maxLat: 30, minLon: 10, maxLon: 30}
	if !poly.contains(15, 15) {
		t.Error("interior of the triangle was rejected")
	}
	if poly.contains(25, 25) {
		t.Error("point inside the bounding box but outside the triangle was claimed")
	}
}

func TestLoadBoundariesRejectsMalformedInput(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"not json":         `{`,
		"empty features":   `[]`,
		"unknown geometry": `[{"boroname":"X","the_geom":{"type":"LineString","coordinates":[]}}]`,
		"missing boroname": `[{"the_geom":{"type":"Polygon","coordinates":[[[0,0],[0,1],[1,1],[0,0]]]}}]`,
		"short position":   `[{"boroname":"X","the_geom":{"type":"Polygon","coordinates":[[[0,0],[0,1],[1]]]}}]`,
		"degenerate ring":  `[{"boroname":"X","the_geom":{"type":"Polygon","coordinates":[[[0,0],[0,0],[0,0]]]}}]`,
	}
	for name, body := range cases {
		path := filepath.Join(dir, name+".geojson")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadBoundaries(path, "", "v"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := loadBoundaries(filepath.Join(dir, "missing.geojson"), "", "v"); err == nil {
		t.Error("missing file: expected an error")
	}
	if _, err := loadBoundaries("", "", "v"); err == nil {
		t.Error("empty path: expected an error")
	}
}

func TestLoadBoundariesRejectsNonFiniteCoordinate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nan.geojson")
	// 1e999 overflows to +Inf when parsed.
	body := `[{"boroname":"X","the_geom":{"type":"Polygon","coordinates":[[[0,0],[0,1],[1,1],[1e999,0]]]}}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBoundaries(path, "", "v"); err == nil {
		t.Error("expected an error for a non-finite coordinate")
	}
}

func TestRingContainsEdgeCases(t *testing.T) {
	if ringContains(nil, 0, 0) {
		t.Error("empty ring claimed a point")
	}
	if ringContains(ring{{0, 0}, {1, 0}}, 0.5, 0.5) {
		t.Error("two-point ring claimed a point")
	}
	// Horizontal edges must not divide by zero: the crossing test skips them.
	square := ring{{0, 0}, {0, 1}, {1, 1}, {1, 0}, {0, 0}}
	if !ringContains(square, 0.5, 0.5) {
		t.Error("square interior was rejected")
	}
	if ringContains(square, 1.5, 0.5) {
		t.Error("point outside the square was claimed")
	}
}

func TestBoroughSlug(t *testing.T) {
	cases := map[string]string{
		"Staten Island": "staten_island",
		"  Bronx  ":     "bronx",
		"St. John's":    "st._johns",
	}
	for in, want := range cases {
		if got := boroughSlug(in); got != want {
			t.Errorf("boroughSlug(%q) = %q want %q", in, got, want)
		}
	}
}

// A nil Boundaries must answer "" rather than panicking, because the fallback
// path constructs one when geometry is unavailable.
func TestNilBoundariesAreSafe(t *testing.T) {
	var b *Boundaries
	if got := b.BoroughAt(40.7, -74.0); got != "" {
		t.Errorf("BoroughAt = %q want empty", got)
	}
	if b.AreaCount() != 0 || b.Version() != "" {
		t.Error("nil boundaries reported area count or version")
	}
}

// writeTestBoundaries writes a one-feature file and returns its SHA-256.
func writeTestBoundaries(t *testing.T, path string) string {
	t.Helper()
	body := `[{"boroname":"Test Area","the_geom":{"type":"Polygon","coordinates":[[[0,0],[0,1],[1,1],[1,0]]]}}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// The end-to-end wiring: NormalizeMeta must label from geometry when it is
// supplied, and from the fallback boxes when it is not.
func TestNormalizeMetaUsesBoundaryGeometry(t *testing.T) {
	b := loadRealBoundaries(t)
	observed := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name            string
		lat, lon        float64
		withGeometry    string
		withoutGeometry string
	}{
		{"Midtown", 40.7549, -73.9840, "manhattan", "manhattan"},
		{"Manhattan east side", 40.74273, -73.91763, "queens", "manhattan"},
		{"Long Island City", 40.71113, -73.87375, "queens", "brooklyn"},
		{"Jersey City", 40.7178, -74.0431, "", "brooklyn"},
	}
	for _, tc := range cases {
		meta := sampleMeta()
		meta.Latitude = ptr(tc.lat)
		meta.Longitude = ptr(tc.lon)

		with, err := NormalizeMeta(meta, MetaOptions{ObservedAt: observed, Boroughs: b})
		if err != nil {
			t.Fatalf("%s: NormalizeMeta with geometry: %v", tc.name, err)
		}
		if with.BoroughGuess != tc.withGeometry {
			t.Errorf("%s: with geometry = %q want %q", tc.name, with.BoroughGuess, tc.withGeometry)
		}

		without, err := NormalizeMeta(meta, MetaOptions{ObservedAt: observed})
		if err != nil {
			t.Fatalf("%s: NormalizeMeta without geometry: %v", tc.name, err)
		}
		if without.BoroughGuess != tc.withoutGeometry {
			t.Errorf("%s: without geometry = %q want %q", tc.name, without.BoroughGuess, tc.withoutGeometry)
		}
	}
}

// The publisher intermittently answers a fetch with a short JSON error object.
// That must be reported as a failed download, not as a checksum mismatch.
func TestLoadBoundariesReportsProviderError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "err.geojson")
	body := `{"message":"Service unavailable","errorCode":"service-unavailable","data":{}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBoundaries(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Service unavailable") {
		t.Errorf("error does not surface the provider message: %v", err)
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("error does not suggest retrying: %v", err)
	}
	if strings.Contains(err.Error(), "does not match the pinned") {
		t.Errorf("provider error was misreported as a checksum mismatch: %v", err)
	}
}

// A genuine content change gets the review-first message.
func TestLoadBoundariesChecksumMessageAsksForReview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "changed.geojson")
	body := `[{"boroname":"X","the_geom":{"type":"Polygon","coordinates":[[[0,0],[0,1],[1,1],[1,0]]]}}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadBoundaries(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "does not match the pinned") {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "reviewed") {
		t.Errorf("checksum error does not ask for a review: %v", err)
	}
}
