package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/zed/platepilot/shared/domain/errs"
)

// DefaultBoundaryFile is the borough geometry used to label restaurants. The
// file itself is git-ignored; data/boundaries/README.md records where it came
// from and what it must hash to.
const DefaultBoundaryFile = "data/boundaries/nyc-borough-boundaries-water.geojson"

// DefaultBoundarySHA256 pins the exact geometry this code was written against.
// A changed download has to be reviewed deliberately, because every restaurant
// label in the corpus is derived from it.
const DefaultBoundarySHA256 = "9c271db18ea8a76b2c9f030159710e45cfd33e236285ad84b8c18c408cdb1591"

// DefaultBoundaryVersion is the NYC DCP borough boundary release the pinned
// geometry came from. It is written to the batch report so a label can be traced
// back to the boundary release that produced it.
const DefaultBoundaryVersion = "nyc-borough-wh2p-dxnf-26b"

// position is a GeoJSON coordinate pair, longitude first per RFC 7946.
type position struct {
	Lon float64
	Lat float64
}

// ring is one closed edge of a polygon: the exterior boundary, or a hole.
type ring []position

// polygon is an exterior ring plus its holes.
type polygon struct {
	exterior ring
	holes    []ring
	// bounds rejects almost every candidate point before any edge is tested.
	minLat, maxLat float64
	minLon, maxLon float64
}

func (p polygon) contains(lat, lon float64) bool {
	if lat < p.minLat || lat > p.maxLat || lon < p.minLon || lon > p.maxLon {
		return false
	}
	if !ringContains(p.exterior, lat, lon) {
		return false
	}
	// Even-odd fill makes a hole subtract regardless of ring winding, so the
	// borough area is never double counted where a boundary data quirk
	// disagrees with RFC 7946 orientation.
	for _, hole := range p.holes {
		if ringContains(hole, lat, lon) {
			return false
		}
	}
	return true
}

// ringContains runs the crossing-number test, casting a ray from the point
// toward +longitude and counting the edges it passes. A point lying exactly on
// an edge is undetermined and resolves to whatever parity the edges produce.
func ringContains(r ring, lat, lon float64) bool {
	inside := false
	n := len(r)
	if n < 3 {
		return false
	}
	prev := r[n-1]
	for i := 0; i < n; i++ {
		curr := r[i]
		// A horizontal edge can never be crossed, and this test also keeps the
		// interpolation below free of a division by zero.
		if (curr.Lat > lat) != (prev.Lat > lat) {
			t := (lat - prev.Lat) / (curr.Lat - prev.Lat)
			if prev.Lon+t*(curr.Lon-prev.Lon) > lon {
				inside = !inside
			}
		}
		prev = curr
	}
	return inside
}

// Boundaries answers administrative-area membership for a coordinate. The zero
// value is unusable; call LoadBoundaries.
type Boundaries struct {
	version string
	areas   []namedPolygons
}

type namedPolygons struct {
	name     string
	polygons []polygon
}

// Version returns the boundary release identifier.
func (b *Boundaries) Version() string {
	if b == nil {
		return ""
	}
	return b.version
}

// AreaCount returns how many named administrative areas are loaded.
func (b *Boundaries) AreaCount() int {
	if b == nil {
		return 0
	}
	return len(b.areas)
}

// BoroughAt returns the borough containing the coordinate, or "" when the point
// falls outside every loaded area (for example New Jersey or Long Island).
func (b *Boundaries) BoroughAt(lat, lon float64) string {
	if b == nil {
		return ""
	}
	for _, area := range b.areas {
		for _, poly := range area.polygons {
			if poly.contains(lat, lon) {
				return area.name
			}
		}
	}
	return ""
}

// boroughSlug maps the publisher's display name onto the stored label. The
// stored form has always been snake_case and lives in the ix_borough index, so
// it is kept stable rather than renamed.
func boroughSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = strings.Join(strings.Fields(slug), "_")
	slug = strings.NewReplacer(" ", "_", "-", "_", "'", "").Replace(slug)
	return slug
}

// LoadBoundaries reads the borough geometry and verifies its checksum.
func LoadBoundaries(path string) (*Boundaries, error) {
	return loadBoundaries(path, DefaultBoundarySHA256, DefaultBoundaryVersion)
}

