package knowledge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
)

// maxDescriptionRunes bounds the description inside a profile document.
//
// The truncation happens here and nowhere else: restaurants.description is
// source-derived master data and must keep whatever the dataset provided.
const maxDescriptionRunes = 1000

// snapshotNote is the fixed wording that marks the document as a 2021 snapshot.
//
// It is a constant rather than a formatted string so that every profile
// contains the same phrase: a varying phrasing would cluster on the phrasing
// itself instead of on the restaurant.
const snapshotNote = "数据来源：Google Local（2021 年快照），非实时信息。"

// unknownDescription is the fixed placeholder for a missing description.
const unknownDescription = "暂无官方描述。"

// ProfileOptions are the generation-time values that must not influence the
// document's identity.
type ProfileOptions struct {
	// GeneratedAt is when the document was produced. It is recorded in
	// metadata only: it must never appear in content, or every re-run would
	// produce a different hash and churn a new version.
	GeneratedAt time.Time
	// CurationVersion identifies the rules that shaped the text.
	CurationVersion string
	// Source is the content source label, e.g. restaurant.SourceGoogleLocal2021.
	Source string
}

// BuildProfile renders the restaurant-level profile document.
//
// It is a pure function of its inputs: the same Restaurant and the same options
// always yield byte-identical content. That property is what makes the version
// bump a no-op on an unchanged re-run, and it is why GeneratedAt is excluded
// from the content.
//
// Only restaurants selected for the demo set get a profile. Review opinions are
// deliberately absent: they belong to evidence-scope documents, and letting
// them into a fact document would let one review rewrite a restaurant's profile.
func BuildProfile(r restaurant.Restaurant, opts ProfileOptions) (evidence.KnowledgeDocument, error) {
	if r.ID <= 0 {
		return evidence.KnowledgeDocument{}, fmt.Errorf("build profile: restaurant id must be positive, got %d", r.ID)
	}
	if strings.TrimSpace(r.Name) == "" {
		return evidence.KnowledgeDocument{}, fmt.Errorf("build profile: restaurant %d has no name", r.ID)
	}

	title := profileTitle(r)
	content := renderProfileBody(r)

	source := opts.Source
	if source == "" {
		source = restaurant.SourceGoogleLocal2021
	}
	generatedAt := opts.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = r.UpdatedAt
	}

	metadata := map[string]any{
		"source":             source,
		"curation_version":   opts.CurationVersion,
		"generated_at":       generatedAt.UTC().Format(time.RFC3339),
		"snapshot_status":    string(r.SnapshotStatus),
		"has_description":    strings.TrimSpace(r.Description) != "",
		"has_hours":          len(r.Hours) > 0,
		"rating_basis":       ratingBasisNote,
		"price_level":        priceLevelValue(r),
		"computed_avg":       computedAvgValue(r),
		"computed_avg_count": r.Rating.RatingCountForComputedAvg,
	}

	// Borough stays empty when unknown. The column is the predicate of the
	// borough partial indexes, so writing "" or "unknown" would silently make
	// those indexes unusable for the majority of the corpus.
	borough := strings.TrimSpace(r.BoroughGuess)

	doc := evidence.KnowledgeDocument{
		RestaurantID: r.ID,
		Scope:        evidence.ScopeRestaurant,
		DocType:      evidence.DocTypeRestaurantProfile,
		Title:        title,
		Content:      content,
		ContentHash:  ContentHash(evidence.ScopeRestaurant, evidence.DocTypeRestaurantProfile, r.ID, content),
		Metadata:     metadata,
		SnapshotAt:   r.ObservedAt,
		IsActive:     false, // M2-05 activates it once the vector is in place.
		Version:      1,
	}

	var sourceIDs []string
	if r.SourceRecordID != "" {
		sourceIDs = []string{r.SourceRecordID}
	}
	doc.SourceRecordIDs = sourceIDs

	if borough != "" {
		doc.Metadata["borough"] = borough
	}
	return doc, nil
}

