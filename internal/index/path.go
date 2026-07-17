package index

// Path-aware query support (Everything-style semantics).
//
// A query that contains a path separator is matched case-insensitively
// against the full path (parent dir + separator + name); queries without
// a separator keep the existing name-only fast path untouched. The
// implementation exploits the interned parent-dir table: a per-query dir
// prematch marks whole subtrees whose dir path contains the query, and
// boundary-spanning occurrences (the query straddling the dir/name join)
// are checked per entry against the name blob. Implementation follows in
// this file.
