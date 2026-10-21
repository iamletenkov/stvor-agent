// Package portal is the client of the Stvor robot API /robot-api/v1/labels
// (spec §5.4, docs/reference/portal-labels-api.md).
package portal

// Labels are the desired managed labels the portal assigns to the machine.
type Labels struct {
	Env   string
	Space string
}

// Expected is a 200 answer of the portal: the labels and their revision
// (16 hex digits; the ETag is `"<revision>"`).
type Expected struct {
	Labels
	Revision string
}