// ratingBasisNote records which rating the document quotes and why, so a reader
// in M3 never presents the source average as if it were measured.
const ratingBasisNote = "评分为入库评论样本均值；来源站评分可能截顶，两者含义不同。"

// profileTitle is the one-line headline: name, cuisine, price, rating.
func profileTitle(r restaurant.Restaurant) string {
	var parts []string
	if cuisine := primaryCuisine(r); cuisine != "" {
		parts = append(parts, cuisine)
	}
	if level := priceLevelValue(r); level != "" {
		parts = append(parts, level)
	}
	if avg := computedAvgValue(r); avg != "" {
		parts = append(parts, avg+"星")
	}
	if len(parts) == 0 {
		return r.Name
	}
	return fmt.Sprintf("%s（%s）", r.Name, strings.Join(parts, "，"))
}

// renderProfileBody assembles the fact lines in a fixed order.
//
// Unknown fields are stated once with fixed wording rather than left blank. A
// blank line in a vector is noise; a constant phrase is at least consistent,
// and varying the wording per restaurant would let the phrasing dominate the
// embedding.
func renderProfileBody(r restaurant.Restaurant) string {
	lines := []string{profileTitle(r)}

	lines = append(lines, "地址："+addressLine(r))

	description := truncateRunes(strings.TrimSpace(r.Description), maxDescriptionRunes)
	if description == "" {
		description = unknownDescription
	}
	lines = append(lines, "简介："+description)

	if hours := renderHours(r.Hours); hours != "" {
		lines = append(lines, "营业时间："+hours)
	}

	if features := renderFeatures(r); features != "" {
		lines = append(lines, "特色："+features)
	}

	if overview := renderReviewOverview(r); overview != "" {
		lines = append(lines, "评论概况："+overview)
	}

	lines = append(lines, statusLine(r))
	lines = append(lines, snapshotNote)

	return strings.Join(lines, "\n")
}

// addressLine is the address plus the borough when one is known.
func addressLine(r restaurant.Restaurant) string {
	address := strings.TrimSpace(r.Address)
	if address == "" {
		address = "地址未知"
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		return address + "（" + borough + "）"
	}
	return address
}

// renderHours summarises the opening matrix as weekday groups.
//
// The raw source text is preferred over the computed minutes: the raw string is
// what a reader would recognise ("11AM–10PM"), and reconstructing it from
// minutes risks a formatting difference that changes the hash without changing
// the meaning.
func renderHours(hours []restaurant.HoursEntry) string {
	if len(hours) == 0 {
		return ""
	}
	byWeekday := make(map[int][]restaurant.HoursEntry, 7)
	for _, entry := range hours {
		byWeekday[entry.Weekday] = append(byWeekday[entry.Weekday], entry)
	}

	weekdayNames := [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	groups := make([]string, 0, 7)
	for weekday := 0; weekday < 7; weekday++ {
		entries := byWeekday[weekday]
		if len(entries) == 0 {
			continue
		}
		// Entries are sorted so the rendered text does not depend on the order
		// the source happened to store them in.
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].OpenMinute < entries[j].OpenMinute })

		intervals := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsClosed {
				intervals = append(intervals, "休息")
				continue
			}
			if raw := strings.TrimSpace(entry.Raw); raw != "" {
				intervals = append(intervals, raw)
				continue
			}
			intervals = append(intervals, fmt.Sprintf("%s-%s", minuteLabel(entry.OpenMinute), minuteLabel(entry.CloseMinute)))
		}
		label := weekdayNames[weekday]
		groups = append(groups, label+" "+strings.Join(intervals, "、"))
	}
	if len(groups) == 0 {
		return ""
	}
	return strings.Join(groups, "；")
}

// minuteLabel formats minutes-after-midnight as a wall-clock time. Close
// minutes may exceed 1440 for intervals that run past midnight.
func minuteLabel(minute int) string {
	day := minute / 1440
	rest := minute % 1440
	if rest < 0 {
		rest += 1440
		day--
	}
	hour := rest / 60
	minuteOfHour := rest % 60
	if minuteOfHour == 0 {
		return fmt.Sprintf("%d:00", hour)
	}
	return fmt.Sprintf("%d:%02d", hour, minuteOfHour)
}

