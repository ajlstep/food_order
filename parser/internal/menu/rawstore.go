package menu

import (
	"os"
	"path/filepath"
)

// RawStore keeps the raw page/JSON of the last successful run, so there is
// something to look at when the site changes. Empty Dir disables it.
type RawStore struct{ Dir string }

// Save writes data to Dir/name atomically (temp file + rename).
func (r RawStore) Save(name string, data []byte) error {
	if r.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.Dir, name+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(r.Dir, name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
