package slots

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// Extraction is the intermediate shape both extractors produce: the model
// through a JSON schema, the rules parser by scanning the sentence. Keeping
// them on one shape means the normalization — the part with all the judgement in
// it — runs identically for both, so a rule-derived plan cannot be subtly less
// normal than a model-derived one.
//
// The field names are the ones the model sees in its schema; they are also the
// names the read-only interpret endpoint projects, so there is one vocabulary
// from end to end.
type Extraction struct {
	Intent       string   `json:"intent"`
	Query        string   `json:"query"`
	Borough      string   `json:"borough"`
	Neighborhood string   `json:"neighborhood"`
	Cuisines     []string `json:"cuisines"`
	PriceLevels  []int    `json:"price_levels"`
	// MinRating and OpenNow are pointers so "not stated" stays distinguishable
	// from "explicitly zero" — the same reason the domain filter uses pointers.
	MinRating         *float64        `json:"min_rating"`
	OpenNow           *bool           `json:"open_now"`
	NamedRestaurants  []string        `json:"named_restaurants"`
	SoftConditions    []SoftCondition `json:"soft_conditions"`
	MissingSlots      []string        `json:"missing_slots"`
	NeedClarification bool            `json:"need_clarification"`
}

// BuildPlan normalizes an extraction into an executable plan.
//
// It is exported because it is the whole deterministic contract of this
// package: the tests drive it directly with hand-written extractions, and the
// read-only interpret endpoint projects its result. Everything a caller can
// observe about a plan is decided here.
//
// The normalization does four things a raw extraction cannot be trusted to do:
//
//   - canonicalize spellings (曼哈顿 → manhattan, 意餐 → italian),
//   - move a商圈 the model put in the borough field into Neighborhood,
//   - strip soft words out of the hard filter, and
//   - refuse to invent a query: an empty query becomes the user's own sentence.
//
// Everything it changes away from what was extracted is recorded in Warnings,
// because a normalization a reviewer cannot see is indistinguishable from the
// model having got it right.
func BuildPlan(userInput string, ex Extraction, source string) Plan {
	plan := Plan{
		Intent: Intent(strings.TrimSpace(ex.Intent)),
		Query:  strings.TrimSpace(ex.Query),
		Source: source,
	}
	if !plan.Intent.Valid() {
		if plan.Intent != "" {
			plan.Warnf("无法识别的意图“%s”，按 %s 处理", plan.Intent, DefaultIntent)
		}
		plan.Intent = DefaultIntent
	}
	if plan.Query == "" {
		// Falling back to the whole sentence keeps the keyword and vector
		// channels alive. A plan with slots but no query would silently lose
		// every condition that only a soft channel could interpret.
		plan.Query = strings.TrimSpace(userInput)
	}

	normalizeBorough(&plan, ex)
	normalizeNeighborhood(&plan, ex)
	normalizeCuisines(&plan, ex)
	normalizePriceLevels(&plan, ex)
	normalizeRating(&plan, ex)
	normalizeOpenNow(&plan, ex)
	normalizeNames(&plan, ex)
	normalizeSoftConditions(&plan, ex)

	plan.MissingSlots = normalizeSlots(ex.MissingSlots)
	plan.NeedClarification = ex.NeedClarification
	// A plan that admits it is missing something must ask, even if the model
	// forgot to say so: the two fields describe one state and disagreeing about
	// it is a bug the transport would surface as a stream that stops without a
	// question.
	if len(plan.MissingSlots) > 0 {
		plan.NeedClarification = true
	}

	warnUngeocodableProximity(&plan, userInput)
	plan.Warnings = dedupeWarnings(plan.Warnings)
	return plan
}

// ---------------------------------------------------------------------------
// Value normalization
// ---------------------------------------------------------------------------

