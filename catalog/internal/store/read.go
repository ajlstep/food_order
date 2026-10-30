package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"food-order/catalog/internal/contracts"
)

// ------------------------------------------------------------ dish views

const viewSelect = `SELECT mi.id, mi.date, d.id, d.name, mi.source, mi.revision, COALESCE(mi.category, ''), mi.price,
	COALESCE(mi.description, ''), COALESCE(mi.image_url, ''), mi.weight_g, COALESCE(mi.weight_text, ''),
	COALESCE(mi.weight_source, ''), ` + nutritionCols + `, mi.nutrition_source, mi.analysis,
	di.allergens_given, di.allergens_possible, di.allergens_confirmed, di.tags, COALESCE(di.status, 'pending')
	FROM menu_items mi JOIN dishes d ON d.id = mi.dish_id LEFT JOIN dish_info di ON di.dish_id = d.id `

// Menu returns the dishes of one day, in the source's order.
func (s *Store) Menu(ctx context.Context, date string) ([]contracts.Dish, error) {
	return s.views(ctx, viewSelect+`WHERE mi.date = ? ORDER BY mi.position, mi.id`, date)
}

// DishesByIDs returns the dishes with the values of the menu of date; with
// date == "" the latest day each dish was on the menu. Unknown ids (or ids
// not on that day's menu) are left out.
func (s *Store) DishesByIDs(ctx context.Context, ids []int64, date string) ([]contracts.Dish, error) {
	if len(ids) == 0 {
		return []contracts.Dish{}, nil
	}
	in := `mi.dish_id IN (` + placeholders(len(ids)) + `)`
	if date != "" {
		return s.views(ctx, viewSelect+`WHERE mi.date = ? AND `+in+` ORDER BY mi.dish_id`, append([]any{date}, int64Args(ids)...)...)
	}
	return s.views(ctx, viewSelect+`WHERE `+in+` AND mi.date = (SELECT MAX(m2.date) FROM menu_items m2 WHERE m2.dish_id = mi.dish_id)
		ORDER BY mi.dish_id`, int64Args(ids)...)
}

