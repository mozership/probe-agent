package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mozership/probe-agent/internal/llm"
)

// DB is a database, which the tester reads to check what a move on another
// target did, and cannot write to.
type DB struct {
	// Name is the database as the tester and the record see it, without
	// its password.
	Name string
	// Driver is postgres, mysql or sqlite, which tells the dialect of SQL.
	Driver string
	// Query runs a statement that reads, with params bound to its
	// placeholders. An answer of the database, as an error of SQL, is in
	// the result; a statement that gets no answer, as one the guard
	// refuses, is an error.
	Query func(ctx context.Context, query string, params []any) (DBResult, error)
	// Tables lists the tables of the database and their columns.
	Tables func(ctx context.Context) ([]DBTable, error)
}

// DBResult is the answer to a statement: its rows, or the error of the
// database.
type DBResult struct {
	Rows  []map[string]any
	Error string
}

// DBTable is a table and its columns, each as its name and its type.
type DBTable struct {
	Name    string
	Columns [][2]string
}

// maxDBRows is how many rows of a result the tester is shown.
const maxDBRows = 50

// dbDriver is a database as a target.
type dbDriver struct {
	*ledger
	db DB
	// shownName is the database as the model and the record see it.
	shownName string
	// tables is what the database held as the test started, or nil, with
	// why in tablesFailed.
	tables       []DBTable
	tablesFailed string
	// last is the last statement run, and lastText what it answered, for
	// the judge.
	last, lastText string
}

var _ Driver = (*dbDriver)(nil)

func newDBDriver(l *ledger, db DB) *dbDriver {
	return &dbDriver{ledger: l, db: db, shownName: l.secrets.mask(db.Name)}
}

// Kind is db.
func (d *dbDriver) Kind() string { return "db" }

// Open reads the tables of the database, for the tester to start from. A
// database whose tables cannot be read is told to the tester, who can
// query it all the same.
func (d *dbDriver) Open(ctx context.Context) error {
	d.readTables(ctx)
	d.seen(d.Opening())
	return nil
}

func (d *dbDriver) readTables(ctx context.Context) {
	tables, err := d.db.Tables(ctx)
	if err != nil {
		d.tablesFailed = d.secrets.mask(err.Error())
		return
	}
	d.tables, d.tablesFailed = tables, ""
}

// Offer offers the tools of the database.
func (d *dbDriver) Offer(opts *llm.ChatOptions) {
	opts.Tools = append(opts.Tools, dbTools...)
}

// Handles says whether call is of a tool of the database.
func (d *dbDriver) Handles(call llm.Call) bool {
	return call.Toolset == "" && slices.ContainsFunc(dbTools, func(t llm.Tool) bool { return t.Name == call.Name })
}

// Brief tells the tester of the database.
func (d *dbDriver) Brief() string {
	return `The ` + dialect(d.db.Driver) + ` database ` + d.shownName + `, which you read through the tools that start with db_, to check what your moves on the other targets did. You cannot write to it:
- db_tables lists its tables and their columns.
- db_query runs one statement that reads, as SELECT, with no semicolon, and shows its rows, each as a JSON object. Give LIMIT to a query of a large table; only the first ` + fmt.Sprint(maxDBRows) + ` rows are shown.
- Give each column a name of its own, as SELECT o.id AS order_id, u.id AS user_id: two of one name come back as one. Cast an integer above 2^53 and an exact time to text, and read a binary column as hex, as they do not come back as they are.`
}

// dialect names the SQL of driver.
func dialect(driver string) string {
	switch driver {
	case "postgres":
		return "PostgreSQL"
	case "mysql":
		return "MySQL"
	case "sqlite":
		return "SQLite"
	}
	return driver
}

// Opening says what tables the database holds.
func (d *dbDriver) Opening() string {
	if d.tables == nil && d.tablesFailed != "" {
		return "The tables of the database " + d.shownName + " could not be read: " + d.tablesFailed + "."
	}
	if len(d.tables) == 0 {
		return "The database " + d.shownName + " holds no table."
	}
	var names []string
	for _, t := range d.tables {
		names = append(names, t.Name)
	}
	return d.secrets.mask("The database " + d.shownName + " holds the tables " + strings.Join(names, ", ") + ".")
}

// dbInput is the input of any tool of the database.
type dbInput struct {
	Query  string `json:"query"`
	Params []any  `json:"params"`
}

