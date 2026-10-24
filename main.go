// Command probe-agent serves a Probe action that tests a web application
// from a task written in natural language: a model uses the application through a
// browser, and another call of the model judges whether the expectation
// held. Probe starts it for a step that uses
// github.com/mozership/probe-agent@<commit>.
package main

import "github.com/mozership/probe-agent/internal/action"

// version is set at build time.
var version = "dev"

func main() {
	action.Serve()
}