func normalizeBorough(plan *Plan, ex Extraction) {
	value := strings.TrimSpace(ex.Borough)
	if value == "" {
		return
	}
	if borough, ok := canonicalBorough(value); ok {
		plan.HardFilters.Borough = borough
		return
	}
	// A商圈 in the borough field is a correctable mistake, not an invalid one:
	// the user named a place to be, and the address filter expresses it exactly.
	// It is moved rather than rejected because rejecting it would make a
	// perfectly answerable question fail validation.
	if neighborhood, ok := canonicalNeighborhood(value); ok {
		plan.Warnf("“%s”是商圈而非行政区，已按地址关键词过滤", value)
		plan.HardFilters.Neighborhood = neighborhood
		return
	}
	// Anything else is left in place deliberately. An unrecognised borough must
	// reach Filter.Validate() and come back as retrieval_invalid_filter; quietly
	// dropping it would turn "that is not a borough" into "no restaurants
	// matched", which are different answers to the user and indistinguishable
	// once the value is gone.
	plan.HardFilters.Borough = search.CanonicalBorough(value)
}

func normalizeNeighborhood(plan *Plan, ex Extraction) {
	value := strings.TrimSpace(ex.Neighborhood)
	if value == "" {
		return
	}
	if neighborhood, ok := canonicalNeighborhood(value); ok {
		plan.HardFilters.Neighborhood = neighborhood
		return
	}
	// An unrecognised商圈 is still a usable address fragment: the domain filter
	// matches it as a case-insensitive substring of the address, so passing it
	// through verbatim is the most faithful thing to do.
	plan.HardFilters.Neighborhood = value
}

func normalizeCuisines(plan *Plan, ex Extraction) {
	seen := map[string]struct{}{}
	for _, raw := range ex.Cuisines {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		// The hard/soft boundary is enforced here as well as on the model's
		// prompt, because a prompt is a request and this is a check. A soft word
		// that arrived as a cuisine is moved to where it belongs, and the move
		// is recorded so the model's mistake is visible rather than corrected in
		// silence.
		if topic, ok := softTopicForValue(value); ok {
			addSoftCondition(plan, value, topic)
			plan.Warnf("硬条件中的“%s”属于评论推断条件，已移入软条件（%s）", value, topic)
			continue
		}
		for _, cuisine := range canonicalCuisines(value) {
			if _, ok := seen[cuisine]; ok {
				continue
			}
			seen[cuisine] = struct{}{}
			plan.HardFilters.Cuisines = append(plan.HardFilters.Cuisines, cuisine)
		}
	}
	sortStrings(plan.HardFilters.Cuisines)
}

func normalizePriceLevels(plan *Plan, ex Extraction) {
	for _, level := range ex.PriceLevels {
		if !containsInt(plan.HardFilters.PriceLevels, level) {
			plan.HardFilters.PriceLevels = append(plan.HardFilters.PriceLevels, level)
		}
	}
	sortInts(plan.HardFilters.PriceLevels)
}

func normalizeRating(plan *Plan, ex Extraction) {
	if ex.MinRating == nil {
		return
	}
	value := *ex.MinRating
	clamped := clampRating(value)
	if clamped != value {
		plan.Warnf("评分阈值 %.2f 超出 0..5，已取 %.2f", value, clamped)
	}
	plan.HardFilters.MinRating = &clamped
}

func normalizeOpenNow(plan *Plan, ex Extraction) {
	if ex.OpenNow == nil {
		return
	}
	value := *ex.OpenNow
	plan.HardFilters.OpenNow = &value
}

func normalizeNames(plan *Plan, ex Extraction) {
	seen := map[string]struct{}{}
	for _, raw := range ex.NamedRestaurants {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		plan.NamedRestaurants = append(plan.NamedRestaurants, name)
	}
}

func normalizeSoftConditions(plan *Plan, ex Extraction) {
	for _, condition := range ex.SoftConditions {
		text := strings.TrimSpace(condition.Text)
		if text == "" {
			continue
		}
		topic := strings.TrimSpace(condition.Topic)
		if !review.KnownTopic(topic) {
			// The model's topic is a suggestion, not a fact: it is re-derived
			// from the user's own words, and a topic nobody can act on is
			// dropped rather than carried into a recall that would filter on a
			// key no document has.
			if derived, ok := softTopicForValue(text); ok {
				topic = derived
			} else {
				if topic != "" {
					plan.Warnf("软条件“%s”的主题“%s”不是已知主题，已忽略该主题", text, topic)
				}
				topic = ""
			}
		}
		addSoftCondition(plan, text, topic)
	}
}

// ---------------------------------------------------------------------------
// Text scanning (the rules parser's building blocks)
// ---------------------------------------------------------------------------

