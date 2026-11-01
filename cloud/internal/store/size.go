package store

// SizeReport is the size of the database and of each of its tables, aggregate numbers only. The
// janitor logs it once a day to show what the database grows with.
type SizeReport struct {
	Bytes      int64            // the database (sqlite: page_count * page_size; postgres: pg_database_size)
	FreeBytes  int64            // sqlite: unused pages inside the file, reused before it grows; postgres: 0
	WALBytes   int64            // sqlite: the -wal file (commits not yet copied back; reused, not shrunk); postgres: 0
	Rows       map[string]int64 // rows per table
	TableBytes map[string]int64 // bytes per table with its indexes; nil when the database cannot tell
}
