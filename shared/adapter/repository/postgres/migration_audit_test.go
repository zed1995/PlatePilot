package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/zed/platepilot/shared/domain/review"
)

// Migration 0003 and the batch statements are the one part of M2 that cannot
// be exercised without a live database, and the one where a mistake is silent:
// a mismatched column list produces a runtime error on the first write of every
// run, long after the migration itself reported success. The tests here pin the
// three things that have to agree — the migration's columns, the statement's
// column list, and the argument order — so that at least this much is proved
// without a server.

// columnName matches the identifier in a DDL or INSERT column list.
var columnName = regexp.MustCompile(`[a-z_][a-z0-9_]*`)

// TestMigration0003DefinesEveryAuditedColumn checks the migration against the
// BatchReport fields that have no home in the M1 schema.
//
// The failure this guards is specific: the migration applies cleanly, and the
// first StartBatch of the next run fails with "column does not exist". Between
// those two events there is a migration that looks fine.
func TestMigration0003DefinesEveryAuditedColumn(t *testing.T) {
	body := readMigration(t, "0003_embedding_audit.sql")

	want := []string{
		"documents_built",
		"documents_embedded",
		"documents_rejected",
		"embedding_model",
		"embedding_dimensions",
		"reject_reasons",
	}
	for _, name := range want {
		if !strings.Contains(body, name) {
			t.Errorf("migration 0003 does not mention %q; "+
				"review.BatchReport has a field for it but the column does not exist", name)
		}
	}

	if !strings.Contains(body, "ALTER TABLE ingestion_batches") {
		t.Error("migration 0003 never alters ingestion_batches, the table the stages write to")
	}
	if adds := strings.Count(body, "ADD COLUMN IF NOT EXISTS"); adds != len(want) {
		t.Errorf("migration 0003 adds %d columns, want %d; "+
			"either a column is missing or one is added twice", adds, len(want))
	}

	// The counters are nullable on purpose. A NOT NULL column with no default
	// would make the M1 stages fail to insert, since they never set them.
	for _, name := range migratedColumnNames(t, body) {
		clause := addColumnClause(body, name)
		upper := strings.ToUpper(clause)
		after := upper[strings.Index(upper, "IF NOT EXISTS")+len("IF NOT EXISTS"):]
		if strings.Contains(after, "NOT NULL") && !strings.Contains(after, "DEFAULT") {
			t.Errorf("column %q is added NOT NULL without a default; "+
				"the M1 stages do not set it and their inserts would fail", name)
		}
	}
}

// TestBatchInsertMatchesTheMigratedColumns is the check that makes 0003 and
// batchInsertSQL one change instead of two.
//
// The insert names the M2 columns explicitly, so it is correct exactly when the
// migration created all of them. A column added to the migration but missed in
// the statement is invisible until a run writes a batch, which is the exact
// shape of the bug that is hardest to find.
func TestBatchInsertMatchesTheMigratedColumns(t *testing.T) {
	migrated := migratedColumnNames(t, readMigration(t, "0003_embedding_audit.sql"))
	inserted := insertColumnNames(batchInsertSQL)
	missing := make([]string, 0, len(migrated))
	for _, name := range migrated {
		if !contains(inserted, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("migration 0003 adds %v but batchInsertSQL does not write them; "+
			"the migration is correct on its own and the run still fails", missing)
	}
}

// TestBatchUpdateSettlesEveryAuditedColumn is the same check for the update
// half of the write.
//
// The insert and the update cover the same columns, and a column present in one
// and absent from the other means the value is recorded at the start of a run
// and then silently lost when it finishes.
func TestBatchUpdateSettlesEveryAuditedColumn(t *testing.T) {
	migrated := migratedColumnNames(t, readMigration(t, "0003_embedding_audit.sql"))
	updated := setColumnNames(batchUpdateSQL)
	for _, name := range migrated {
		if !contains(updated, name) {
			t.Errorf("column %q is written by the insert but never by the update; "+
				"the value recorded at start would be lost at finish", name)
		}
	}
}

// TestBatchStatementsAgreeOnColumnAndParameterOrder checks the property a
// positional statement has and a named one does not: that argument n is the
// value of column n.
//
// Go does not check this, and PostgreSQL reports a mismatch as "column x is of
// type y but expression is of type z", which names neither the stage nor the
// field. The insert, the update, and batchArgs each have to be edited together,
// and this catches the edit that missed one of the three.
func TestBatchStatementsAgreeOnColumnAndParameterOrder(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		cols := insertColumnNames(batchInsertSQL)
		order := placeholderOrder(batchInsertSQL)
		if len(cols) != len(order) {
			t.Fatalf("insert names %d columns but binds %d parameters", len(cols), len(order))
		}
		for i, name := range cols {
			if got := order[i]; got != i+1 {
				t.Errorf("insert column %q is bound as $%d, want $%d", name, got, i+1)
			}
		}
		if highest := highestPlaceholder(order); highest != len(order) {
			t.Errorf("insert references $%d but binds %d parameters", highest, len(order))
		}
	})

	t.Run("update", func(t *testing.T) {
		// The update's order is its own: $1 is the primary key, so the SET list
		// runs $2.. in the order the columns are listed there.
		cols := setColumnNames(batchUpdateSQL)
		// Only the SET clause is bound positionally in the same order as the
		// column list; the WHERE clause carries the primary key, which is $1
		// and comes before every column in the list rather than after it.
		order := placeholderOrder(updateSetClause(batchUpdateSQL))
		if len(cols) != len(order) {
			t.Fatalf("update sets %d columns but binds %d parameters", len(cols), len(order))
		}
		for i, name := range cols {
			if got := order[i]; got != i+2 {
				t.Errorf("update column %q is bound as $%d, want $%d", name, got, i+2)
			}
		}
	})

	t.Run("batchArgs", func(t *testing.T) {
		// The Go side has no column names to compare against, so the check is
		// that it binds exactly as many arguments as the insert names columns.
		// batchArgs is the single place both statements draw their values from.
		args, _, err := batchArgs(review.BatchReport{Stage: review.StageEmbedding})
		if err != nil {
			t.Fatalf("batchArgs: %v", err)
		}
		want := len(insertColumnNames(batchInsertSQL))
		if len(args) != want {
			t.Errorf("batchArgs binds %d arguments, the insert names %d columns; "+
				"one of the two was edited without the other", len(args), want)
		}
	})
}