// canonicalBorough maps a borough spelling onto its canonical value.
func canonicalBorough(value string) (string, bool) {
	key := normalizeKey(value)
	if borough, ok := boroughAliases[key]; ok {
		return borough, true
	}
	if search.ValidBorough(value) {
		return search.CanonicalBorough(value), true
	}
	return "", false
}

// canonicalNeighborhood maps a商圈 spelling onto the address fragment the
// corpus carries. The returned value is the English spelling because that is
// what the addresses are written in.
func canonicalNeighborhood(value string) (string, bool) {
	key := normalizeKey(value)
	neighborhood, ok := neighborhoodAliases[key]
	return neighborhood, ok
}

// canonicalCuisines maps a cuisine spelling onto one or more canonical tokens.
// A single word may legitimately name two things — "日料" implies both the
// cuisine and the fact that sushi is on the menu — and returning both is more
// useful than picking one, because the repository treats the list as "any of".
func canonicalCuisines(value string) []string {
	key := normalizeKey(value)
	if cuisines, ok := cuisineAliases[key]; ok {
		return cuisines
	}
	cleaned := strings.ToLower(strings.TrimSpace(value))
	if cleaned == "" {
		return nil
	}
	return []string{cleaned}
}

// boroughAliases maps every spelling a user might type onto the five canonical
// boroughs. Both spellings are normalized before lookup, so casing and spacing
// never decide whether a borough is recognised.
var boroughAliases = map[string]string{
	"曼哈顿":   "manhattan",
	"曼哈顿区":  "manhattan",
	"布鲁克林":  "brooklyn",
	"布鲁克林区": "brooklyn",
	"皇后区":   "queens",
	"皇后":    "queens",
	"昆斯":    "queens",
	"布朗克斯":  "bronx",
	"布朗士":   "bronx",
	"史泰登岛":  "staten_island",
	"斯塔滕岛":  "staten_island",
	"斯坦顿岛":  "staten_island",

	// English spellings, so a mixed-language question ("ramen in Brooklyn") is
	// recognized the same way as a Chinese one.
	"manhattan":     "manhattan",
	"brooklyn":      "brooklyn",
	"queens":        "queens",
	"bronx":         "bronx",
	"staten island": "staten_island",
	"staten_island": "staten_island",
}

// neighborhoodAliases maps a商圈 onto the substring that appears in the
// corpus's addresses.
//
// It is a lookup rather than a geocoder: "中城" is not a point, and this project
// has no geocoder, so the honest representation of "in Midtown" is an address
// filter. The corpus writes addresses in English, so the canonical value is the
// English spelling even when the user typed Chinese — the filter compares
// substrings, and the user's spelling will not appear in the address.
var neighborhoodAliases = map[string]string{
	"中城":     "Midtown",
	"曼哈顿中城":  "Midtown",
	"下城":     "Downtown",
	"上城":     "Uptown",
	"上东区":    "Upper East Side",
	"上西区":    "Upper West Side",
	"苏活":     "SoHo",
	"soho":   "SoHo",
	"唐人街":    "Chinatown",
	"中国城":    "Chinatown",
	"小意大利":   "Little Italy",
	"东村":     "East Village",
	"西村":     "West Village",
	"格林威治村":  "Greenwich Village",
	"翠贝卡":    "Tribeca",
	"金融区":    "Financial District",
	"时代广场":   "Times Square",
	"切尔西":    "Chelsea",
	"哈莱姆":    "Harlem",
	"地狱厨房":   "Hell's Kitchen",
	"哈德逊广场":  "Hudson Yards",
	"熨斗区":    "Flatiron",
	"肉库区":    "Meatpacking",
	"诺利塔":    "Nolita",
	"包厘街":    "Bowery",
	"威廉斯堡":   "Williamsburg",
	"布鲁克林高地": "Brooklyn Heights",
	"公园坡":    "Park Slope",
	"绿点":     "Greenpoint",
	"阿斯托利亚":  "Astoria",
	"法拉盛":    "Flushing",
	"长岛市":    "Long Island City",
	"杰克逊高地":  "Jackson Heights",
}

