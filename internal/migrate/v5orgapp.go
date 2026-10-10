package migrate

import (
	"fmt"
	"slices"

	"github.com/truvity/sluis/internal/githubapp/catalogue"
	"github.com/truvity/sluis/internal/githubroster/appid"
	"github.com/truvity/sluis/internal/githubroster/catalogueapp"
)

// An organisation on layout v4 was connected with an App of its own, and its
// credential is that App's key. On layout v5 the key is kept once, under the
// App, and the organisation names the App (`app_ref`). This file decides which
// App each organisation of the source names, and makes the App when the source
// holds none:
//
//   - the configuration's `controllers.github.appRefs` entry, when there is one
//     (an override: a mismatch with the installing App is still refused);
//   - otherwise the source's App (catalogue or runner) that has the App id the
//     organisation is installed by;
//   - otherwise a new catalogue App, with the App's slug as its id, holding the
//     organisation's key. Organisations installed by the same App share it.
//
// The synthesized App is added to the source's catalogue Apps, so that the plan
// shows it as an item of its own, a copy writes it before the organisations
// that name it, and a verify compares its key by value like any other.

// appRefOf is the App the organisation names on layout v5: what its record
// names, or what the planner decided for it.
func (p *planner) appRefOf(doc orgDoc) string {
	if doc.Record.AppRef != "" {
		return doc.Record.AppRef
	}
	if ref, ok := p.appRefs[doc.Record.Org]; ok {
		return ref
	}
	if p.opt.AppRef != nil {
		return p.opt.AppRef(doc.Record.Org)
	}
	return ""
}

// resolveOrgApps decides the App of each organisation of the source and adds
// the Apps it has to make to the source's catalogue Apps.
func (p *planner) resolveOrgApps(all []readKind, appIDs map[string]int64) {
	orgs, catalogueAt := -1, -1
	for i, rk := range all {
		if rk.k.domain != "github" {
			continue
		}
		switch rk.k.name {
		case "organisations":
			orgs = i
		case "catalogue-apps":
			catalogueAt = i
		}
	}
	if orgs < 0 {
		return
	}
	// The source's own Apps that can sign an installation token, by their
	// numeric App id, the first by id winning.
	byNumber := map[int64]string{}
	var ids []string
	numbers := map[string]int64{}
	for _, rk := range all {
		if rk.k.domain != "github" {
			continue
		}
		for _, e := range rk.src {
			switch doc := e.val.(type) {
			case catalogueDoc:
				ids = append(ids, doc.Record.ID)
				numbers[doc.Record.ID] = doc.Record.AppID
			case runnerDoc:
				id := appid.RunnerID(doc.Record.Tier, doc.Record.Org)
				ids = append(ids, id)
				numbers[id] = doc.Record.AppID
			}
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if n := numbers[id]; n != 0 {
			if _, taken := byNumber[n]; !taken {
				byNumber[n] = id
			}
		}
	}

	made := map[string]catalogueDoc{}
	for _, e := range all[orgs].src {
		doc, ok := e.val.(orgDoc)
		if !ok || e.unreadable != "" || doc.Record.AppRef != "" {
			continue
		}
		org := doc.Record.Org
		if p.opt.AppRef != nil {
			if ref := p.opt.AppRef(org); ref != "" {
				p.appRefs[org] = ref
				continue
			}
		}
		if id, ok := byNumber[doc.Record.AppID]; ok && doc.Record.AppID != 0 {
			p.appRefs[org] = id
			continue
		}
		if catalogueAt < 0 {
			p.orgWhy[org] = "the organisation's own App cannot be carried: the catalogue Apps are not read"
			continue
		}
		app, why := ownApp(doc)
		if why != "" {
			p.orgWhy[org] = why
			continue
		}
		id := app.Record.ID
		switch prior, dup := made[id]; {
		case dup && (prior.Record.AppID != app.Record.AppID || !sameSecret([]byte(prior.PrivateKey), []byte(app.PrivateKey))):
			p.orgWhy[org] = fmt.Sprintf("the organisations installed by the App %s hold different keys for it: name the App in controllers.github.appRefs", id)
			continue
		case dup:
		default:
			if n, taken := numbers[id]; taken && n != app.Record.AppID {
				p.orgWhy[org] = fmt.Sprintf("the App id %s is taken by GitHub App %d in the source: name the App in controllers.github.appRefs", id, n)
				continue
			}
			if n, taken := appIDs[id]; taken && n != 0 && n != app.Record.AppID {
				p.orgWhy[org] = fmt.Sprintf("the App id %s is taken by GitHub App %d on the destination: name the App in controllers.github.appRefs", id, n)
				continue
			}
			made[id] = app
		}
		p.appRefs[org] = id
	}
	for _, id := range slices.Sorted(mapKeys(made)) {
		app := made[id]
		it := newItem(id, app).withSecrets(app.PrivateKey)
		all[catalogueAt].src = append(all[catalogueAt].src, entry{id: it.id, canon: it.canon, val: it.val, secrets: it.secrets})
		appIDs[id] = app.Record.AppID
		p.note("the organisation's own App %s (GitHub App %d) is made a catalogue App, with the key of the organisation's credential", id, app.Record.AppID)
	}
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// ownApp is the catalogue App an organisation's own App becomes: its id is the
// App's slug, and its record and key are the organisation's. why says what stops
// it.
func ownApp(doc orgDoc) (catalogueDoc, string) {
	r := doc.Record
	id := r.AppSlug
	switch {
	case r.AppID == 0 || id == "":
		return catalogueDoc{}, "the organisation's record names no App id and slug"
	case !catalogue.ValidID(id):
		return catalogueDoc{}, fmt.Sprintf("the App slug %q cannot be an App id (lower case, at most 32 characters): name the App in controllers.github.appRefs", id)
	}
	if err := appid.CheckCatalogueID(id); err != nil {
		return catalogueDoc{}, err.Error() + ": name the App in controllers.github.appRefs"
	}
	if doc.Credential.PrivateKey == "" {
		return catalogueDoc{}, "the organisation's credential holds no key"
	}
	return catalogueDoc{
		Record: catalogueapp.Record{
			Version: catalogueapp.Version, ID: id, Org: r.Org, AppID: r.AppID, AppSlug: r.AppSlug, Purpose: appid.Catalogue,
			InstallationID: r.InstallationID, HTMLURL: r.HTMLURL, ConnectedAt: r.ConnectedAt, ConnectedBy: r.ConnectedBy,
		},
		PrivateKey: doc.Credential.PrivateKey,
	}, ""
}
