package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/food"
)

// querier is what *sql.DB and *sql.Tx have in common.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Item is one dish of a day, prepared by the service from what the source
// sent (names trimmed, weight read, nutrition scaled to the portion).
type Item struct {
	ExternalID   string
	Name         string
	Category     string
	Price        *float64
	Description  string
	ImageURL     string
	WeightG      *float64 // from the source, or read from WeightText
	WeightText   string
	Components   []contracts.Component
	Nutrition    *contracts.Nutrition // per portion, from the source
	NutritionRaw string               // JSON as the source sent it ("" = nothing)
	// Per100: NutritionRaw is per 100 g and the weight is unknown; the values
	// are filled once the portion is known (from llm).
	Per100    bool
	Allergens []string // nil = the source said nothing
}

// SourceHash covers everything the source gave: if it changes, the day
// gets a new revision.
func (it Item) SourceHash() string {
	return food.Hash(it.InputHash(), it.Category, food.F(it.Price), it.NutritionRaw, food.JSON(it.Allergens))
}

// InputHash covers what llm looks at: if it changes, llm's estimates for
// that day are dropped and asked again.
func (it Item) InputHash() string {
	return food.Hash(food.NormName(it.Name), it.Description, food.F(it.WeightG), it.WeightText, it.ImageURL, food.JSON(it.Components))
}

// DayResult says what ImportDay did.
type DayResult struct {
	Date     string
	Revision int
	Items    int
	Skipped  bool // a revision older than the saved one
	Changed  bool
}

// sourceLabel is the value of weight_source / components.source for a menu source.
func sourceLabel(source string) string {
	if source == "api" {
		return "kitchen"
	}
	return "site"
}

// ImportDay saves the menu of one day. The new revision replaces the old
// one: dishes that disappeared are removed from that day; dishes that stay
// keep llm's estimates unless what llm looks at changed. rev = 0 means the
// source gave no revision: catalog counts them (a new one when something
// changed). analysisOn = llm may be asked.
func (s *Store) ImportDay(ctx context.Context, source, date string, rev int, items []Item, analysisOn bool, now time.Time) (DayResult, error) {
	res := DayResult{Date: date}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	var saved int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) FROM menu_items WHERE date = ?`, date).Scan(&saved); err != nil {
		return res, err
	}
	if rev > 0 && rev < saved {
		res.Revision, res.Skipped = saved, true
		return res, nil
	}

	existing := map[int64]string{} // dish_id -> source_hash
	rows, err := tx.QueryContext(ctx, `SELECT dish_id, COALESCE(source_hash, '') FROM menu_items WHERE date = ?`, date)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id int64
		var h string
		if err := rows.Scan(&id, &h); err != nil {
			rows.Close()
			return res, err
		}
		existing[id] = h
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	type entry struct {
		dishID int64
		it     Item
	}
	var list []entry
	seen := map[int64]bool{}
	for _, it := range items {
		id, err := findOrCreateDish(ctx, tx, it, now)
		if err != nil {
			return res, err
		}
		if seen[id] { // the same dish twice in one day: the first one counts
			continue
		}
		seen[id] = true
		list = append(list, entry{id, it})
	}

	changed := len(list) != len(existing)
	for _, e := range list {
		if h, ok := existing[e.dishID]; !ok || h != e.it.SourceHash() {
			changed = true
		}
	}
	newRev := rev
	if newRev <= 0 {
		newRev = saved
		if changed || saved == 0 {
			newRev = saved + 1
		}
	}

	for dishID := range existing {
		if !seen[dishID] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM menu_items WHERE date = ? AND dish_id = ?`, date, dishID); err != nil {
				return res, err
			}
		}
	}
	label := sourceLabel(source)
	ids := make([]int64, 0, len(list))
	for i, e := range list {
		id, err := upsertItem(ctx, tx, date, e.dishID, source, label, newRev, i, e.it, now)
		if err != nil {
			return res, err
		}
		ids = append(ids, id)
		if e.it.Allergens != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE dish_info SET allergens_given = ? WHERE dish_id = ?`,
				jsonList(food.NormList(e.it.Allergens)), e.dishID); err != nil {
				return res, err
			}
		}
	}
	for _, id := range ids {
		if err := prepareAnalysis(ctx, tx, id, analysisOn); err != nil {
			return res, err
		}
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Revision, res.Items, res.Changed = newRev, len(list), changed
	return res, nil
}

// findOrCreateDish recognises the same dish from day to day: by
// external_id when the source gives one, otherwise by the normalised name.
func findOrCreateDish(ctx context.Context, tx querier, it Item, now time.Time) (int64, error) {
	norm := food.NormName(it.Name)
	var id int64
	found := func(err error) (bool, error) {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}
	if it.ExternalID != "" {
		ok, err := found(tx.QueryRowContext(ctx, `SELECT id FROM dishes WHERE external_id = ?`, it.ExternalID).Scan(&id))
		if err != nil {
			return 0, err
		}
		if !ok { // a dish known only by its name gets the external id
			ok, err = found(tx.QueryRowContext(ctx,
				`SELECT id FROM dishes WHERE name_norm = ? AND external_id IS NULL ORDER BY id LIMIT 1`, norm).Scan(&id))
			if err != nil {
				return 0, err
			}
			if ok {
				if _, err := tx.ExecContext(ctx, `UPDATE dishes SET external_id = ? WHERE id = ?`, it.ExternalID, id); err != nil {
					return 0, err
				}
			}
		}
		if ok {
			return id, renameDish(ctx, tx, id, it.Name, norm)
		}
	} else {
		ok, err := found(tx.QueryRowContext(ctx,
			`SELECT id FROM dishes WHERE name_norm = ? ORDER BY external_id IS NOT NULL, id LIMIT 1`, norm).Scan(&id))
		if err != nil {
			return 0, err
		}
		if ok {
			return id, renameDish(ctx, tx, id, it.Name, norm)
		}
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO dishes (external_id, name, name_norm, created_at) VALUES (?, ?, ?, ?)`,
		nullStr(it.ExternalID), it.Name, norm, ts(now))
	if err != nil {
		return 0, err
	}
	id, err = r.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, renameDish(ctx, tx, id, it.Name, norm)
}