// cuisineAliases maps a cuisine spelling onto canonical tokens.
var cuisineAliases = map[string][]string{
	"日餐":     {"japanese"},
	"日料":     {"japanese", "sushi"},
	"日本菜":    {"japanese"},
	"日本料理":   {"japanese"},
	"寿司":     {"sushi"},
	"拉面":     {"ramen"},
	"居酒屋":    {"japanese", "izakaya"},
	"意餐":     {"italian"},
	"意大利菜":   {"italian"},
	"意大利餐":   {"italian"},
	"披萨":     {"pizza"},
	"比萨":     {"pizza"},
	"中餐":     {"chinese"},
	"中国菜":    {"chinese"},
	"川菜":     {"sichuan", "chinese"},
	"粤菜":     {"cantonese", "chinese"},
	"火锅":     {"hotpot", "chinese"},
	"点心":     {"dim_sum", "chinese"},
	"小笼包":    {"dim_sum", "chinese"},
	"韩餐":     {"korean"},
	"韩料":     {"korean"},
	"韩国菜":    {"korean"},
	"韩式烤肉":   {"korean", "bbq"},
	"泰餐":     {"thai"},
	"泰国菜":    {"thai"},
	"越南菜":    {"vietnamese"},
	"越南粉":    {"vietnamese"},
	"印度菜":    {"indian"},
	"墨西哥菜":   {"mexican"},
	"塔可":     {"mexican", "tacos"},
	"法餐":     {"french"},
	"法国菜":    {"french"},
	"美式":     {"american"},
	"汉堡":     {"burger"},
	"素食":     {"vegetarian"},
	"咖啡馆":    {"cafe"},
	"咖啡":     {"cafe"},
	"早午餐":    {"brunch"},
	"面包店":    {"bakery"},
	"甜品":     {"dessert"},
	"甜点":     {"dessert"},
	"牛排":     {"steakhouse"},
	"地中海菜":   {"mediterranean"},
	"希腊菜":    {"greek"},
	"西班牙菜":   {"spanish"},
	"中东菜":    {"middle_eastern"},
	"埃塞俄比亚菜": {"ethiopian"},
	"酒吧":     {"bar"},

	// English spellings. They are scanned as substrings like the Chinese ones,
	// so they are limited to words that cannot appear inside an unrelated word.
	"ramen":         {"ramen"},
	"sushi":         {"sushi"},
	"izakaya":       {"japanese", "izakaya"},
	"pizza":         {"pizza"},
	"italian":       {"italian"},
	"japanese":      {"japanese"},
	"chinese":       {"chinese"},
	"sichuan":       {"sichuan", "chinese"},
	"cantonese":     {"cantonese", "chinese"},
	"hotpot":        {"hotpot", "chinese"},
	"dim sum":       {"dim_sum", "chinese"},
	"korean":        {"korean"},
	"thai":          {"thai"},
	"vietnamese":    {"vietnamese"},
	"indian":        {"indian"},
	"mexican":       {"mexican"},
	"tacos":         {"mexican", "tacos"},
	"french":        {"french"},
	"american":      {"american"},
	"burger":        {"burger"},
	"vegetarian":    {"vegetarian"},
	"vegan":         {"vegan", "vegetarian"},
	"cafe":          {"cafe"},
	"brunch":        {"brunch"},
	"bakery":        {"bakery"},
	"dessert":       {"dessert"},
	"steakhouse":    {"steakhouse"},
	"mediterranean": {"mediterranean"},
	"greek":         {"greek"},
	"spanish":       {"spanish"},
	"ethiopian":     {"ethiopian"},
}