// TestEmbeddingReportFieldsAreDistinctive guards a subtler version of the same
// bug. A pointer field left nil is the convention for "this stage does not
// apply", and a report that sets DocumentsBuilt to a pointer to zero has claimed
// that the stage counted zero rows rather than that it did not run. The M2
// report columns are nullable precisely so the two stay distinguishable.
func TestEmbeddingReportFieldsAreDistinctive(t *testing.T) {
	empty := review.BatchReport{Stage: review.StageEmbedding}
	if empty.DocumentsBuilt != nil || empty.DocumentsEmbedded != nil || empty.DocumentsRejected != nil {
		t.Error("a zero-value BatchReport carries non-nil counters; " +
			"the audit row would claim the stage ran and counted nothing")
	}
	if empty.EmbeddingModel != "" || empty.EmbeddingDimensions != nil {
		t.Error("a zero-value BatchReport claims an embedding model")
	}
	if empty.RejectReasons != nil {
		t.Error("a zero-value BatchReport carries rejection reasons")
	}
}

// --- helpers ---------------------------------------------------------------

// readMigration loads a migration file from the on-disk directory.
func readMigration(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// addColumnClause returns the ADD COLUMN text declaring the named column.
func addColumnClause(body, name string) string {
	marker := "ADD COLUMN IF NOT EXISTS " + name
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	rest := body[idx:]
	if end := strings.IndexAny(rest, ";\n"); end > 0 {
		// Keep only the declaration, not the following columns in the same
		// ALTER: the type is the only thing that follows the name.
		if comma := strings.Index(rest, ","); comma > 0 && comma < end {
			return rest[:comma]
		}
		return rest[:end]
	}
	return rest
}

// migratedColumnNames lists the columns the migration adds.
func migratedColumnNames(t *testing.T, body string) []string {
	t.Helper()
	const marker = "ADD COLUMN IF NOT EXISTS "
	var out []string
	rest := body
	for {
		idx := strings.Index(rest, marker)
		if idx < 0 {
			return out
		}
		rest = rest[idx+len(marker):]
		end := strings.IndexAny(rest, " ,\n")
		if end < 0 {
			return out
		}
		if name := rest[:end]; name != "" {
			out = append(out, name)
		}
	}
}

// insertColumnNames returns the column list of the INSERT statement.
func insertColumnNames(statement string) []string {
	open := strings.Index(statement, "(")
	closed := strings.Index(statement, ")")
	if open < 0 || closed < open {
		return nil
	}
	return columnName.FindAllString(statement[open+1:closed], -1)
}

// setColumnNames returns the columns assigned by an UPDATE's SET clause, in
// the order the statement lists them.
//
// The split cannot be a plain strings.Split on commas: most assignments here
// are wrapped in COALESCE($n, column), whose own comma is not a separator.
// Splitting naively turns one assignment into two halves and makes the column
// count disagree with the placeholder count for a statement that is perfectly
// correct, so the assignments are cut at top-level commas only.
func setColumnNames(statement string) []string {
	start := strings.Index(statement, "SET")
	if start < 0 {
		return nil
	}
	rest := statement[start+len("SET"):]
	if end := strings.Index(rest, "WHERE"); end >= 0 {
		rest = rest[:end]
	}
	var out []string
	for _, assignment := range splitTopLevel(rest) {
		parts := strings.SplitN(assignment, "=", 2)
		if len(parts) != 2 {
			continue
		}
		// "missing_fields= $11" has no space before the operator.
		if name := columnName.FindString(strings.TrimSpace(parts[0])); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// updateSetClause returns the SET clause of an UPDATE, without the WHERE.
func updateSetClause(statement string) string {
	start := strings.Index(statement, "SET")
	if start < 0 {
		return ""
	}
	rest := statement[start+len("SET"):]
	if end := strings.Index(rest, "WHERE"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// splitTopLevel splits on commas that are not inside parentheses.
func splitTopLevel(clause string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range clause {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, clause[start:i])
				start = i + 1
			}
		}
	}
	return append(out, clause[start:])
}

// placeholderOrder returns the $n references in the order they appear.
func placeholderOrder(statement string) []int {
	var out []int
	for _, match := range placeholderPattern.FindAllStringSubmatch(statement, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// highestPlaceholder returns the largest $n referenced.
func highestPlaceholder(order []int) int {
	highest := 0
	for _, n := range order {
		if n > highest {
			highest = n
		}
	}
	return highest
}

// contains reports whether names holds name.
func contains(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

// --- upsertDocumentsSQL version assignment ---------------------------------

// upsertDocumentsSQL 里 next_version 子查询的 body（从 Go 源码里复制会漂，
// 这里只验证"子查询的 WHERE 条件是否覆盖批内同组"这一结构性质）。
var upsertVersionSQL = upsertDocumentsSQL

// nextVersionSubquery returns the next_version CTE body.
func nextVersionSubquery(sql string) string {
	i := strings.Index(sql, "next_version AS (")
	if i < 0 {
		return ""
	}
	// The CTE is closed by the line that ends the statement's last ")" at
	// column zero; taking everything up to the INSERT is precise enough and
	// does not depend on the exact indentation.
	j := strings.Index(sql, "INSERT INTO knowledge_documents")
	if j < 0 {
		return sql[i:]
	}
	return sql[i:j]
}

var groupKeys = []string{"restaurant_id", "retrieval_scope", "doc_type"}

// aliasJoinOn reports whether the statement joins two relations on the given
// column, whatever it calls them.
func aliasJoinOn(sql, key string) bool {
	pattern := regexp.MustCompile(`([a-z])\.` + key + `\s*=\s*([a-z])\.` + key)
	m := pattern.FindStringSubmatch(sql)
	if m == nil {
		return false
	}
	// 两侧必须是不同的关系别名，否则那是同 relation 内的自比较。
	return m[1] != m[2]
}

func TestVersionQuerySeesItsOwnBatch(t *testing.T) {
	sub := nextVersionSubquery(upsertVersionSQL)
	if sub == "" {
		t.Fatal("could not locate the next_version subquery; the test needs updating")
	}
	// 1. 必须按组匹配（否则不同组会串号）。别名随实现而变，所以匹配的是
	// "同一个 key 出现在 JOIN 条件两侧" 这件事，而不是某组具体的别名。
	for _, key := range groupKeys {
		matched := aliasJoinOn(sub, key)
		if !matched {
			t.Errorf("next_version does not join batches to history on %s; "+
				"documents of different groups would share a version number", key)
		}
	}
	// 2. 必须能看到同批同组的其他行，否则批内会重号
	// The batch has to be able to see itself.
	//
	// The check is deliberately narrow. An earlier version of this test
	// accepted the mere presence of the word "incoming", which every version
	// of the statement has — including the broken one, whose next_version CTE
	// selects `FROM incoming i` and then reads only the table. The test passed
	// on exactly the bug it was written for.
	//
	// A per-row ordinal that depends on the batch membership is the thing that
	// makes rows distinguish themselves, so that is what is required here.
	seesBatch := strings.Contains(sub, "row_number() OVER") &&
		regexp.MustCompile(`PARTITION BY [^)]*(restaurant_id|ord)`).MatchString(sub)
	if !seesBatch {
		t.Error("next_version has no per-row ordinal within the batch; " +
			"documents arriving together are invisible to each other and " +
			"every one of them is handed the same version number")
	}
}

func TestVersionQueryUsesAllHistory(t *testing.T) {
	sub := nextVersionSubquery(upsertVersionSQL)
	// 只能从全表取 max，不能限定 is_active
	if strings.Contains(sub, "is_active") {
		t.Error("next_version filters on is_active; a group whose versions were " +
			"all retired would hand out version 2 twice")
	}
	if !strings.Contains(sub, "max(d.version)") {
		t.Error("next_version does not use max(version) over the group's history")
	}
}