// renderFeatures lists cuisine tags plus the most useful attribute groups.
//
// Only non-empty groups appear, so a restaurant with no accessibility data does
// not get an empty "无障碍" line.
func renderFeatures(r restaurant.Restaurant) string {
	var parts []string
	if len(r.CuisineTags) > 0 {
		parts = append(parts, strings.Join(dedupeStrings(r.CuisineTags), "、"))
	}
	groups := []struct {
		label string
		tags  []string
	}{
		{"氛围", r.Attributes.AtmosphereTags},
		{"适合", r.Attributes.PopularForTags},
		{"服务", r.Attributes.ServiceOptionTags},
		{"无障碍", r.Attributes.AccessibilityTags},
	}
	for _, group := range groups {
		if tags := dedupeStrings(group.tags); len(tags) > 0 {
			parts = append(parts, group.label+"："+strings.Join(tags, "、"))
		}
	}
	return strings.Join(parts, "；")
}

// renderReviewOverview states the sample size next to the average.
//
// Without the count, "4.5星" reads as a precise fact about the restaurant;
// with it, it reads as what it is - an average over the reviews that were
// sampled into the knowledge base.
func renderReviewOverview(r restaurant.Restaurant) string {
	parts := make([]string, 0, 3)
	if avg := r.Rating.ComputedAvg; avg != nil {
		parts = append(parts, fmt.Sprintf("入库评论样本均分 %.1f", *avg))
	}
	if n := r.Rating.RatingCountForComputedAvg; n > 0 {
		parts = append(parts, fmt.Sprintf("样本量 %d 条", n))
	}
	if r.ReviewStats.TextReviewCount > 0 {
		parts = append(parts, fmt.Sprintf("含文字评论 %d 条", r.ReviewStats.TextReviewCount))
	}
	if sourceAvg := r.Rating.SourceAvg; sourceAvg != nil {
		note := fmt.Sprintf("来源站评分 %.1f", *sourceAvg)
		if r.ReviewStats.SourceReviewCountCapped {
			note += "（已截顶）"
		}
		parts = append(parts, note)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "，")
}

// statusLine explains the snapshot status in words, so a closed place is not
// silently presented as open.
func statusLine(r restaurant.Restaurant) string {
	switch r.SnapshotStatus {
	case restaurant.StatusOpen:
		return "快照状态：2021 年时该店在营业。"
	case restaurant.StatusClosed:
		return "快照状态：2021 年时该店已停业，但仍保留其历史信息。"
	case restaurant.StatusPermanentlyClosed:
		return "快照状态：2021 年时该店已永久停业。"
	default:
		return "快照状态：2021 年时未知。"
	}
}

// primaryCuisine picks the single most representative cuisine label.
func primaryCuisine(r restaurant.Restaurant) string {
	if len(r.CuisineTags) == 0 {
		return ""
	}
	return strings.TrimSpace(r.CuisineTags[0])
}

// priceLevelValue renders the price level, distinguishing "missing" from
// "free".
func priceLevelValue(r restaurant.Restaurant) string {
	if r.Price.Level != nil {
		return strings.Repeat("$", *r.Price.Level)
	}
	if raw := strings.TrimSpace(r.Price.Raw); raw != "" {
		return raw
	}
	return ""
}

// computedAvgValue renders the computed average, or empty when unknown.
func computedAvgValue(r restaurant.Restaurant) string {
	if r.Rating.ComputedAvg == nil {
		return ""
	}
	return fmt.Sprintf("%.1f", *r.Rating.ComputedAvg)
}

// truncateRunes cuts s to at most limit runes, appending an ellipsis when it
// actually truncated. Counting runes rather than bytes keeps a multi-byte
// character from being cut in half.
func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// dedupeStrings removes empties and repeats while preserving order, so the
// rendered text does not depend on a tag list happening to contain a duplicate.
func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
