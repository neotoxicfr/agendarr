package store

import (
	"context"
	"database/sql"
	"errors"
)

// Instance kinds.
const (
	KindRadarr = "radarr"
	KindSonarr = "sonarr"
	KindSeerr  = "seerr"
)

// Instance is a configured Radarr, Sonarr or Seerr.
type Instance struct {
	ID        int64
	Kind      string
	Name      string
	URL       string
	PublicURL string // optional: what a browser opens, for admin links
	APIKey    string // sealed
	Enabled   bool
	Position  int
}

const instCols = `id, kind, name, url, api_key, enabled, position, public_url`

func scanInstance(row interface{ Scan(...any) error }) (Instance, error) {
	var i Instance
	err := row.Scan(&i.ID, &i.Kind, &i.Name, &i.URL, &i.APIKey, &i.Enabled, &i.Position, &i.PublicURL)
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// Instances lists instances, by kind then position.
func (s *Store) Instances(ctx context.Context) ([]Instance, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+instCols+` FROM instances
		ORDER BY CASE kind WHEN 'radarr' THEN 0 WHEN 'sonarr' THEN 1 ELSE 2 END, position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		i, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Instance returns one instance.
func (s *Store) Instance(ctx context.Context, id int64) (Instance, error) {
	return scanInstance(s.DB.QueryRowContext(ctx, `SELECT `+instCols+` FROM instances WHERE id = ?`, id))
}

// SaveInstance inserts (ID == 0) or updates an instance and returns its id.
func (s *Store) SaveInstance(ctx context.Context, i Instance) (int64, error) {
	if i.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO instances (kind, name, url, api_key, enabled, public_url, position)
			VALUES (?, ?, ?, ?, ?, ?, (SELECT coalesce(max(position), 0) + 1 FROM instances WHERE kind = ?))`,
			i.Kind, i.Name, i.URL, i.APIKey, i.Enabled, i.PublicURL, i.Kind)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE instances SET name = ?, url = ?, api_key = ?, enabled = ?, public_url = ? WHERE id = ?`,
		i.Name, i.URL, i.APIKey, i.Enabled, i.PublicURL, i.ID)
	return i.ID, err
}

// DeleteInstance removes an instance.
func (s *Store) DeleteInstance(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM instances WHERE id = ?`, id)
	return err
}