// Do runs a call of a tool of the database.
func (d *dbDriver) Do(ctx context.Context, call llm.Call) (llm.Result, Step) {
	step := Step{Tool: call.Name, Target: d.Kind(), URL: d.shownName}
	var raw map[string]any
	if json.Unmarshal(call.Input, &raw) == nil && len(raw) > 0 {
		step.Input, _ = d.secrets.maskAny(raw).(map[string]any)
	}
	res := llm.Result{CallID: call.ID}
	var in dbInput
	text, err := "", json.Unmarshal(call.Input, &in)
	if err == nil {
		switch call.Name {
		case "db_tables":
			text, err = d.tablesText(ctx)
		case "db_query":
			text, err = d.query(ctx, in)
		default:
			err = fmt.Errorf("there is no tool %s", call.Name)
		}
	}
	if err != nil {
		res.Text = "Error: " + d.secrets.mask(err.Error())
		res.IsError = true
		step.Error = res.Text
		return res, step
	}
	text = truncate(d.secrets.mask(text), maxText)
	d.seen(text)
	res.Text = text
	step.Result = summary(res.Text)
	return res, step
}

// tablesText tells the tables and their columns, read again, as a table
// may have come since the test started.
func (d *dbDriver) tablesText(ctx context.Context) (string, error) {
	d.readTables(ctx)
	if d.tablesFailed != "" {
		return "", fmt.Errorf("the tables could not be read: %s", d.tablesFailed)
	}
	if len(d.tables) == 0 {
		return "The database holds no table.", nil
	}
	var b strings.Builder
	for i, t := range d.tables {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(t.Name + " (")
		for j, c := range t.Columns {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strings.TrimSpace(c[0] + " " + c[1]))
		}
		b.WriteString(")")
	}
	return b.String(), nil
}

// query runs a statement of the tester, with the secrets filled in.
func (d *dbDriver) query(ctx context.Context, in dbInput) (string, error) {
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return "", fmt.Errorf("db_query needs a query")
	}
	filled, err := d.secrets.fill(q)
	if err != nil {
		return "", err
	}
	params := make([]any, len(in.Params))
	for i, p := range in.Params {
		if params[i], err = d.fillAny(p); err != nil {
			return "", err
		}
	}
	res, err := d.db.Query(ctx, filled, params)
	if err != nil {
		return "", err
	}
	// The rows are masked before they are written as JSON, which escapes
	// what a secret may hold, as & or a quote.
	for i, row := range res.Rows {
		res.Rows[i], _ = d.secrets.maskAny(row).(map[string]any)
	}
	text := dbText(res)
	d.last, d.lastText = d.secrets.mask(q), d.secrets.mask(text)
	return text, nil
}

// fillAny fills the secrets in the strings of v, however deep.
func (d *dbDriver) fillAny(v any) (any, error) {
	switch v := v.(type) {
	case string:
		return d.secrets.fill(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			f, err := d.fillAny(e)
			if err != nil {
				return nil, err
			}
			out[i] = f
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			f, err := d.fillAny(e)
			if err != nil {
				return nil, err
			}
			out[k] = f
		}
		return out, nil
	}
	return v, nil
}

// dbText tells the answer to a statement: its rows, each as a JSON object,
// or the error of the database.
func dbText(r DBResult) string {
	if r.Error != "" {
		return "The database answered with an error: " + r.Error
	}
	if len(r.Rows) == 0 {
		return "No rows."
	}
	var b strings.Builder
	if len(r.Rows) == 1 {
		b.WriteString("1 row:")
	} else {
		fmt.Fprintf(&b, "%d rows:", len(r.Rows))
	}
	for i, row := range r.Rows {
		if i == maxDBRows {
			fmt.Fprintf(&b, "\n(%d more rows are not shown; give the query a LIMIT, or narrow it.)", len(r.Rows)-maxDBRows)
			break
		}
		var line strings.Builder
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(row); err != nil {
			line.Reset()
			_, _ = fmt.Fprint(&line, row)
		}
		b.WriteString("\n" + strings.TrimSuffix(line.String(), "\n"))
	}
	return b.String()
}

// Final is the last statement and its answer. Nothing is read again.
func (d *dbDriver) Final(context.Context, bool) observation {
	o := observation{
		heading: "The last statement the tester ran on the database " + d.shownName + ", and its answer:",
		state:   TargetState{Kind: d.Kind(), URL: d.shownName},
	}
	text := "The tester ran no statement."
	if d.last != "" {
		text = truncate(d.last+"\n\n"+d.lastText, 20000)
		o.state.Query = d.last
		o.state.Response = truncate(d.lastText, 20000)
	}
	o.sections = []section{{"last_query", text}}
	return o
}

var dbTools = []llm.Tool{
	{
		Name:        "db_tables",
		Description: "List the tables of the database and their columns, with their types.",
		Properties:  map[string]any{},
	},
	{
		Name:        "db_query",
		Description: "Run one statement that reads the database, as SELECT, and read its rows, each as a JSON object, or the error of the database.",
		Properties: map[string]any{
			"query": map[string]any{"type": "string", "description": "The statement, with no semicolon, such as SELECT status FROM orders WHERE id = ? (or $1 for PostgreSQL)."},
			"params": map[string]any{
				"type":        "array",
				"items":       map[string]any{},
				"description": "Values for the placeholders of the statement, in order.",
			},
		},
		Required: []string{"query"},
	},
}