// softTopicKeywords is the product vocabulary that maps a user's phrasing onto
// a review topic.
//
// Every entry exists because reviews discuss it and no column records it. The
// list is intentionally short: it is a product decision, and a word that is not
// on it stays in the free-text query, where the vector channel can still use it.
var softTopicKeywords = map[string]string{
	// ambience
	"安静":    review.TopicAmbience,
	"安静点":   review.TopicAmbience,
	"安静一点":  review.TopicAmbience,
	"安静的环境": review.TopicAmbience,
	"氛围好":   review.TopicAmbience,
	"氛围不错":  review.TopicAmbience,
	"有氛围":   review.TopicAmbience,
	"氛围":    review.TopicAmbience,
	"环境好":   review.TopicAmbience,
	"环境不错":  review.TopicAmbience,
	"环境":    review.TopicAmbience,
	"浪漫":    review.TopicAmbience,
	"有情调":   review.TopicAmbience,
	"情调":    review.TopicAmbience,
	"适合约会":  review.TopicAmbience,
	"约会":    review.TopicAmbience,
	"温馨":    review.TopicAmbience,
	"舒适":    review.TopicAmbience,
	"情调好":   review.TopicAmbience,

	// wait
	"排队":   review.TopicWait,
	"不用排队": review.TopicWait,
	"等位":   review.TopicWait,
	"不用等位": review.TopicWait,
	"等待":   review.TopicWait,
	"出餐快":  review.TopicWait,
	"上菜快":  review.TopicWait,
	"上菜速度": review.TopicWait,

	// service
	"服务好":  review.TopicService,
	"服务":   review.TopicService,
	"态度好":  review.TopicService,
	"服务周到": review.TopicService,
	"贴心":   review.TopicService,

	// value
	"性价比":  review.TopicValue,
	"性价比高": review.TopicValue,
	"实惠":   review.TopicValue,
	"划算":   review.TopicValue,
	"物有所值": review.TopicValue,
	"份量足":  review.TopicValue,

	// food
	"好吃":   review.TopicFood,
	"菜品好":  review.TopicFood,
	"味道好":  review.TopicFood,
	"美味":   review.TopicFood,
	"正宗":   review.TopicFood,
	"招牌菜":  review.TopicFood,
	"菜品丰富": review.TopicFood,

	// kid friendly
	"带孩子":   review.TopicKidFriendly,
	"亲子":    review.TopicKidFriendly,
	"适合带孩子": review.TopicKidFriendly,
	"儿童":    review.TopicKidFriendly,

	// group friendly
	"适合聚会": review.TopicGroupFriendly,
	"聚会":   review.TopicGroupFriendly,
	"聚餐":   review.TopicGroupFriendly,
	"团建":   review.TopicGroupFriendly,
	"人多":   review.TopicGroupFriendly,

	// English phrasings, for a question written partly or wholly in English.
	// They are matched as substrings like the Chinese entries, so only phrases
	// that cannot occur inside an unrelated word are listed — a bare "date"
	// would fire on "candidate", and a wrong soft condition is worse than a
	// missed one.
	"quiet":           review.TopicAmbience,
	"cozy":            review.TopicAmbience,
	"romantic":        review.TopicAmbience,
	"date night":      review.TopicAmbience,
	"no wait":         review.TopicWait,
	"good service":    review.TopicService,
	"good value":      review.TopicValue,
	"delicious":       review.TopicFood,
	"tasty":           review.TopicFood,
	"kid friendly":    review.TopicKidFriendly,
	"family friendly": review.TopicKidFriendly,
	"good for groups": review.TopicGroupFriendly,
}

// openNowKeywords are the phrasings that mean "open right now", which is the
// only way a rules-derived plan can set the three-state OpenNow filter: the
// corpus records an open state, and absence of one must never read as "closed".
var openNowKeywords = []string{
	"现在营业", "现在还开着", "营业中", "现在开门", "open now", "still open", "open right now",
}

// scanOpenNow reads an open-now requirement out of a sentence.
func scanOpenNow(text string) *bool {
	if !hasAny(strings.ToLower(text), openNowKeywords) {
		return nil
	}
	value := true
	return &value
}

// softTopicForValue returns the review topic a phrase belongs to.
//
// The lookup is exact after normalization rather than a substring search. A
// substring rule would classify "环境" inside "环境很好的日料" correctly but also
// fire on any sentence containing the word incidentally, and a wrongly claimed
// soft condition is worse than a missed one: it makes the answer assert a
// review-based inference the user never asked for.
func softTopicForValue(value string) (string, bool) {
	topic, ok := softTopicKeywords[normalizeKey(value)]
	return topic, ok
}

// TopicFor maps a single soft-condition phrase onto its review topic.
//
// It is exported for callers that receive soft conditions from somewhere other
// than this package — a model that filled the tool's soft_conditions argument
// itself, say — and still have to hand the retrieval layer a topic alongside
// the words. The lookup is exact after normalisation rather than a substring
// scan, for the same reason softTopicForValue is: claiming a topic the user
// never asked about makes the answer assert an inference nobody requested,
// which is worse than admitting the phrase could not be mapped.
func TopicFor(phrase string) (string, bool) {
	return softTopicForValue(phrase)
}