// renameDish keeps the latest spelling of the name and makes sure the dish
// has its dish_info row.
func renameDish(ctx context.Context, tx querier, id int64, name, norm string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE dishes SET name = ?, name_norm = ? WHERE id = ?`, name, norm, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dish_info (dish_id, status) VALUES (?, 'pending')`, id)
	return err
}

func upsertItem(ctx context.Context, tx querier, date string, dishID int64, source, label string, rev, pos int, it Item, now time.Time) (int64, error) {
	srcHash, inHash := it.SourceHash(), it.InputHash()
	var (
		id         int64
		oldIn      string
		wsrc, nsrc sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		`SELECT id, COALESCE(input_hash, ''), weight_source, nutrition_source FROM menu_items WHERE date = ? AND dish_id = ?`,
		date, dishID).Scan(&id, &oldIn, &wsrc, &nsrc)
	if errors.Is(err, sql.ErrNoRows) {
		var wsource any
		if it.WeightG != nil {
			wsource = label
		}
		nsource := "missing"
		if it.Nutrition != nil {
			nsource = "kitchen"
		}
		args := []any{date, dishID, source, rev, nullStr(it.Category), nullF(it.Price), nullStr(it.Description),
			nullStr(it.ImageURL), nullF(it.WeightG), nullStr(it.WeightText), wsource}
		args = append(args, nutritionArgs(it.Nutrition)...)
		args = append(args, nsource, nullStr(it.NutritionRaw), ts(now), pos, srcHash, inHash)
		r, err := tx.ExecContext(ctx, `INSERT INTO menu_items (date, dish_id, source, revision, category, price, description,
			image_url, weight_g, weight_text, weight_source, `+nutritionCols+`,
			nutrition_source, nutrition_raw, submitted_at, position, source_hash, input_hash, analysis)
			VALUES (`+placeholders(len(args))+`, 'none')`, args...)
		if err != nil {
			return 0, err
		}
		if id, err = r.LastInsertId(); err != nil {
			return 0, err
		}
		return id, insertComponents(ctx, tx, id, it.Components, label)
	}
	if err != nil {
		return 0, err
	}
	inputChanged := oldIn != inHash
	reset := inputChanged // something llm may have to fill again

	if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET source = ?, revision = ?, category = ?, price = ?, description = ?,
		image_url = ?, weight_text = ?, submitted_at = ?, position = ?, source_hash = ?, input_hash = ? WHERE id = ?`,
		source, rev, nullStr(it.Category), nullF(it.Price), nullStr(it.Description), nullStr(it.ImageURL),
		nullStr(it.WeightText), ts(now), pos, srcHash, inHash, id); err != nil {
		return 0, err
	}

	// weight: the source wins; the source's old value goes if it is not given any more
	switch {
	case it.WeightG != nil:
		_, err = tx.ExecContext(ctx, `UPDATE menu_items SET weight_g = ?, weight_source = ? WHERE id = ?`, *it.WeightG, label, id)
	case wsrc.String == "site" || wsrc.String == "kitchen" || (inputChanged && wsrc.String == "llm"):
		_, err = tx.ExecContext(ctx, `UPDATE menu_items SET weight_g = NULL, weight_source = NULL WHERE id = ?`, id)
		reset = true
	}
	if err != nil {
		return 0, err
	}

	// nutrition: the same rule
	switch {
	case it.Nutrition != nil:
		args := append(nutritionArgs(it.Nutrition), it.NutritionRaw, id)
		_, err = tx.ExecContext(ctx, `UPDATE menu_items SET `+nutritionSet+`, nutrition_source = 'kitchen', nutrition_raw = ? WHERE id = ?`, args...)
	case it.NutritionRaw != "": // per 100 g, waiting for the weight
		args := append(nutritionArgs(nil), it.NutritionRaw, id)
		_, err = tx.ExecContext(ctx, `UPDATE menu_items SET `+nutritionSet+`, nutrition_source = 'missing', nutrition_raw = ? WHERE id = ?`, args...)
	case nsrc.String == "kitchen" || (inputChanged && nsrc.String == "llm"):
		args := append(nutritionArgs(nil), id)
		_, err = tx.ExecContext(ctx, `UPDATE menu_items SET `+nutritionSet+`, nutrition_source = 'missing', nutrition_raw = NULL WHERE id = ?`, args...)
		reset = true
	}
	if err != nil {
		return 0, err
	}

	// components: the source's are replaced; llm's go when the dish changed
	// or the source now gives its own
	q := `DELETE FROM menu_item_components WHERE menu_item_id = ? AND source <> 'llm'`
	if inputChanged || len(it.Components) > 0 {
		q = `DELETE FROM menu_item_components WHERE menu_item_id = ?`
	}
	if _, err := tx.ExecContext(ctx, q, id); err != nil {
		return 0, err
	}
	if err := insertComponents(ctx, tx, id, it.Components, label); err != nil {
		return 0, err
	}
	if reset {
		if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET analysis = 'none', need = NULL,
			analysis_requested_at = NULL, analysis_done_at = NULL WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// insertComponents adds components (and their names to the ingredients dictionary).
func insertComponents(ctx context.Context, tx querier, itemID int64, comps []contracts.Component, source string) error {
	for _, c := range comps {
		name := strings.Join(strings.Fields(c.Name), " ")
		if name == "" {
			continue
		}
		norm := food.NormName(name)
		if _, err := tx.ExecContext(ctx, `INSERT INTO ingredients (name, name_norm) VALUES (?, ?) ON CONFLICT(name_norm) DO NOTHING`, name, norm); err != nil {
			return err
		}
		var ingID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM ingredients WHERE name_norm = ?`, norm).Scan(&ingID); err != nil {
			return err
		}
		var grams any
		if c.Grams != nil && *c.Grams >= 0 {
			grams = *c.Grams
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO menu_item_components (menu_item_id, ingredient_id, name, grams, cooking_method, source)
			VALUES (?, ?, ?, ?, ?, ?)`, itemID, ingID, name, grams, nullStr(contracts.NormMethod(c.Method)), source); err != nil {
			return err
		}
	}
	return nil
}

// itemState is what the analysis logic needs to know about a menu item.
type itemState struct {
	id, dishID   int64
	hash         string
	analysis     string
	weight       *float64
	weightSource string
	nutrition    string // nutrition_source
	raw          string // nutrition_raw
	comps        int    // all components
	sourceComps  int    // components not from llm
}

func loadState(ctx context.Context, q querier, id int64) (itemState, error) {
	st := itemState{id: id}
	var w sql.NullFloat64
	err := q.QueryRowContext(ctx, `SELECT dish_id, COALESCE(input_hash, ''), analysis, weight_g, COALESCE(weight_source, ''),
		nutrition_source, COALESCE(nutrition_raw, ''),
		(SELECT COUNT(*) FROM menu_item_components WHERE menu_item_id = mi.id),
		(SELECT COUNT(*) FROM menu_item_components WHERE menu_item_id = mi.id AND source <> 'llm')
		FROM menu_items mi WHERE id = ?`, id).Scan(&st.dishID, &st.hash, &st.analysis, &w, &st.weightSource,
		&st.nutrition, &st.raw, &st.comps, &st.sourceComps)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	st.weight = ptrF(w)
	return st, err
}

// needOf lists what is missing (the "need" sent to llm), in the contract order.
func needOf(ctx context.Context, q querier, st itemState) ([]string, error) {
	var need []string
	if st.comps == 0 {
		need = append(need, contracts.NeedComponents)
	}
	if st.nutrition == "missing" && st.raw == "" {
		need = append(need, contracts.NeedNutrition)
	}
	var given, possible, confirmed, tags sql.NullString
	err := q.QueryRowContext(ctx, `SELECT allergens_given, allergens_possible, allergens_confirmed, tags FROM dish_info WHERE dish_id = ?`,
		st.dishID).Scan(&given, &possible, &confirmed, &tags)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !given.Valid && !possible.Valid && !confirmed.Valid {
		need = append(need, contracts.NeedAllergens)
	}
	if st.weight == nil {
		need = append(need, contracts.NeedPortion)
	}
	if !tags.Valid {
		need = append(need, contracts.NeedTags)
	}
	return need, nil
}

// applyRaw fills the nutrition from a source's "per 100 g" values once the
// weight of the portion is known.
func applyRaw(ctx context.Context, q querier, st itemState) error {
	if st.nutrition != "missing" || st.raw == "" || st.weight == nil {
		return nil
	}
	n, per100, err := food.ParseNutrition(json.RawMessage(st.raw))
	if err != nil || n == nil {
		return nil // stays missing; the raw text is kept
	}
	if per100 {
		n = n.Scale(*st.weight / 100)
	}
	args := append(nutritionArgs(n), st.id)
	_, err = q.ExecContext(ctx, `UPDATE menu_items SET `+nutritionSet+`, nutrition_source = 'kitchen' WHERE id = ?`, args...)
	return err
}

// prepareAnalysis decides the analysis state of a menu item after an
// import: copy llm's work from the same dish with the same input on
// another day, then ask llm only for what is still missing.
func prepareAnalysis(ctx context.Context, tx querier, id int64, on bool) error {
	st, err := loadState(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := applyRaw(ctx, tx, st); err != nil { // "per 100 g" values with a known weight
		return err
	}
	switch st.analysis {
	case AnalysisRequested, AnalysisDone, AnalysisTimeout:
		return nil // in the hands of llm, or already answered
	}
	if st, err = loadState(ctx, tx, id); err != nil {
		return err
	}
	need, err := needOf(ctx, tx, st)
	if err != nil {
		return err
	}
	copied := false
	if hasAny(need, contracts.NeedComponents, contracts.NeedNutrition, contracts.NeedPortion) {
		if copied, err = copyFromDonor(ctx, tx, st); err != nil {
			return err
		}
		if copied {
			if st, err = loadState(ctx, tx, id); err != nil {
				return err
			}
			if err := applyRaw(ctx, tx, st); err != nil {
				return err
			}
			if st, err = loadState(ctx, tx, id); err != nil {
				return err
			}
			if need, err = needOf(ctx, tx, st); err != nil {
				return err
			}
		}
	}

	state, requestedAt := AnalysisPending, any(nil)
	switch {
	case len(need) == 0 && copied:
		state = AnalysisDone
	case len(need) == 0:
		state = AnalysisNone
	case !on:
		state = AnalysisOff
	default:
		// the same dish with the same input is already asked about (another day of the same menu)
		var at string
		err := tx.QueryRowContext(ctx, `SELECT analysis_requested_at FROM menu_items
			WHERE dish_id = ? AND input_hash = ? AND id <> ? AND analysis = 'requested' LIMIT 1`, st.dishID, st.hash, id).Scan(&at)
		switch {
		case err == nil:
			state, requestedAt = AnalysisRequested, at
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}
	var needJSON any
	if len(need) > 0 {
		needJSON = food.JSON(need)
	}
	_, err = tx.ExecContext(ctx, `UPDATE menu_items SET analysis = ?, need = ?, analysis_requested_at = ? WHERE id = ?`,
		state, needJSON, requestedAt, id)
	return err
}

// copyFromDonor copies llm's components, portion and nutrition from the
// latest analysed item of the same dish with the same input.
func copyFromDonor(ctx context.Context, tx querier, st itemState) (bool, error) {
	var donor int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM menu_items WHERE dish_id = ? AND input_hash = ? AND id <> ? AND analysis = 'done'
		ORDER BY date DESC, id DESC LIMIT 1`, st.dishID, st.hash, st.id).Scan(&donor)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if st.comps == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO menu_item_components (menu_item_id, ingredient_id, name, grams, cooking_method, source)
			SELECT ?, ingredient_id, name, grams, cooking_method, source FROM menu_item_components
			WHERE menu_item_id = ? AND source = 'llm' ORDER BY id`, st.id, donor); err != nil {
			return false, err
		}
	}
	if st.weight == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET weight_g = (SELECT weight_g FROM menu_items WHERE id = ?), weight_source = 'llm'
			WHERE id = ? AND EXISTS (SELECT 1 FROM menu_items WHERE id = ? AND weight_source = 'llm' AND weight_g IS NOT NULL)`,
			donor, st.id, donor); err != nil {
			return false, err
		}
	}
	if st.nutrition == "missing" && st.raw == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET (`+nutritionCols+`) = (SELECT `+nutritionCols+` FROM menu_items WHERE id = ?),
			nutrition_source = 'llm'
			WHERE id = ? AND EXISTS (SELECT 1 FROM menu_items WHERE id = ? AND nutrition_source = 'llm')`,
			donor, st.id, donor); err != nil {
			return false, err
		}
	}
	return true, nil
}

func hasAny(list []string, wanted ...string) bool {
	for _, a := range list {
		for _, b := range wanted {
			if a == b {
				return true
			}
		}
	}
	return false
}

// ------------------------------------------------------------- analysis

// AnalysisJob is one food.dish.analyze.requested to send. Items lists every
// menu item with the same dish and input (one request answers them all).
type AnalysisJob struct {
	Request   contracts.DishAnalyzeRequested
	InputHash string
	Items     []int64
}

// PendingAnalyses returns the requests that must go to llm, one per dish
// and input.
func (s *Store) PendingAnalyses(ctx context.Context) ([]AnalysisJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mi.id, mi.dish_id, d.name, COALESCE(mi.description, ''), mi.weight_g,
		COALESCE(mi.weight_text, ''), COALESCE(mi.image_url, ''), COALESCE(mi.input_hash, ''), COALESCE(mi.need, '[]'),
		mi.nutrition_source, `+nutritionCols+`
		FROM menu_items mi JOIN dishes d ON d.id = mi.dish_id
		WHERE mi.analysis = 'pending' ORDER BY mi.date, mi.position, mi.id`)
	if err != nil {
		return nil, err
	}
	var jobs []AnalysisJob
	byKey := map[string]int{}
	for rows.Next() {
		var (
			r        contracts.DishAnalyzeRequested
			w        sql.NullFloat64
			hash     string
			needJSON string
			nsrc     string
			ns       nutritionScan
		)
		dest := append([]any{&r.MenuItemID, &r.DishID, &r.Name, &r.Description, &w, &r.WeightText, &r.ImageURL, &hash, &needJSON, &nsrc}, ns.dest()...)
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, err
		}
		key := strconv.FormatInt(r.DishID, 10) + "/" + hash
		if i, ok := byKey[key]; ok {
			jobs[i].Items = append(jobs[i].Items, r.MenuItemID)
			continue
		}
		r.WeightG = ptrF(w)
		_ = json.Unmarshal([]byte(needJSON), &r.Need)
		if nsrc == "kitchen" {
			r.Known.Nutrition = ns.value()
		}
		byKey[key] = len(jobs)
		jobs = append(jobs, AnalysisJob{Request: r, InputHash: hash, Items: []int64{r.MenuItemID}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range jobs {
		r := &jobs[i].Request
		comps, err := s.components(ctx, []int64{r.MenuItemID})
		if err != nil {
			return nil, err
		}
		for _, c := range comps[r.MenuItemID] {
			r.Known.Components = append(r.Known.Components, contracts.Component{Name: c.Name, Grams: c.Grams, Method: c.Method})
		}
		var given, confirmed sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT allergens_given, allergens_confirmed FROM dish_info WHERE dish_id = ?`, r.DishID).
			Scan(&given, &confirmed); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		r.Known.Allergens = append(readList(given), readList(confirmed)...)
		if r.Need == nil {
			r.Need = []string{}
		}
	}
	return jobs, nil
}

// MarkRequested says the request for these items is out.
func (s *Store) MarkRequested(ctx context.Context, items []int64, now time.Time) error {
	if len(items) == 0 {
		return nil
	}
	args := append([]any{ts(now)}, int64Args(items)...)
	_, err := s.db.ExecContext(ctx, `UPDATE menu_items SET analysis = 'requested', analysis_requested_at = ?
		WHERE analysis = 'pending' AND id IN (`+placeholders(len(items))+`)`, args...)
	return err
}

// ApplyAnalysis saves llm's answer for a menu item, and for every other
// item of the same dish with the same input that waits for it. It never
// overwrites what the source gave. found = false: the item is gone (a newer
// revision replaced it).
func (s *Store) ApplyAnalysis(ctx context.Context, a contracts.DishAnalyzed, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	first, err := loadState(ctx, tx, a.MenuItemID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM menu_items WHERE dish_id = ? AND input_hash = ?
		AND (id = ? OR analysis IN ('pending', 'requested', 'timeout')) ORDER BY id`, first.dishID, first.hash, a.MenuItemID)
	if err != nil {
		return false, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	n := a.Nutrition.Clean()
	for _, id := range ids {
		st, err := loadState(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if a.Components != nil && st.sourceComps == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM menu_item_components WHERE menu_item_id = ?`, id); err != nil {
				return false, err
			}
			if err := insertComponents(ctx, tx, id, a.Components, "llm"); err != nil {
				return false, err
			}
		}
		if p := a.TypicalPortionG; p != nil && *p > 0 && (st.weight == nil || st.weightSource == "llm") {
			if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET weight_g = ?, weight_source = 'llm' WHERE id = ?`, *p, id); err != nil {
				return false, err
			}
		}
		if st, err = loadState(ctx, tx, id); err != nil {
			return false, err
		}
		if err := applyRaw(ctx, tx, st); err != nil {
			return false, err
		}
		if st, err = loadState(ctx, tx, id); err != nil {
			return false, err
		}
		if n != nil && st.nutrition != "kitchen" && st.raw == "" {
			args := append(nutritionArgs(n), id)
			if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET `+nutritionSet+`, nutrition_source = 'llm' WHERE id = ?`, args...); err != nil {
				return false, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE menu_items SET analysis = 'done', analysis_done_at = ? WHERE id = ?`, ts(now), id); err != nil {
			return false, err
		}
	}

	if a.AllergensPossible != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE dish_info SET allergens_possible = ? WHERE dish_id = ?`,
			jsonList(food.NormList(a.AllergensPossible)), first.dishID); err != nil {
			return false, err
		}
	}
	if a.Tags != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE dish_info SET tags = ? WHERE dish_id = ?`, jsonList(food.NormList(a.Tags)), first.dishID); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dish_info SET model = ?, analyzed_at = ?,
		status = CASE WHEN allergens_confirmed IS NOT NULL THEN 'confirmed' ELSE 'analyzed' END WHERE dish_id = ?`,
		nullStr(a.Model), ts(now), first.dishID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// TimedOut is a menu item llm did not answer about in time.
type TimedOut struct {
	ItemID int64
	Date   string
	Name   string
}

// TimeoutAnalyses gives up on requests older than before: the items go
// with what they have (nutrition_source stays missing). A late answer is
// still saved by ApplyAnalysis.
func (s *Store) TimeoutAnalyses(ctx context.Context, before time.Time) ([]TimedOut, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mi.id, mi.date, d.name FROM menu_items mi JOIN dishes d ON d.id = mi.dish_id
		WHERE mi.analysis = 'requested' AND mi.analysis_requested_at < ? ORDER BY mi.date, mi.position`, ts(before))
	if err != nil {
		return nil, err
	}
	var out []TimedOut
	for rows.Next() {
		var t TimedOut
		if err := rows.Scan(&t.ItemID, &t.Date, &t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, t := range out {
		if _, err := s.db.ExecContext(ctx, `UPDATE menu_items SET analysis = 'timeout' WHERE id = ? AND analysis = 'requested'`, t.ItemID); err != nil {
			return nil, err
		}
	}
	return out, nil
}
