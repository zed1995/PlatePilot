package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

// RestaurantSearchRepository is the Postgres implementation of
// store.RestaurantRepository.
//
// It is a separate type from RestaurantStore rather than more methods on it.
// The store's queries are shaped for batch writes and idempotent re-runs; this
// one's are shaped for a single indexed page. Sharing the type would mean every
// read carried the write path's constraints, and vice versa.
type RestaurantSearchRepository struct {
	client *Client
}

// NewRestaurantSearchRepository builds a read-side repository on an existing pool.
func NewRestaurantSearchRepository(client *Client) *RestaurantSearchRepository {
	return &RestaurantSearchRepository{client: client}
}

var _ store.RestaurantRepository = (*RestaurantSearchRepository)(nil)

// restaurantSearchColumns is the projection a search needs.
//
// It is deliberately much lighter than the store's row: attributes, hours, and
// the raw source objects are the bulk of the table, and none of them appear in
// a result list. The one thing that looks optional here but is not is
// rating_computed_avg — the source average is capped at the number of reviews
// the provider sampled, so filtering on it would pass restaurants whose real
// rating is unknown.
//
// knowledge_score is selected because it is the pipeline's own notion of how
// much a restaurant can support, and it is the last tie-breaker available when
// every other signal ties.
//
// It is measured, not useful: the demo selection writes -log10(3000) = 4.48 for
// every row it keeps, so all 3,000 selected restaurants carry an identical
// score and the column cannot order anything. It stays because it is the
// pipeline's stated intent, and the prior uses rating instead — see priorOf.
const restaurantSearchColumns = `
	id, name, address, borough, cuisine_tags, price_level,
	rating_computed_avg, rating_count, source, snapshot_status,
	knowledge_score, is_active_for_demo, observed_at`

// GetByID returns one restaurant by its internal id.
func (r *RestaurantSearchRepository) GetByID(ctx context.Context, restaurantID int64) (search.RestaurantDetail, error) {
	if restaurantID <= 0 {
		return search.RestaurantDetail{}, errs.New(errs.CodeInvalidArgument, "restaurant_id must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	row := r.client.pool.QueryRow(ctx,
		`SELECT `+restaurantSearchColumns+` FROM restaurants WHERE id = $1`, restaurantID)
	detail, err := scanRestaurantDetail(row)
	if err != nil {
		if pgErrNoRows(err) {
			return search.RestaurantDetail{}, errs.Newf(errs.CodeNotFound, "restaurant %d not found", restaurantID)
		}
		return search.RestaurantDetail{}, err
	}
	return detail, nil
}

// Search applies every hard filter and returns candidates ordered by prior.
//
// Text is not interpreted here: it belongs to the keyword channel, and letting
// it influence this ordering would double-count it during fusion. The
// structured channel's score is therefore the candidate's prior, and Text is
// still carried on the query so a caller that runs both channels from one
// request keeps the text available.
func (r *RestaurantSearchRepository) Search(ctx context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error) {
	if err := query.Filter.Validate(); err != nil {
		return nil, err
	}
	statement, args, err := buildStructuredSearch(query.Filter, topK(query.TopK))
	if err != nil {
		return nil, err
	}
	if statement == "" {
		return nil, errs.New(errs.CodeRetrievalEmptyQuery,
			"a structured search needs at least one filter")
	}

	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, operationError("postgres: search restaurants", err)
	}
	return scanCandidates(rows)
}

// MatchByText finds restaurants whose name or address resembles text.
//
// Two details are load-bearing. First, similarity() is coalesced to zero: a
// restaurant with no address makes GREATEST return NULL, and NULL sorts last in
// both ASC and DESC, so the row would be pushed to the bottom of the results
// rather than ranked by its name. Second, the pattern is escaped, because a
// user searching "50%" would otherwise emit a pattern that matches every row.
func (r *RestaurantSearchRepository) MatchByText(ctx context.Context, text string, limit int) ([]search.RestaurantCandidate, error) {
	pattern, err := likePattern(text)
	if err != nil {
		return nil, err
	}
	statement := `
		SELECT ` + restaurantSearchColumns + `,
		       GREATEST(similarity(name, $1), COALESCE(similarity(address, $1), 0)) AS match_score
		FROM restaurants
		WHERE is_active_for_demo
		  AND (name ILIKE $2 ESCAPE '\' OR (address IS NOT NULL AND address ILIKE $2 ESCAPE '\'))
		ORDER BY match_score DESC, id ASC
		LIMIT ` + strconv.Itoa(topK(limit))
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, statement, text, pattern)
	if err != nil {
		return nil, operationError("postgres: match restaurants by text", err)
	}
	return scanMatchCandidates(rows)
}

