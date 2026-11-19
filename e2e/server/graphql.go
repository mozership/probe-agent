package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/graphql-go/graphql"
)

// The GraphQL API of notes, at /graphql, which answers introspection and
// takes the key of the HTTP API in X-Api-Key. Its notes are its own, apart
// from those of the HTTP API and of the gRPC service.

// graphqlNotes keeps the notes of the GraphQL API.
type graphqlNotes struct {
	mu    sync.Mutex
	notes []note
}

// schema returns the schema of the notes of n.
func (n *graphqlNotes) schema() (graphql.Schema, error) {
	noteType := graphql.NewObject(graphql.ObjectConfig{
		Name:        "Note",
		Description: "A note.",
		Fields: graphql.Fields{
			"id":   &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
			"text": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})
	query := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"note": &graphql.Field{
				Type:        noteType,
				Description: "A note by its id, or null when there is none.",
				Args:        graphql.FieldConfigArgument{"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.ID)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					n.mu.Lock()
					defer n.mu.Unlock()
					for _, x := range n.notes {
						if x.ID == p.Args["id"] {
							return x, nil
						}
					}
					return nil, nil
				},
			},
			"notes": &graphql.Field{
				Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(noteType))),
				Description: "Every note, the oldest first.",
				Resolve: func(graphql.ResolveParams) (any, error) {
					n.mu.Lock()
					defer n.mu.Unlock()
					return append([]note{}, n.notes...), nil
				},
			},
		},
	})
	mutation := graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation",
		Fields: graphql.Fields{
			"createNote": &graphql.Field{
				Type:        graphql.NewNonNull(noteType),
				Description: "Writes a note.",
				Args:        graphql.FieldConfigArgument{"text": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					text := strings.TrimSpace(p.Args["text"].(string))
					if text == "" {
						return nil, errText
					}
					n.mu.Lock()
					defer n.mu.Unlock()
					x := note{ID: "q" + strconv.Itoa(len(n.notes)+1), Text: text}
					n.notes = append(n.notes, x)
					return x, nil
				},
			},
		},
	})
	return graphql.NewSchema(graphql.SchemaConfig{Query: query, Mutation: mutation})
}

// errText is the error of a note without a text.
var errText = gqlError("text is required")

type gqlError string

func (e gqlError) Error() string { return string(e) }

// handle serves the API at /graphql.
func (n *graphqlNotes) handle(mux *http.ServeMux) {
	schema, err := n.schema()
	if err != nil {
		panic(err)
	}
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != apiKey {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"errors": []any{map[string]any{"message": "a valid X-Api-Key is required"}}})
			return
		}
		var in struct {
			Query         string         `json:"query"`
			Variables     map[string]any `json:"variables"`
			OperationName string         `json:"operationName"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errors": []any{map[string]any{"message": "the body must be JSON with a query"}}})
			return
		}
		writeJSON(w, http.StatusOK, graphql.Do(graphql.Params{
			Schema: schema, RequestString: in.Query, VariableValues: in.Variables, OperationName: in.OperationName, Context: r.Context(),
		}))
	})
}
