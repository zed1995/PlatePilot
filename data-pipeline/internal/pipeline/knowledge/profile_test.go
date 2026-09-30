package knowledge

import (
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/restaurant"
)

// fixtureTime is the snapshot instant used across the profile tests.
var fixtureTime = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }

// fullRestaurant has every field populated, so a golden assertion on it fails
// only when the rendering actually changes.
func fullRestaurant() restaurant.Restaurant {
	return restaurant.Restaurant{
		ID:             42,
		SourceRecordID: "gmap-abc",
		Name:           "Luca Pizza",
		Address:        "7 Carmine St, New York, NY",
		BoroughGuess:   "Manhattan",
		CuisineTags:    []string{"Pizza", "Italian"},
		Description:    "A neighbourhood pizza spot.",
		Price:          restaurant.Price{Raw: "$$", Level: intPtr(2)},
		Rating: restaurant.Rating{
			ComputedAvg:               float64Ptr(4.3),
			SourceAvg:                 float64Ptr(4.5),
			RatingCountForComputedAvg: 128,
		},
		ReviewStats: restaurant.ReviewStats{
			TextReviewCount:           120,
			SourceReviewCountCapped:   true,
			RepresentativeReviewCount: 12,
		},
		Attributes: restaurant.Attributes{
			AtmosphereTags: []string{"Casual", "Cozy"},
		},
		Hours: []restaurant.HoursEntry{
			{Weekday: 1, OpenMinute: 660, CloseMinute: 1320, Raw: "11AM–10PM"},
			{Weekday: 6, OpenMinute: 660, CloseMinute: 1380},
		},
		SnapshotStatus: restaurant.StatusOpen,
		ObservedAt:     fixtureTime,
	}
}

func defaultOptions() ProfileOptions {
	return ProfileOptions{CurationVersion: "v1", GeneratedAt: fixtureTime}
}

// TestBuildProfileGoldenText pins the rendered document. Changing any line here
// is a deliberate content decision: it changes every profile's hash and would
// churn a version across the corpus.
func TestBuildProfileGoldenText(t *testing.T) {
	doc, err := BuildProfile(fullRestaurant(), defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}

	const wantTitle = "Luca Pizza（Pizza，$$，4.3星）"
	if doc.Title != wantTitle {
		t.Errorf("title =\n%q\nwant\n%q", doc.Title, wantTitle)
	}

	const wantContent = `Luca Pizza（Pizza，$$，4.3星）
地址：7 Carmine St, New York, NY（Manhattan）
简介：A neighbourhood pizza spot.
营业时间：周一 11AM–10PM；周六 11:00-23:00
特色：Pizza、Italian；氛围：Casual、Cozy
评论概况：入库评论样本均分 4.3，样本量 128 条，含文字评论 120 条，来源站评分 4.5（已截顶）
快照状态：2021 年时该店在营业。
数据来源：Google Local（2021 年快照），非实时信息。`
	if doc.Content != wantContent {
		t.Errorf("content =\n%s\n\nwant\n%s", doc.Content, wantContent)
	}
}