// buildStructuredSearch renders the filter into a statement and its arguments.
//
// It returns an empty statement when the filter constrains nothing. That case
// is refused rather than served: a filterless structured search is a request to
// list the corpus in prior order, which is neither what the caller meant nor
// something a top-k page can make meaningful.
func buildStructuredSearch(filter search.RestaurantFilter, limit int) (string, []any, error) {
	if filter.IsEmpty() {
		return "", nil, nil
	}
	clauses := []string{"is_active_for_demo"}
	args := make([]any, 0, 6)

	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	if filter.Borough != "" {
		clauses = append(clauses, "borough = "+bind(search.CanonicalBorough(filter.Borough)))
	}
	if len(filter.Cuisines) > 0 {
		// EXISTS over unnest rather than an array operator: the operator's
		// overlap semantics are easy to read as "all" when they are "any", and
		// this form states which one it is.
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM unnest(cuisine_tags) AS c WHERE c = ANY("+bind(filter.Cuisines)+"))")
	}
	if len(filter.PriceLevels) > 0 {
		clauses = append(clauses, "price_level = ANY("+bind(filter.PriceLevels)+")")
	}
	if filter.MinRating != nil {
		clauses = append(clauses, "rating_computed_avg >= "+bind(*filter.MinRating))
	}
	if filter.Neighborhood != "" {
		clauses = append(clauses, "address ILIKE "+bind("%"+escapeLike(filter.Neighborhood)+"%")+" ESCAPE '\\'")
	}
	if filter.OpenNow != nil {
		// COALESCE names the third state explicitly. Without it, a missing
		// attribute is NULL, and `attributes->>'open_now' = 'false'` is NULL for
		// those rows rather than false, so they drop out of the result set
		// instead of being counted as closed.
		want := strconv.FormatBool(*filter.OpenNow)
		clauses = append(clauses,
			"COALESCE(attributes->>'open_now', 'unknown') = "+bind(want))
	}
	if filter.HasDistance() {
		// ST_DWithin is the form that can use the geography index; ST_Distance
		// can only filter after every row has been measured.
		clauses = append(clauses,
			"location IS NOT NULL AND ST_DWithin(location, "+
				bind(wktPoint(*filter.QueryOrigin))+"::geography, "+
				strconv.Itoa(*filter.MaxDistanceMeters)+")")
	}

	statement := `SELECT ` + restaurantSearchColumns + `
		FROM restaurants
		WHERE ` + strings.Join(clauses, "\n\t\t  AND ") + `
		ORDER BY knowledge_score DESC, id ASC
		LIMIT ` + strconv.Itoa(limit)
	return statement, args, nil
}

// wktPoint renders a coordinate as WKT in lon,lat order.
//
// geography takes its point in that order while the domain stores latitude
// first, so this is where the inversion happens. Getting it backwards does not
// error: it places the point somewhere in the Indian Ocean, where no restaurant
// is, and the search quietly returns nothing.
func wktPoint(point search.GeoPoint) string {
	return "SRID=4326;POINT(" +
		strconv.FormatFloat(point.Longitude, 'g', -1, 64) + " " +
		strconv.FormatFloat(point.Latitude, 'g', -1, 64) + ")"
}

// likePattern builds the ILIKE pattern for a fuzzy text match.
//
// Empty text and text too short to index are different failures and get
// different codes. "You sent nothing" and "what you sent cannot be matched" are
// different messages, and a client that retries one should not retry the other.
//
// Three characters is the floor because a trigram index cannot match anything
// shorter. Answering would mean scanning the corpus and returning whatever the
// limit cut out of it, so the request is refused instead — which also catches
// the common case of a truncated name.
func likePattern(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", errs.New(errs.CodeRetrievalEmptyQuery, "match text must not be empty")
	}
	if len([]rune(trimmed)) < minMatchText {
		return "", errs.Newf(errs.CodeRetrievalQueryTooShort,
			"match text must be at least %d characters (got %d)", minMatchText, len([]rune(trimmed)))
	}
	return "%" + escapeLike(trimmed) + "%", nil
}