func (s *Store) views(ctx context.Context, q string, args ...any) ([]contracts.Dish, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := []contracts.Dish{}
	for rows.Next() {
		var (
			d                                   contracts.Dish
			price, weight                       sql.NullFloat64
			ns                                  nutritionScan
			given, possible, confirmed, tagsCol sql.NullString
		)
		dest := []any{&d.MenuItemID, &d.Date, &d.ID, &d.Name, &d.Source, &d.Revision, &d.Category, &price,
			&d.Description, &d.ImageURL, &weight, &d.WeightText, &d.WeightSource}
		dest = append(dest, ns.dest()...)
		dest = append(dest, &d.NutritionSource, &d.Analysis, &given, &possible, &confirmed, &tagsCol, &d.Status)
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, err
		}
		d.Price, d.WeightG = ptrF(price), ptrF(weight)
		if d.NutritionSource != "missing" {
			d.Nutrition = ns.value()
		}
		d.Allergens.Given = readList(given)
		d.Allergens.Possible = readList(possible)
		if c := readList(confirmed); c != nil {
			d.Allergens.Confirmed = &c
		}
		d.Tags = readList(tagsCol)
		out = append(out, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	itemIDs := make([]int64, len(out))
	for i, d := range out {
		itemIDs[i] = d.MenuItemID
	}
	comps, err := s.components(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Components = comps[out[i].MenuItemID]
	}
	return out, nil
}

// components returns the components of menu items, in insertion order.
func (s *Store) components(ctx context.Context, itemIDs []int64) (map[int64][]contracts.DishComponent, error) {
	out := map[int64][]contracts.DishComponent{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT menu_item_id, name, grams, COALESCE(cooking_method, ''), source
		FROM menu_item_components WHERE menu_item_id IN (`+placeholders(len(itemIDs))+`) ORDER BY id`, int64Args(itemIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id    int64
			c     contracts.DishComponent
			grams sql.NullFloat64
		)
		if err := rows.Scan(&id, &c.Name, &grams, &c.Method, &c.Source); err != nil {
			return nil, err
		}
		c.Grams = ptrF(grams)
		out[id] = append(out[id], c)
	}
	return out, rows.Err()
}

// DishExists says whether a dish id is known.
func (s *Store) DishExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dishes WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// ConfirmAllergens saves a user's confirmation of a dish's allergens (an
// empty list = "no allergens"). The dish is shared, so the confirmation is
// too; who confirmed is kept only as an id.
func (s *Store) ConfirmAllergens(ctx context.Context, dishID int64, allergens []string, userID int64, now time.Time) error {
	if allergens == nil {
		allergens = []string{}
	}
	r, err := s.db.ExecContext(ctx, `UPDATE dish_info SET allergens_confirmed = ?, status = 'confirmed',
		confirmed_by = ?, confirmed_at = ? WHERE dish_id = ?`, jsonList(allergens), userID, ts(now), dishID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------- requests

// SaveRequest remembers a request of core (or starts it again when the
// same request_id comes back): food.menu.ready is due once its days are complete.
func (s *Store) SaveRequest(ctx context.Context, requestID, source, from, to string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO menu_requests (request_id, source, date_from, date_to, status, received_at)
		VALUES (?, ?, ?, ?, 'waiting', ?)
		ON CONFLICT(request_id) DO UPDATE SET source = excluded.source,
			date_from = MIN(date_from, excluded.date_from), date_to = MAX(date_to, excluded.date_to),
			status = 'waiting', dish_ids = NULL, received_at = excluded.received_at, ready_at = NULL, published_at = NULL`,
		requestID, source, from, to, ts(now))
	return err
}

// CompleteRequests marks ready every waiting request whose days have no
// dish still waiting for llm.
func (s *Store) CompleteRequests(ctx context.Context, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT request_id, date_from, date_to FROM menu_requests WHERE status = 'waiting'`)
	if err != nil {
		return err
	}
	type req struct{ id, from, to string }
	var list []req
	for rows.Next() {
		var r req
		if err := rows.Scan(&r.id, &r.from, &r.to); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		var waiting int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_items WHERE date BETWEEN ? AND ?
			AND analysis IN ('pending', 'requested')`, r.from, r.to).Scan(&waiting); err != nil {
			return err
		}
		if waiting > 0 {
			continue
		}
		ids, err := s.dishIDs(ctx, r.from, r.to)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(ids)
		if _, err := s.db.ExecContext(ctx, `UPDATE menu_requests SET status = 'ready', dish_ids = ?, ready_at = ?
			WHERE request_id = ? AND status = 'waiting'`, string(b), ts(now), r.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) dishIDs(ctx context.Context, from, to string) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT dish_id FROM menu_items WHERE date BETWEEN ? AND ? ORDER BY date, position, id`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	seen := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ReadyToPublish returns the ready requests whose food.menu.ready is not out yet.
func (s *Store) ReadyToPublish(ctx context.Context) ([]contracts.MenuReady, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT request_id, date_from, date_to, COALESCE(dish_ids, '[]') FROM menu_requests
		WHERE status = 'ready' AND published_at IS NULL ORDER BY ready_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contracts.MenuReady
	for rows.Next() {
		var (
			m   contracts.MenuReady
			ids string
		)
		if err := rows.Scan(&m.RequestID, &m.From, &m.To, &ids); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(ids), &m.DishIDs); err != nil || m.DishIDs == nil {
			m.DishIDs = []int64{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkPublished says food.menu.ready went out for a request.
func (s *Store) MarkPublished(ctx context.Context, requestID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE menu_requests SET published_at = ? WHERE request_id = ?`, ts(now), requestID)
	return err
}

