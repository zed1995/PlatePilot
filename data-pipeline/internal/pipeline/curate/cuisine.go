// Package curate turns raw Google Local records into curated domain DTOs.
//
// Every function here is a pure function: no I/O, no database, no clock except
// values passed in. That keeps the transformation rules unit-testable and
// reproducible.
package curate

import (
	"strings"
	"unicode"
)

// foodPlaceMarkers are category substrings that make a place food-relevant.
// A place must match at least one to enter the knowledge base.
var foodPlaceMarkers = []string{
	"restaurant", "cafe", "café", "coffee", "bakery", "bakeries", "bar", "bars", "pubs", "diner",
	"pizzeria", "pizza", "bistro", "eatery", "deli", "delicatessen", "pub",
	"brewery", "brewing", "ice cream", "dessert", "tea house", "juice",
	"sandwich", "burger", "hamburger", "sushi", "ramen", "noodle", "taco",
	"grill", "barbecue", "bbq", "buffet", "cafeteria", "donut",
	"doughnut", "bagel", "crepe", "waffle", "steakhouse", "seafood", "breakfast",
	"brunch", "food court", "food truck", "dim sum", "dumpling", "falafel",
	"shawarma", "kebab", "poke", "bubble tea", "smoothie", "confectionery",
}

// IsFoodPlace reports whether any category marks the place as food-relevant.
//
// Matching is on whole words and phrases, not substrings: a naive Contains check
// accepts "Barber shop" (via "bar"), "Public library" (via "pub"), and
// "Delivery service" (via "deli"), which is how ~6.6k non-food places slipped
// into a first full-corpus run.
func IsFoodPlace(categories []string) bool {
	for _, category := range categories {
		normalized := normalizedWords(category)
		if normalized == "" {
			continue
		}
		for _, marker := range foodPlaceMarkers {
			if strings.Contains(normalized, " "+marker+" ") {
				return true
			}
		}
	}
	return false
}

// normalizedWords lowercases a category and reduces it to space-separated
// alphanumeric tokens, padded with a space at each end so callers can test
// membership with " marker " and get whole-word semantics.
func normalizedWords(value string) string {
	var b strings.Builder
	b.WriteByte(' ')
	lastWasSpace := true
	for _, r := range strings.ToLower(value) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			lastWasSpace = false
		case !lastWasSpace:
			b.WriteByte(' ')
			lastWasSpace = true
		}
	}
	if !lastWasSpace {
		b.WriteByte(' ')
	}
	return b.String()
}

// cuisineByCategory maps exact Google category names (lowercased) to cuisine
// tags. Categories not listed fall back to a generic derivation.
var cuisineByCategory = map[string][]string{
	"pizza restaurant":          {"pizza", "italian"},
	"italian restaurant":        {"italian"},
	"pizzeria":                  {"pizza", "italian"},
	"sushi restaurant":          {"sushi", "japanese"},
	"japanese restaurant":       {"japanese"},
	"ramen restaurant":          {"ramen", "japanese"},
	"chinese restaurant":        {"chinese"},
	"dim sum restaurant":        {"dim_sum", "chinese"},
	"dumpling restaurant":       {"dumpling", "chinese"},
	"mexican restaurant":        {"mexican"},
	"tex-mex restaurant":        {"mexican", "tex_mex"},
	"thai restaurant":           {"thai"},
	"indian restaurant":         {"indian"},
	"korean restaurant":         {"korean"},
	"vietnamese restaurant":     {"vietnamese"},
	"french restaurant":         {"french"},
	"greek restaurant":          {"greek"},
	"spanish restaurant":        {"spanish"},
	"mediterranean restaurant":  {"mediterranean"},
	"middle eastern restaurant": {"middle_eastern"},
	"lebanese restaurant":       {"lebanese", "middle_eastern"},
	"turkish restaurant":        {"turkish"},
	"american restaurant":       {"american"},
	"diner":                     {"american", "diner"},
	"fast food restaurant":      {"fast_food"},
	"seafood restaurant":        {"seafood"},
	"hamburger restaurant":      {"burger", "american"},
	"sandwich shop":             {"sandwich"},
	"breakfast restaurant":      {"breakfast"},
	"brunch restaurant":         {"brunch"},
	"barbecue restaurant":       {"bbq"},
	"steak house":               {"steakhouse"},
	"vegetarian restaurant":     {"vegetarian"},
	"vegan restaurant":          {"vegan"},
	"cafe":                      {"cafe"},
	"coffee shop":               {"coffee", "cafe"},
	"bakery":                    {"bakery"},
	"bar":                       {"bar"},
	"pub":                       {"pub", "bar"},
	"ice cream shop":            {"ice_cream"},
	"dessert shop":              {"dessert"},
	"juice shop":                {"juice"},
	"smoothie shop":             {"smoothie"},
	"bubble tea store":          {"bubble_tea"},
	"poke bar":                  {"poke"},
	"falafel restaurant":        {"falafel", "middle_eastern"},
}

// genericCuisinePrefixes are words too vague to become a cuisine tag on their own.
var genericCuisinePrefixes = map[string]bool{
	"restaurant": true, "food": true, "meal": true, "eat": true, "place": true,
}

// CuisineTags derives cuisine tags from categories, preserving first-seen order.
func CuisineTags(categories []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(categories))
	add := func(tag string) {
		if tag == "" || seen[tag] {
			return
		}
		seen[tag] = true
		out = append(out, tag)
	}
	for _, category := range categories {
		lower := strings.ToLower(strings.TrimSpace(category))
		if tags, ok := cuisineByCategory[lower]; ok {
			for _, tag := range tags {
				add(tag)
			}
			continue
		}
		if prefix, ok := strings.CutSuffix(lower, " restaurant"); ok {
			if tag := slug(prefix); tag != "" && !genericCuisinePrefixes[tag] {
				add(tag)
			}
		}
	}
	return out
}

func slug(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case r == ' ' || r == '-' || r == '&' || r == '/':
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(strings.TrimSpace(b.String()), "_")
}