// minMatchText is the shortest text a trigram-backed match accepts.
const minMatchText = 3

// escapeLike neutralises the wildcard characters in a user-supplied fragment so
// a literal percent is matched as a percent.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// topK clamps a requested page size into the supported range.
func topK(requested int) int {
	if requested <= 0 {
		return defaultSearchTopK
	}
	if requested > maxSearchTopK {
		return maxSearchTopK
	}
	return requested
}

const (
	// defaultSearchTopK is the page size when the caller does not set one.
	defaultSearchTopK = 10
	// maxSearchTopK caps a page so one request cannot pull the corpus.
	maxSearchTopK = 100
)

// int16PtrToInt converts a nullable smallint column to the domain's int.
func int16PtrToInt(v *int16) *int {
	if v == nil {
		return nil
	}
	out := int(*v)
	return &out
}

// scanner is satisfied by both QueryRow and Rows, so the projection has one
// decode path instead of two that can drift.
type scanner interface {
	Scan(dest ...any) error
}

// scanRestaurantDetail decodes one projected row.
func scanRestaurantDetail(row scanner) (search.RestaurantDetail, error) {
	var (
		detail     search.RestaurantDetail
		address    *string
		borough    *string
		priceLevel *int16
		rating     *float64
		snapshotAt = &detail.SnapshotAt
		status     string
	)
	if err := row.Scan(
		&detail.RestaurantID, &detail.Name, &address, &borough, &detail.Cuisines,
		&priceLevel, &rating, &detail.RatingCount, &detail.Source, &status,
		&detail.KnowledgeScore, &detail.IsActiveForDemo, snapshotAt,
	); err != nil {
		return search.RestaurantDetail{}, operationError("postgres: scan restaurant", err)
	}
	if address != nil {
		detail.Address = *address
	}
	if borough != nil {
		detail.Borough = *borough
	}
	detail.PriceLevel = int16PtrToInt(priceLevel)
	detail.Rating = rating
	detail.SnapshotStatus = status
	return detail, nil
}

// scanCandidates decodes a projected candidate list.
func scanCandidates(rows pgx.Rows) ([]search.RestaurantCandidate, error) {
	defer rows.Close()
	out := make([]search.RestaurantCandidate, 0, topK(0))
	for rows.Next() {
		detail, err := scanRestaurantDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, candidateFromDetail(detail))
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate restaurant search", err)
	}
	return out, nil
}

// scanMatchCandidates decodes a candidate list carrying its match score.
func scanMatchCandidates(rows pgx.Rows) ([]search.RestaurantCandidate, error) {
	defer rows.Close()
	out := make([]search.RestaurantCandidate, 0, topK(0))
	for rows.Next() {
		var (
			detail     search.RestaurantDetail
			matchScore float64
			address    *string
			borough    *string
			priceLevel *int16
			rating     *float64
			status     string
		)
		if err := rows.Scan(
			&detail.RestaurantID, &detail.Name, &address, &borough, &detail.Cuisines,
			&priceLevel, &rating, &detail.RatingCount, &detail.Source, &status,
			&detail.KnowledgeScore, &detail.IsActiveForDemo, &detail.SnapshotAt, &matchScore,
		); err != nil {
			return nil, operationError("postgres: scan text match", err)
		}
		if address != nil {
			detail.Address = *address
		}
		if borough != nil {
			detail.Borough = *borough
		}
		detail.PriceLevel = int16PtrToInt(priceLevel)
		detail.Rating = rating
		detail.SnapshotStatus = status

		candidate := candidateFromDetail(detail)
		candidate.Score = matchScore
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate text matches", err)
	}
	return out, nil
}

// candidateFromDetail projects a restaurant onto a candidate.
//
// The score is left at zero: a repository that invented its own ordering would
// leave fusion with two numbers that claim to measure the same thing.
func candidateFromDetail(detail search.RestaurantDetail) search.RestaurantCandidate {
	return search.RestaurantCandidate{
		RestaurantID: detail.RestaurantID,
		Name:         detail.Name,
		Address:      detail.Address,
		Borough:      detail.Borough,
		Cuisines:     detail.Cuisines,
		PriceLevel:   detail.PriceLevel,
		Rating:       detail.Rating,
		RatingCount:  detail.RatingCount,
		SnapshotAt:   detail.SnapshotAt,
	}
}
