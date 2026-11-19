package main

import (
	"database/sql"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// The subscriptions of the newsletter are kept in a SQLite database as
// well, at the path DB_PATH names, for a workflow to read what the pages
// did; see the job Database of e2e/workflow.yml. Without DB_PATH, as in the
// tests of the server, the database is in memory.

// subscriptionsSchema is the table of the subscriptions.
const subscriptionsSchema = `CREATE TABLE IF NOT EXISTS subscriptions (
  email TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  subscribed_at TEXT NOT NULL,
  confirmed_at TEXT
)`

// openStore opens the database of the subscriptions, and makes its table.
func openStore() *sql.DB {
	path := os.Getenv("DB_PATH")
	dsn := path
	if path == "" {
		dsn = "file::memory:"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	// One connection, so that a database in memory is the same one for
	// every statement, and writes do not wait on each other.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{"PRAGMA journal_mode = WAL", "PRAGMA busy_timeout = 5000", subscriptionsSchema} {
		if _, err := db.Exec(stmt); err != nil {
			log.Fatalf("the database of the subscriptions %s: %v", path, err)
		}
	}
	return db
}

// now is the time the store writes, to the second, in UTC.
func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