// scanSoftConditions finds every soft condition in a sentence, longest phrase
// first so "适合约会" is one condition rather than "约会".
func scanSoftConditions(text string) []SoftCondition {
	lowered := strings.ToLower(text)
	matches := make([]struct {
		index int
		topic string
		text  string
	}, 0, 4)
	for keyword, topic := range softTopicKeywords {
		index := strings.Index(lowered, strings.ToLower(keyword))
		if index < 0 {
			continue
		}
		matches = append(matches, struct {
			index int
			topic string
			text  string
		}{index: index, topic: topic, text: keyword})
	}
	// A longer phrase at the same position wins; otherwise the earlier position
	// wins, so the conditions come out in the order the user wrote them.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].index != matches[j].index {
			return matches[i].index < matches[j].index
		}
		return len(matches[i].text) > len(matches[j].text)
	})
	conditions := make([]SoftCondition, 0, len(matches))
	covered := make([]bool, len(lowered))
	for _, match := range matches {
		if overlap(covered, match.index, len(match.text)) {
			continue
		}
		for i := match.index; i < match.index+len(match.text) && i < len(covered); i++ {
			covered[i] = true
		}
		conditions = append(conditions, SoftCondition{Text: match.text, Topic: match.topic})
	}
	return conditions
}

func overlap(covered []bool, start, length int) bool {
	for i := start; i < start+length && i < len(covered); i++ {
		if i >= 0 && covered[i] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Text scanning: hard conditions
// ---------------------------------------------------------------------------

var (
	// dollarPricePattern matches a run of '$' so "under $$" and "$$$$" both read
	// as a price ceiling. The longest run wins when several appear.
	dollarPricePattern = regexp.MustCompile(`\${1,4}`)
	// priceCeilingWords mark a phrase as an upper bound rather than an exact
	// level: "under $$" means levels one and two, not level two only.
	priceCeilingWords = []string{"以下", "以内", "under", "below", "less than", "不超过", "不高于"}
	// ratingUnitPattern requires a rating unit (星/分/star), which is what keeps
	// "3 人以上" from being read as a 3-star minimum: without a unit there is
	// nothing that says the number is a rating at all.
	ratingUnitPattern = regexp.MustCompile(`(\d(?:\.\d)?)\s*(?:星|分|stars?)\s*(?:以上|及以上|起|\+|plus|or more|or better)?`)
	// ratingPlusPattern catches the unitless "4.5+" form.
	ratingPlusPattern = regexp.MustCompile(`(\d(?:\.\d)?)\s*\+`)
	// nearMePattern matches the phrasings that mean "near me" when no
	// coordinates were supplied.
	nearMePattern = regexp.MustCompile(`附近|周边|旁边|离我近|离我很近|near me|nearby|close to me|walking distance`)
)

// scanBorough finds a borough name in a sentence.
func scanBorough(text string) (string, string, bool) {
	return scanAlias(text, boroughAliases)
}

// scanNeighborhood finds a商圈 in a sentence.
func scanNeighborhood(text string) (string, string, bool) {
	return scanAlias(text, neighborhoodAliases)
}

// scanAlias returns the longest alias that appears in text, with the matched
// spelling so the caller can quote what the user wrote.
func scanAlias(text string, aliases map[string]string) (string, string, bool) {
	lowered := strings.ToLower(text)
	bestValue, bestMatch := "", ""
	for keyword, value := range aliases {
		needle := strings.ToLower(keyword)
		if !strings.Contains(lowered, needle) {
			continue
		}
		if len(needle) > len(bestMatch) {
			bestMatch, bestValue = keyword, value
		}
	}
	if bestMatch == "" {
		return "", "", false
	}
	return bestValue, bestMatch, true
}

// scanCuisines collects every cuisine named in a sentence.
func scanCuisines(text string) []string {
	lowered := strings.ToLower(text)
	seen := map[string]struct{}{}
	var out []string
	for keyword, cuisines := range cuisineAliases {
		if !strings.Contains(lowered, strings.ToLower(keyword)) {
			continue
		}
		for _, cuisine := range cuisines {
			if _, ok := seen[cuisine]; ok {
				continue
			}
			seen[cuisine] = struct{}{}
			out = append(out, cuisine)
		}
	}
	sortStrings(out)
	return out
}

// scanPriceLevels reads a price constraint out of a sentence.
func scanPriceLevels(text string) ([]int, bool) {
	lowered := strings.ToLower(text)
	if runs := dollarPricePattern.FindAllString(text, -1); len(runs) > 0 {
		level := 0
		for _, run := range runs {
			if len(run) > level {
				level = len(run)
			}
		}
		if hasAny(lowered, priceCeilingWords) {
			return rangeInts(1, level), true
		}
		return []int{level}, true
	}
	for word, levels := range priceWordAliases {
		if !strings.Contains(lowered, word) {
			continue
		}
		return levels, true
	}
	return nil, false
}

// priceWordAliases maps a price word onto the levels it denotes.
var priceWordAliases = map[string][]int{
	"便宜":  {1},
	"实惠":  {1},
	"经济":  {1},
	"不贵":  {1, 2},
	"中等":  {2},
	"中档":  {2},
	"适中":  {2},
	"高档":  {3},
	"精致":  {3},
	"很贵":  {4},
	"超高档": {4},
	"奢华":  {4},
}

// scanMinRating reads a minimum rating out of a sentence.
func scanMinRating(text string) *float64 {
	if match := ratingUnitPattern.FindStringSubmatch(strings.ToLower(text)); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			clamped := clampRating(value)
			return &clamped
		}
	}
	if match := ratingPlusPattern.FindStringSubmatch(text); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			clamped := clampRating(value)
			return &clamped
		}
	}
	return nil
}

