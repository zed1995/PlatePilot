package knowledge

import (
	"fmt"
	"strings"
	"time"

	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/restaurant"
)

// attributeGroupLabels names each curated attribute bucket in the order they
// are rendered. A fixed order keeps the document hash stable: sorting by the
// label text instead would reorder the text whenever a label is edited.
var attributeGroupLabels = []struct {
	key   string
	label string
}{
	{"atmosphere", "氛围"},
	{"popular_for", "适合场合"},
	{"service_options", "服务"},
	{"accessibility", "无障碍"},
}

// BuildAttributes renders the restaurant_attributes evidence document.
//
// Only the curated tag lists are used. AttributesRaw is the untouched source
// MISC object, full of spelling variants and values like "False:No"; rendering
// it would embed that noise into the vector and make every restaurant with a
// dirty MISC object look alike.
//
// It returns ok=false when the restaurant has no attributes, because an empty
// document would be pure vector noise and the table forbids an active document
// without a vector anyway.
func BuildAttributes(r restaurant.Restaurant, opts ProfileOptions) (evidence.KnowledgeDocument, bool, error) {
	tags := collectAttributeTags(r.Attributes)
	if len(tags) == 0 {
		return evidence.KnowledgeDocument{}, false, nil
	}

	var lines []string
	for _, group := range attributeGroupLabels {
		values := groupTags(r.Attributes, group.key)
		if len(values) == 0 {
			continue
		}
		lines = append(lines, group.label+"："+strings.Join(values, "、"))
	}

	content := fmt.Sprintf("%s的服务与设施\n%s",
		r.Name, strings.Join(lines, "\n"))

	return newEvidenceDocument(r, evidence.DocTypeRestaurantAttributes, content,
		map[string]any{
			"attribute_groups": len(lines),
			"attribute_count":  len(tags),
		}, opts), true, nil
}

// BuildHours renders the restaurant_hours evidence document.
//
// It returns ok=false when the restaurant has no opening hours: stating "no
// hours recorded" would be a fact about the dataset, not about the restaurant.
func BuildHours(r restaurant.Restaurant, opts ProfileOptions) (evidence.KnowledgeDocument, bool, error) {
	if len(r.Hours) == 0 {
		return evidence.KnowledgeDocument{}, false, nil
	}
	hours := renderHours(r.Hours)
	if hours == "" {
		return evidence.KnowledgeDocument{}, false, nil
	}

	content := fmt.Sprintf("%s的营业时间\n%s", r.Name, hours)
	return newEvidenceDocument(r, evidence.DocTypeRestaurantHours, content,
		map[string]any{"hours_entries": len(r.Hours)}, opts), true, nil
}

// collectAttributeTags flattens every curated tag list.
func collectAttributeTags(a restaurant.Attributes) []string {
	var out []string
	for _, group := range attributeGroupLabels {
		out = append(out, groupTags(a, group.key)...)
	}
	return out
}

// groupTags returns one bucket's tags, deduplicated and in source order.
func groupTags(a restaurant.Attributes, key string) []string {
	var values []string
	switch key {
	case "atmosphere":
		values = a.AtmosphereTags
	case "popular_for":
		values = a.PopularForTags
	case "service_options":
		values = a.ServiceOptionTags
	case "accessibility":
		values = a.AccessibilityTags
	}
	return dedupeStrings(values)
}

// newEvidenceDocument assembles the shared fields of an evidence-scope
// document.
func newEvidenceDocument(
	r restaurant.Restaurant,
	docType evidence.DocType,
	content string,
	extraMetadata map[string]any,
	opts ProfileOptions,
) evidence.KnowledgeDocument {
	source := opts.Source
	if source == "" {
		source = restaurant.SourceGoogleLocal2021
	}
	generatedAt := opts.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = r.UpdatedAt
	}

	metadata := map[string]any{
		"source":           source,
		"curation_version": opts.CurationVersion,
		"generated_at":     generatedAt.UTC().Format(time.RFC3339),
		"snapshot_status":  string(r.SnapshotStatus),
	}
	for key, value := range extraMetadata {
		metadata[key] = value
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		metadata["borough"] = borough
	}

	doc := evidence.KnowledgeDocument{
		RestaurantID: r.ID,
		Scope:        evidence.ScopeEvidence,
		DocType:      docType,
		Title:        r.Name,
		Content:      content,
		ContentHash:  ContentHash(evidence.ScopeEvidence, docType, r.ID, content),
		Metadata:     metadata,
		SnapshotAt:   r.ObservedAt,
		IsActive:     false,
		Version:      1,
	}
	if r.SourceRecordID != "" {
		doc.SourceRecordIDs = []string{r.SourceRecordID}
	}
	return doc
}
