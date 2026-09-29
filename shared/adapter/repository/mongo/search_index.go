package mongo

import "encoding/json"

// SearchIndexName is the Atlas Search index on the restaurants collection.
const SearchIndexName = "restaurants_search_index"

// searchIndexJSON is the source of truth for the restaurants Atlas Search
// index. scripts/atlas/search_index_restaurants.json must stay byte-identical;
// a test enforces that so the committed definition cannot drift.
const searchIndexJSON = `{
  "name": "restaurants_search_index",
  "analyzer": "lucene.standard",
  "mappings": {
    "dynamic": false,
    "fields": {
      "name": [
        { "type": "autocomplete", "tokenization": "edgeGram" },
        { "type": "string", "analyzer": "lucene.standard" }
      ],
      "address": { "type": "string", "analyzer": "lucene.standard" },
      "categories": { "type": "string" },
      "cuisine_tags": { "type": "string" },
      "description": { "type": "string", "analyzer": "lucene.standard" },
      "borough_guess": { "type": "string" }
    }
  }
}
`

// SearchIndexDefinition returns the Atlas Search index definition as JSON.
func SearchIndexDefinition() []byte {
	return []byte(searchIndexJSON)
}

// ParseSearchIndexDefinition validates the definition and returns its name.
func ParseSearchIndexDefinition() (string, error) {
	var doc struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(searchIndexJSON), &doc); err != nil {
		return "", err
	}
	return doc.Name, nil
}
