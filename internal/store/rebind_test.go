package store

import "testing"

func TestRebind(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SELECT * FROM people WHERE household_id = ? AND id = ?", "SELECT * FROM people WHERE household_id = $1 AND id = $2"},
		{"UPDATE x SET a = ?1 WHERE b = ?2 AND c = ?1", "UPDATE x SET a = $1 WHERE b = $2 AND c = $1"},
		{"SELECT '?', \"a?b\", 'it''s ?' FROM t WHERE x = ?", "SELECT '?', \"a?b\", 'it''s ?' FROM t WHERE x = $1"},
		{"-- why? because\nSELECT ? -- trailing ?\n, ?", "-- why? because\nSELECT $1 -- trailing ?\n, $2"},
		{"SELECT 1", "SELECT 1"},
	} {
		if got := rebind(tc.in); got != tc.want {
			t.Errorf("rebind(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}
