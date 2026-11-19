package scenario

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mozership/probe-agent/internal/llm"
)

// fakeDB answers each statement with the next of answers, and records the
// statements and their params.
type fakeDB struct {
	tables    []DBTable
	tablesErr error
	answers   []DBResult
	queries   []string
	params    [][]any
}

func (f *fakeDB) target() *DB {
	return &DB{Name: "postgres://reader:***@db.test/shop", Driver: "postgres", Query: f.query, Tables: f.listTables}
}

func (f *fakeDB) query(_ context.Context, q string, params []any) (DBResult, error) {
	f.queries = append(f.queries, q)
	f.params = append(f.params, params)
	if len(f.answers) == 0 {
		return DBResult{}, errors.New("refused: the statement INSERT may write, and the run is read-only")
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func (f *fakeDB) listTables(context.Context) ([]DBTable, error) {
	return f.tables, f.tablesErr
}

func TestRun_ReadsADatabase(t *testing.T) {
	db := &fakeDB{
		tables: []DBTable{
			{Name: "public.orders", Columns: [][2]string{{"id", "integer"}, {"email", "text"}, {"status", "text"}}},
			{Name: "public.items", Columns: [][2]string{{"id", "integer"}}},
		},
		answers: []DBResult{
			{Rows: []map[string]any{{"id": 7, "email": "alice@example.com", "status": "paid", "note": nil}}},
			{Error: `pq: relation "order" does not exist`},
		},
	}
	m := &fakeModel{scripts: [][]llm.Reply{
		{
			toolCall("1", "db_tables", map[string]any{}),
			toolCall("2", "db_query", map[string]any{"query": "SELECT * FROM orders WHERE email = $1", "params": []any{"{secret:EMAIL}"}}),
			toolCall("3", "db_query", map[string]any{"query": "SELECT * FROM \"order\""}),
			toolCall("4", "db_query", map[string]any{"query": "INSERT INTO orders VALUES (1)"}),
			toolCall("5", "finish", map[string]any{"status": "done", "summary": "Checked."}),
		},
		{toolCall("j1", "verdict", map[string]any{"verdict": "pass", "reason": "r", "evidence": []string{`"status":"paid"`, "public.orders (id integer, email text, status text)", "the tables public.orders, public.items"}})},
	}}
	o, err := Run(t.Context(), apiConfig(), Targets{DB: db.target()}, m)
	if err != nil {
		t.Fatal(err)
	}
	if o.Verdict != Pass {
		t.Fatalf("verdict %s: %s", o.Verdict, o.Reason)
	}
	for _, e := range o.Evidence {
		if !e.Verified {
			t.Errorf("evidence %q is not verified", e.Quote)
		}
	}
	if len(db.queries) != 3 || !reflect.DeepEqual(db.params[0], []any{"alice@example.com"}) {
		t.Fatalf("queries %q %v", db.queries, db.params)
	}

	tester := m.chats[0]
	if !strings.Contains(tester.opts.System, "The PostgreSQL database postgres://reader:***@db.test/shop, which you read through the tools that start with db_") {
		t.Errorf("system prompt:\n%s", tester.opts.System)
	}
	if !strings.HasSuffix(tester.opts.Prompt, "The database postgres://reader:***@db.test/shop holds the tables public.orders, public.items.") || !strings.Contains(tester.opts.Prompt, "seen in the database") {
		t.Errorf("prompt:\n%s", tester.opts.Prompt)
	}
	if got := tester.turns[1].Results[0].Text; got != "public.orders (id integer, email text, status text)\npublic.items (id integer)" {
		t.Errorf("tables %q", got)
	}
	if got := tester.turns[2].Results[0].Text; got != `1 row:`+"\n"+`{"email":"{secret:EMAIL}","id":7,"note":null,"status":"paid"}` {
		t.Errorf("rows %q", got)
	}
	if got := tester.turns[3].Results[0]; got.IsError || got.Text != `The database answered with an error: pq: relation "order" does not exist` {
		t.Errorf("an error of SQL %+v", got)
	}
	if got := tester.turns[4].Results[0]; !got.IsError || !strings.Contains(got.Text, "the run is read-only") {
		t.Errorf("a write %+v", got)
	}
	if o.Steps[1].Target != "db" || o.Steps[1].URL != "postgres://reader:***@db.test/shop" || o.Steps[1].Input["params"].([]any)[0] != "{secret:EMAIL}" {
		t.Errorf("steps %+v", o.Steps)
	}
	state := TargetState{Kind: "db", URL: "postgres://reader:***@db.test/shop", Query: `SELECT * FROM "order"`, Response: `The database answered with an error: pq: relation "order" does not exist`}
	if len(o.Targets) != 1 || o.Targets[0] != state {
		t.Errorf("targets %+v", o.Targets)
	}
	if judge := m.chats[1].opts.Prompt; !strings.Contains(judge, "The last statement the tester ran on the database postgres://reader:***@db.test/shop, and its answer:\n<last_query>\nSELECT * FROM \"order\"\n\nThe database answered") {
		t.Errorf("judge prompt:\n%s", judge)
	}
}

func TestDB_TablesThatCannotBeRead(t *testing.T) {
	db := &fakeDB{tablesErr: errors.New("connection refused")}
	d := newDBDriver(newLedger(config()), *db.target())
	if err := d.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := d.Opening(); got != "The tables of the database postgres://reader:***@db.test/shop could not be read: connection refused." {
		t.Errorf("opening %q", got)
	}
	res, _ := d.Do(t.Context(), llm.Call{ID: "1", Name: "db_tables", Input: []byte(`{}`)})
	if !res.IsError || res.Text != "Error: the tables could not be read: connection refused" {
		t.Errorf("tables %+v", res)
	}
	// The database comes up, with no table.
	db.tablesErr = nil
	res, _ = d.Do(t.Context(), llm.Call{ID: "2", Name: "db_tables", Input: []byte(`{}`)})
	if res.IsError || res.Text != "The database holds no table." {
		t.Errorf("tables %+v", res)
	}
	res, _ = d.Do(t.Context(), llm.Call{ID: "3", Name: "db_query", Input: []byte(`{"query":"  "}`)})
	if !res.IsError || !strings.Contains(res.Text, "needs a query") {
		t.Errorf("query %+v", res)
	}
	if o := d.Final(t.Context(), false); o.sections[0].text != "The tester ran no statement." || o.state.Query != "" {
		t.Errorf("final %+v", o)
	}
}

func TestDB_MasksASecretInTheRows(t *testing.T) {
	db := &fakeDB{answers: []DBResult{{Rows: []map[string]any{{"note": `Tom&Jerry<1> "x"`, "nested": []any{"Tom&Jerry<1>"}}}}}}
	cfg := config()
	cfg.Secrets["NOTE"] = "Tom&Jerry<1>"
	d := newDBDriver(newLedger(cfg), *db.target())
	res, _ := d.Do(t.Context(), llm.Call{ID: "1", Name: "db_query", Input: []byte(`{"query":"SELECT note FROM t WHERE a = ? AND b = ?","params":[["{secret:NOTE}"],7]}`)})
	if res.IsError || strings.Contains(res.Text, "Tom") || res.Text != `1 row:`+"\n"+`{"nested":["{secret:NOTE}"],"note":"{secret:NOTE} \"x\""}` {
		t.Errorf("rows %q", res.Text)
	}
	// A secret in a param, however deep, is filled in.
	if !reflect.DeepEqual(db.params[0], []any{[]any{"Tom&Jerry<1>"}, 7.0}) {
		t.Errorf("params %#v", db.params[0])
	}
}

func TestDBText(t *testing.T) {
	if got := dbText(DBResult{}); got != "No rows." {
		t.Errorf("no rows %q", got)
	}
	var rows []map[string]any
	for i := range maxDBRows + 3 {
		rows = append(rows, map[string]any{"n": i})
	}
	got := dbText(DBResult{Rows: rows})
	if !strings.HasPrefix(got, fmt.Sprintf("%d rows:\n{\"n\":0}", maxDBRows+3)) || !strings.HasSuffix(got, fmt.Sprintf("{\"n\":%d}\n(3 more rows are not shown; give the query a LIMIT, or narrow it.)", maxDBRows-1)) {
		t.Errorf("rows %q", got)
	}
	if dialect("mysql") != "MySQL" || dialect("sqlite") != "SQLite" || dialect("x") != "x" {
		t.Error("dialect")
	}
}