func loadBoundaries(path, wantSHA, version string) (*Boundaries, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "boundary file path is empty")
	}
	path = ResolvePath(path)
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(errs.CodeNotFound,
			"read boundary file "+path+"; see data/boundaries/README.md for how to fetch it", err)
	}
	if wantSHA != "" {
		// The publisher intermittently answers with a short JSON error object
		// instead of the dataset. Distinguishing that from a genuine content
		// change saves the reader from chasing a checksum mismatch that is
		// really a failed download.
		if message := providerErrorMessage(payload); message != "" {
			return nil, errs.Newf(errs.CodeInvalidArgument,
				"boundary file %s: the download returned %q; retry the fetch (see data/boundaries/README.md)",
				path, message)
		}
		sum := sha256.Sum256(payload)
		if got := hex.EncodeToString(sum[:]); got != wantSHA {
			return nil, errs.Newf(errs.CodeInvalidArgument,
				"boundary file %s: sha256 %s does not match the pinned %s; the boundary release changed, so the labels must be reviewed before the checksum is updated",
				path, got, wantSHA)
		}
	}

	var records []struct {
		BoroName string `json:"boroname"`
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"the_geom"`
	}
	if err := json.Unmarshal(payload, &records); err != nil {
		return nil, errs.Wrap(errs.CodeInvalidArgument, "parse boundary file "+path, err)
	}
	if len(records) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "boundary file "+path+" has no features")
	}

	out := &Boundaries{version: version}
	for _, record := range records {
		name := boroughSlug(record.BoroName)
		if name == "" {
			return nil, errs.Newf(errs.CodeInvalidArgument,
				"boundary file %s: a feature has no boroname", path)
		}
		polys, err := decodeGeometry(record.Geometry.Type, record.Geometry.Coordinates)
		if err != nil {
			return nil, errs.Wrap(errs.CodeInvalidArgument,
				"boundary file "+path+": borough "+record.BoroName, err)
		}
		if len(polys) == 0 {
			return nil, errs.Newf(errs.CodeInvalidArgument,
				"boundary file %s: borough %s decoded to no polygon", path, record.BoroName)
		}
		out.areas = append(out.areas, namedPolygons{name: name, polygons: polys})
	}
	return out, nil
}

// providerErrorMessage recognises the Socrata error envelope, so a failed
// download is reported as such instead of as a corrupt file.
func providerErrorMessage(payload []byte) string {
	if len(payload) > 4096 || payload[0] != '{' {
		return ""
	}
	var envelope struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	if envelope.Message == "" {
		return ""
	}
	if envelope.ErrorCode != "" {
		return envelope.Message + " (" + envelope.ErrorCode + ")"
	}
	return envelope.Message
}

// decodeGeometry reads Polygon or MultiPolygon coordinates into polygons.
func decodeGeometry(geomType string, coordinates json.RawMessage) ([]polygon, error) {
	switch geomType {
	case "Polygon":
		rings, err := decodeRings(coordinates)
		if err != nil {
			return nil, err
		}
		return assemblePolygons(rings)
	case "MultiPolygon":
		var members []json.RawMessage
		if err := json.Unmarshal(coordinates, &members); err != nil {
			return nil, fmt.Errorf("decode MultiPolygon: %w", err)
		}
		var polys []polygon
		for _, member := range members {
			rings, err := decodeRings(member)
			if err != nil {
				return nil, err
			}
			assembled, err := assemblePolygons(rings)
			if err != nil {
				return nil, err
			}
			polys = append(polys, assembled...)
		}
		return polys, nil
	default:
		return nil, fmt.Errorf("unsupported geometry type %q", geomType)
	}
}

func decodeRings(raw json.RawMessage) ([]ring, error) {
	var coords [][][]float64
	if err := json.Unmarshal(raw, &coords); err != nil {
		return nil, fmt.Errorf("decode rings: %w", err)
	}
	rings := make([]ring, 0, len(coords))
	for _, positions := range coords {
		r := make(ring, 0, len(positions)+1)
		for _, pair := range positions {
			if len(pair) < 2 {
				return nil, fmt.Errorf("a position has %d ordinates, want at least 2", len(pair))
			}
			lon, lat := pair[0], pair[1]
			if math.IsNaN(lon) || math.IsNaN(lat) || math.IsInf(lon, 0) || math.IsInf(lat, 0) {
				return nil, fmt.Errorf("position %v is not finite", pair)
			}
			r = append(r, position{Lon: lon, Lat: lat})
		}
		r = closeRing(r)
		if len(r) < 4 {
			continue // a degenerate ring cannot bound area
		}
		rings = append(rings, r)
	}
	return rings, nil
}

// closeRing appends the first position when the ring is not already closed. The
// crossing-number test walks the ring cyclically either way, but closing it
// keeps the data in the shape the file claims to be.
func closeRing(r ring) ring {
	if len(r) == 0 {
		return r
	}
	first, last := r[0], r[len(r)-1]
	if first != last {
		r = append(r, first)
	}
	return r
}

// assemblePolygons treats the first ring of each set as the exterior and the
// rest as holes. The source data is a borough outline with no holes, but a
// boundary file that later gains them must not start labelling the hole.
func assemblePolygons(rings []ring) ([]polygon, error) {
	if len(rings) == 0 {
		return nil, nil
	}
	exterior := rings[0]
	poly := polygon{exterior: exterior}
	poly.minLat, poly.minLon = math.Inf(1), math.Inf(1)
	poly.maxLat, poly.maxLon = math.Inf(-1), math.Inf(-1)
	for _, p := range exterior {
		poly.minLat = math.Min(poly.minLat, p.Lat)
		poly.maxLat = math.Max(poly.maxLat, p.Lat)
		poly.minLon = math.Min(poly.minLon, p.Lon)
		poly.maxLon = math.Max(poly.maxLon, p.Lon)
	}
	poly.holes = rings[1:]
	return []polygon{poly}, nil
}