// RequestStatus returns the status of a request ("" when unknown); for tests and debugging.
func (s *Store) RequestStatus(ctx context.Context, requestID string) (status string, published bool, err error) {
	var pub sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT status, published_at FROM menu_requests WHERE request_id = ?`, requestID).Scan(&status, &pub)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return status, pub.Valid, err
}

// ----------------------------------------------------------- embeddings

// PendingEmbeds returns embedding requests for dishes without an
// embedding. A dish waits until its analysis is over, so the text has the
// components and tags.
func (s *Store) PendingEmbeds(ctx context.Context, limit int) ([]contracts.DishEmbedRequested, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.name,
		(SELECT mi.id FROM menu_items mi WHERE mi.dish_id = d.id AND mi.analysis IN ('none', 'done', 'timeout', 'off')
			ORDER BY mi.date DESC LIMIT 1) AS item_id
		FROM dishes d
		WHERE d.embed_requested_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.dish_id = d.id)
			AND item_id IS NOT NULL
		ORDER BY d.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	type job struct {
		id, item int64
		name     string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.name, &j.item); err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []contracts.DishEmbedRequested
	for _, j := range jobs {
		var (
			desc, category string
			tagsCol        sql.NullString
		)
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(mi.description, ''), COALESCE(mi.category, ''), di.tags
			FROM menu_items mi LEFT JOIN dish_info di ON di.dish_id = mi.dish_id WHERE mi.id = ?`, j.item).
			Scan(&desc, &category, &tagsCol); err != nil {
			return nil, err
		}
		comps, err := s.components(ctx, []int64{j.item})
		if err != nil {
			return nil, err
		}
		out = append(out, contracts.DishEmbedRequested{DishID: j.id, Text: EmbedText(j.name, category, desc, comps[j.item], readList(tagsCol))})
	}
	return out, nil
}

// EmbedText is the text llm turns into the embedding of a dish.
func EmbedText(name, category, description string, comps []contracts.DishComponent, tags []string) string {
	parts := []string{name}
	if category != "" {
		parts = append(parts, "Categorie: "+category)
	}
	if description != "" {
		parts = append(parts, description)
	}
	if len(comps) > 0 {
		names := make([]string, len(comps))
		for i, c := range comps {
			names[i] = c.Name
			if c.Method != "" && c.Method != "other" {
				names[i] += " (" + c.Method + ")"
			}
		}
		parts = append(parts, "Componente: "+strings.Join(names, ", "))
	}
	if len(tags) > 0 {
		parts = append(parts, "Etichete: "+strings.Join(tags, ", "))
	}
	return strings.Join(parts, ". ")
}

// MarkEmbedRequested says the embedding request for a dish is out.
func (s *Store) MarkEmbedRequested(ctx context.Context, dishID int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dishes SET embed_requested_at = ? WHERE id = ?`, ts(now), dishID)
	return err
}

// SaveEmbedding stores (or replaces) the embedding of a dish.
// ErrNotFound: the dish is unknown.
func (s *Store) SaveEmbedding(ctx context.Context, dishID int64, model string, vec []float64, now time.Time) error {
	ok, err := s.DishExists(ctx, dishID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	buf := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(float32(f)))
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embeddings (dish_id, model, vector, dims, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(dish_id) DO UPDATE SET model = excluded.model, vector = excluded.vector, dims = excluded.dims,
			created_at = excluded.created_at`, dishID, model, buf, len(vec), ts(now))
	return err
}

// Embedding is the stored vector of a dish.
type Embedding struct {
	Model  string
	Vector []float32
}

// Embeddings returns the stored vectors of the given dishes (missing ones are left out).
func (s *Store) Embeddings(ctx context.Context, ids []int64) (map[int64]Embedding, error) {
	out := map[int64]Embedding{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT dish_id, COALESCE(model, ''), vector FROM embeddings
		WHERE dish_id IN (`+placeholders(len(ids))+`)`, int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  int64
			e   Embedding
			raw []byte
		)
		if err := rows.Scan(&id, &e.Model, &raw); err != nil {
			return nil, err
		}
		e.Vector = make([]float32, len(raw)/4)
		for i := range e.Vector {
			e.Vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
		}
		out[id] = e
	}
	return out, rows.Err()
}
