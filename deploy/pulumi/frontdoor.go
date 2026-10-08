package sluispulumi

import "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

// FrontDoor is what a front door needs of the installation: the HTTP API to put
// a domain in front of. An edge module (edge/cloudflare, and later edge/aws)
// takes it, so that the library never imports an edge and an edge imports only
// this.
type FrontDoor struct {
	// Name is the Lambda component's name. An edge module uses it to alias the
	// resources an older release created under the Lambda (the custom domain and
	// its mapping), so that a stack moves to the edge without replacing them.
	Name string
	// Component is the Lambda component itself: the parent of those aliases.
	// Nil is allowed (a stack that never had the domain in the library).
	Component pulumi.Resource
	// APIID and StageName are the HTTP API and its stage, which a domain maps to.
	APIID, StageName pulumi.StringInput
}

// FrontDoor is the installation's HTTP API, as a front door takes it.
func (l *Lambda) FrontDoor() *FrontDoor {
	return &FrontDoor{Name: l.name, Component: l, APIID: l.APIID, StageName: l.APIStageName}
}