// clampRating keeps a rating threshold inside the domain's 0..5 range.
func clampRating(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 5:
		return 5
	default:
		return value
	}
}

// warnUngeocodableProximity records that a "near me" request could not be
// honoured geographically.
//
// Without a geocoder there is no centre point to measure from, and inventing one
// would rank by a location the user never supplied. The condition degrades to
// the borough/neighborhood filter — which narrows the area honestly — and the
// limitation is written down rather than hidden, because a user who asked for
// "near me" and got a borough-wide answer deserves to know why.
func warnUngeocodableProximity(plan *Plan, userInput string) {
	if !nearMePattern.MatchString(strings.ToLower(userInput)) {
		return
	}
	if plan.HardFilters.HasDistance() {
		return
	}
	plan.Warn("请求包含“附近”，但没有可用坐标，已降级为行政区/商圈过滤（无地理编码器）")
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func addSoftCondition(plan *Plan, text, topic string) {
	for _, existing := range plan.SoftConditions {
		if strings.EqualFold(existing.Text, text) && existing.Topic == topic {
			return
		}
	}
	plan.SoftConditions = append(plan.SoftConditions, SoftCondition{Text: text, Topic: topic})
}

func normalizeSlots(slots []string) []string {
	known := map[string]struct{}{
		SlotBorough: {}, SlotCuisine: {}, SlotPriceLevel: {},
		SlotDate: {}, SlotPartySize: {}, SlotRestaurantID: {},
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(slots))
	for _, raw := range slots {
		slot := strings.ToLower(strings.TrimSpace(raw))
		if slot == "" {
			continue
		}
		if _, ok := known[slot]; !ok {
			continue
		}
		if _, ok := seen[slot]; ok {
			continue
		}
		seen[slot] = struct{}{}
		out = append(out, slot)
	}
	sortStrings(out)
	return out
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func hasAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func rangeInts(from, to int) []int {
	if to < from {
		return nil
	}
	out := make([]int, 0, to-from+1)
	for value := from; value <= to; value++ {
		out = append(out, value)
	}
	return out
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortStrings(values []string) { sort.Strings(values) }

func sortInts(values []int) { sort.Ints(values) }

func appendWarning(existing []string, message string) []string {
	message = strings.TrimSpace(message)
	if message == "" {
		return existing
	}
	for _, item := range existing {
		if item == message {
			return existing
		}
	}
	return append(existing, message)
}

func dedupeWarnings(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		out = appendWarning(out, warning)
	}
	return out
}

func formatWarning(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