// A profile must stay a fact document. If review opinions leak in, a single
// review could rewrite what the restaurant *is*.
func TestBuildProfileCarriesNoReviewOpinion(t *testing.T) {
	doc, err := BuildProfile(fullRestaurant(), defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if doc.Scope != evidence.ScopeRestaurant {
		t.Errorf("scope = %q, want %q", doc.Scope, evidence.ScopeRestaurant)
	}
	if doc.DocType != evidence.DocTypeRestaurantProfile {
		t.Errorf("doc_type = %q, want %q", doc.DocType, evidence.DocTypeRestaurantProfile)
	}
	for _, banned := range []string{"代表评论", "评论原文", "says", "review text"} {
		if strings.Contains(doc.Content, banned) {
			t.Errorf("profile contains review-opinion marker %q", banned)
		}
	}
}

func TestBuildProfileIsPure(t *testing.T) {
	opts := defaultOptions()
	first, err := BuildProfile(fullRestaurant(), opts)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	// A different generation time must not change the content, or every re-run
	// would produce a different hash and a new version.
	opts.GeneratedAt = opts.GeneratedAt.Add(72 * time.Hour)
	second, err := BuildProfile(fullRestaurant(), opts)
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if first.Content != second.Content {
		t.Error("GeneratedAt leaked into content")
	}
	if first.ContentHash != second.ContentHash {
		t.Errorf("hash changed with GeneratedAt:\n%s\n%s", first.ContentHash, second.ContentHash)
	}
	if first.Metadata["generated_at"] == second.Metadata["generated_at"] {
		t.Error("generated_at should still record when the document was produced")
	}
}

// SnapshotAt is "when these facts were true". Using the generation time would
// make 2021 data look freshly scraped, which is the credibility bug the
// timestamp exists to prevent.
func TestBuildProfileUsesSnapshotTimeNotGenerationTime(t *testing.T) {
	doc, err := BuildProfile(fullRestaurant(), ProfileOptions{
		CurationVersion: "v1",
		GeneratedAt:     time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !doc.SnapshotAt.Equal(fixtureTime) {
		t.Errorf("snapshot_at = %v, want %v", doc.SnapshotAt, fixtureTime)
	}
	if doc.SnapshotAt.Year() != 2021 {
		t.Errorf("snapshot_at year = %d, want 2021", doc.SnapshotAt.Year())
	}
}

// Documents start inactive: the table forbids an active document without a
// vector, so activation belongs to the version/embedding stage.
func TestBuildProfileStartsInactive(t *testing.T) {
	doc, err := BuildProfile(fullRestaurant(), defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if doc.IsActive {
		t.Error("profile must not be active before its vector is written")
	}
	if doc.Embedding != nil {
		t.Error("profile must not carry a vector")
	}
}

// A missing borough is a real state for most of the corpus. Writing "" or
// "unknown" would make the borough partial indexes permanently miss.
func TestBuildProfileLeavesUnknownBoroughEmpty(t *testing.T) {
	r := fullRestaurant()
	r.BoroughGuess = ""
	doc, err := BuildProfile(r, defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if _, ok := doc.Metadata["borough"]; ok {
		t.Error("metadata must not claim a borough that is unknown")
	}
	if strings.Contains(doc.Content, "unknown") || strings.Contains(doc.Content, "（)") {
		t.Errorf("content should not render an empty borough: %q", doc.Content)
	}
}

// Unknown fields get one fixed phrase rather than a blank line or a
// per-restaurant wording, which would cluster on the phrasing.
func TestBuildProfileStatesMissingFieldsWithFixedWording(t *testing.T) {
	r := restaurant.Restaurant{
		ID:             7,
		Name:           "Diner",
		SnapshotStatus: restaurant.StatusUnknown,
		ObservedAt:     fixtureTime,
	}
	doc, err := BuildProfile(r, defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !strings.Contains(doc.Content, unknownDescription) {
		t.Errorf("missing description should use the fixed phrase:\n%s", doc.Content)
	}
	if strings.Contains(doc.Content, "营业时间：") {
		t.Errorf("absent hours should be omitted entirely:\n%s", doc.Content)
	}
	if strings.Contains(doc.Content, "评论概况：") {
		t.Errorf("absent rating should be omitted entirely:\n%s", doc.Content)
	}
	if !strings.Contains(doc.Content, "快照状态：2021 年时未知。") {
		t.Errorf("unknown status should be stated:\n%s", doc.Content)
	}
}

func TestBuildProfileOmitsBlankLines(t *testing.T) {
	doc, err := BuildProfile(restaurant.Restaurant{
		ID: 9, Name: "Bare", ObservedAt: fixtureTime, SnapshotStatus: restaurant.StatusOpen,
	}, defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	for _, line := range strings.Split(doc.Content, "\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("content contains a blank line:\n%q", doc.Content)
		}
	}
}

func TestBuildProfileTruncatesLongDescription(t *testing.T) {
	r := fullRestaurant()
	r.Description = strings.Repeat("很", maxDescriptionRunes+50)
	doc, err := BuildProfile(r, defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if !strings.HasSuffix(strings.Split(doc.Content, "\n")[2], "…") {
		t.Errorf("long description should be truncated with an ellipsis:\n%s", doc.Content)
	}
	// The master record must be untouched: truncation is a document concern.
	if len([]rune(r.Description)) <= maxDescriptionRunes {
		t.Error("test fixture did not actually exceed the limit")
	}
}

func TestBuildProfileDeduplicatesTags(t *testing.T) {
	r := fullRestaurant()
	r.CuisineTags = []string{"Pizza", "Pizza", " Italian "}
	r.Attributes.AtmosphereTags = []string{"Casual", "Casual"}
	doc, err := BuildProfile(r, defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	// "Pizza" legitimately appears twice: once in the title and once in the
	// feature list. What must not happen is the duplicate pair "Pizza、Pizza"
	// reaching the text.
	if strings.Contains(doc.Content, "Pizza、Pizza") {
		t.Errorf("duplicate tag survived into the document:\n%s", doc.Content)
	}
	if !strings.Contains(doc.Content, "Pizza、Italian") {
		t.Errorf("tags should be trimmed and kept in order:\n%s", doc.Content)
	}
	if !strings.Contains(doc.Content, "Italian") {
		t.Errorf("tag should be trimmed and kept:\n%s", doc.Content)
	}
}

// Hours are rendered from whichever representation the source gave us, and the
// order must not depend on how the rows happened to be stored.
func TestRenderHoursIsOrderIndependent(t *testing.T) {
	entries := []restaurant.HoursEntry{
		{Weekday: 1, OpenMinute: 660, CloseMinute: 1320, Raw: "11AM–10PM"},
		{Weekday: 1, OpenMinute: 480, CloseMinute: 660, Raw: "8AM–11AM"},
	}
	reversed := []restaurant.HoursEntry{entries[1], entries[0]}
	if renderHours(entries) != renderHours(reversed) {
		t.Errorf("hour order leaked into the text:\n%s\n%s", renderHours(entries), renderHours(reversed))
	}
}

func TestMinuteLabelHandlesPastMidnight(t *testing.T) {
	cases := map[int]string{
		0:    "0:00",
		660:  "11:00",
		1380: "23:00",
		1440: "0:00",
		1620: "3:00",
	}
	for minute, want := range cases {
		if got := minuteLabel(minute); got != want {
			t.Errorf("minuteLabel(%d) = %q, want %q", minute, got, want)
		}
	}
}

func TestBuildProfileRejectsUnusableInput(t *testing.T) {
	cases := map[string]restaurant.Restaurant{
		"no id":   {Name: "X"},
		"no name": {ID: 1},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildProfile(r, defaultOptions()); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestBuildProfileRecordsSourceRecordID(t *testing.T) {
	doc, err := BuildProfile(fullRestaurant(), defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	if len(doc.SourceRecordIDs) != 1 || doc.SourceRecordIDs[0] != "gmap-abc" {
		t.Errorf("source_record_ids = %v, want [gmap-abc]", doc.SourceRecordIDs)
	}
}

func TestBuildProfileHashIsSet(t *testing.T) {
	doc, err := BuildProfile(fullRestaurant(), defaultOptions())
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	want := ContentHash(evidence.ScopeRestaurant, evidence.DocTypeRestaurantProfile, 42, doc.Content)
	if doc.ContentHash != want {
		t.Errorf("content_hash = %q, want %q", doc.ContentHash, want)
	}
}
