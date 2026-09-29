package curate

import (
	"sort"
	"strings"

	"github.com/zed/platepilot/shared/domain/restaurant"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
)

// canonicalTriStateKeys are always present in the curated document. Absent
// labels leave them "unknown", which is never the same as "false".
var canonicalTriStateKeys = []string{
	"accepts_reservations",
	"wheelchair_accessible",
	"outdoor_seating",
	"takeout",
	"delivery",
	"dine_in",
	"good_for_kids",
	"good_for_groups",
}

// attributeKeyByLabel maps a lowercased MISC label to a canonical key.
var attributeKeyByLabel = map[string]string{
	"accepts reservations":              "accepts_reservations",
	"reservations required":             "accepts_reservations",
	"wheelchair accessible entrance":    "wheelchair_accessible",
	"wheelchair accessible restroom":    "wheelchair_accessible",
	"wheelchair accessible parking lot": "wheelchair_accessible",
	"outdoor seating":                   "outdoor_seating",
	"takeout":                           "takeout",
	"take-out":                          "takeout",
	"delivery":                          "delivery",
	"no-contact delivery":               "delivery",
	"dine-in":                           "dine_in",
	"dine in":                           "dine_in",
	"good for kids":                     "good_for_kids",
	"good for groups":                   "good_for_groups",
	"family-friendly":                   "good_for_kids",
}

// topicTagBuckets routes a MISC topic's labels into the matching tag list.
var topicTagBuckets = map[string]func(*restaurant.Attributes, string){
	"atmosphere":  func(a *restaurant.Attributes, tag string) { a.AtmosphereTags = appendUnique(a.AtmosphereTags, tag) },
	"highlights":  func(a *restaurant.Attributes, tag string) { a.AtmosphereTags = appendUnique(a.AtmosphereTags, tag) },
	"popular for": func(a *restaurant.Attributes, tag string) { a.PopularForTags = appendUnique(a.PopularForTags, tag) },
	"crowd":       func(a *restaurant.Attributes, tag string) { a.PopularForTags = appendUnique(a.PopularForTags, tag) },
	"accessibility": func(a *restaurant.Attributes, tag string) {
		a.AccessibilityTags = appendUnique(a.AccessibilityTags, tag)
	},
	"service options": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"offerings": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"dining options": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"amenities": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"payments": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"planning": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"health & safety": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
	"from the business": func(a *restaurant.Attributes, tag string) {
		a.ServiceOptionTags = appendUnique(a.ServiceOptionTags, tag)
	},
}

// NormalizeAttributes converts the raw MISC object into three-state flags plus
// derived tag lists. It always returns the canonical keys so downstream
// queries can distinguish "false" from "unknown".
func NormalizeAttributes(misc raw.MISC) restaurant.Attributes {
	attrs := restaurant.Attributes{TriStates: make(map[string]string, len(canonicalTriStateKeys))}
	for _, key := range canonicalTriStateKeys {
		attrs.TriStates[key] = restaurant.TriStateUnknown
	}
	if len(misc) == 0 {
		return attrs
	}

	topics := make([]string, 0, len(misc))
	for topic := range misc {
		topics = append(topics, topic)
	}
	sort.Strings(topics)

	for _, topic := range topics {
		topicKey := strings.ToLower(strings.TrimSpace(topic))
		bucket := topicTagBuckets[topicKey]
		for _, label := range misc[topic] {
			normalized := strings.ToLower(strings.TrimSpace(label))
			if normalized == "" {
				continue
			}
			switch {
			case attributeKeyByLabel[normalized] != "":
				attrs.TriStates[attributeKeyByLabel[normalized]] = restaurant.TriStateTrue
			default:
				if rest, ok := strings.CutPrefix(normalized, "no "); ok {
					if key := attributeKeyByLabel[rest]; key != "" {
						attrs.TriStates[key] = restaurant.TriStateFalse
					}
				}
			}
			if bucket != nil {
				bucket(&attrs, slug(normalized))
			}
		}
	}
	return attrs
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
