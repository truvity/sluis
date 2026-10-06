package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// clientsCommand is `sluisctl clients`: what an operator does with the stored
// secret of a client the issuer generates (`secret: {generate: true}`). It
// signs in as the person running it and the issuer decides: the operators
// group, from the token, admits it. Nothing here prints a secret; a rotation
// reaches whoever uses the client through the export the policy declares.
func clientsCommand(args []string) error {
	if len(args) == 0 {
		return badUsage("clients needs rotate, show or purge")
	}
	switch args[0] {
	case "rotate":
		return clientsRotate(args[1:])
	case "show":
		return clientsShow(args[1:])
	case "purge":
		return clientsPurge(args[1:])
	default:
		return badUsage("clients %q is not rotate, show or purge", args[0])
	}
}

// clientsFlags is the command line of one clients subcommand: the client id,
// then flags, in either order.
type clientsFlags struct {
	id, issuer, client string
	overlap            time.Duration
	overlapSet         bool
}

func parseClientsFlags(name string, args []string, withOverlap bool) (clientsFlags, error) {
	var out clientsFlags
	flags := flag.NewFlagSet("clients "+name, flag.ContinueOnError)
	flags.StringVar(&out.issuer, "issuer", "", "the issuer, when not configured")
	flags.StringVar(&out.client, "client", "", "the client to present")
	var overlap string
	if withOverlap {
		flags.StringVar(&overlap, "overlap", "", "how long the replaced secret stays valid (default 24h, at most 168h; 0 cuts it at once)")
	}
	// The id and the flags come in either order: `rotate <id> --overlap 24h`
	// and `rotate --overlap 24h <id>`. Flags are parsed again after each
	// positional argument, so none after the id is dropped.
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return clientsFlags{}, usageError{err}
		}
		if flags.NArg() == 0 {
			break
		}
		if out.id != "" {
			return clientsFlags{}, badUsage("clients %s takes one client id, not %q as well", name, flags.Arg(0))
		}
		out.id, rest = flags.Arg(0), flags.Args()[1:]
	}
	if strings.TrimSpace(out.id) == "" {
		return clientsFlags{}, badUsage("clients %s needs the client id", name)
	}
	if overlap != "" {
		d, err := time.ParseDuration(overlap)
		if err != nil || d < 0 || d > 7*24*time.Hour {
			return clientsFlags{}, badUsage("--overlap is a duration between 0 and 168h, like 24h")
		}
		out.overlap, out.overlapSet = d, true
	}
	return out, nil
}

func clientsRotate(args []string) error {
	f, err := parseClientsFlags("rotate", args, true)
	if err != nil {
		return err
	}
	body := map[string]any{"client": f.id}
	if f.overlapSet {
		body["overlap_seconds"] = int64(f.overlap / time.Second)
	}
	var answer struct {
		Rotated            string `json:"rotated"`
		OverlapSeconds     int64  `json:"overlap_seconds"`
		PreviousValidUntil string `json:"previous_valid_until"`
		DiscardedPrevious  bool   `json:"discarded_previous"`
	}
	if err = clientsCall(f, "rotate", body, &answer); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "rotated the secret of %s at %s\n", f.id, answer.Rotated)
	if answer.PreviousValidUntil == "" {
		_, _ = fmt.Fprintln(stdout, "the old secret stopped working at once")
	} else {
		_, _ = fmt.Fprintf(stdout, "the old secret still works until %s\n", answer.PreviousValidUntil)
	}
	if answer.DiscardedPrevious {
		_, _ = fmt.Fprintln(stdout, "warning: an earlier rotation's overlap was still open; that older secret has stopped working")
	}
	return nil
}

func clientsShow(args []string) error {
	f, err := parseClientsFlags("show", args, false)
	if err != nil {
		return err
	}
	var m struct {
		Exists             bool   `json:"exists"`
		Generated          bool   `json:"generated"`
		Created            string `json:"created"`
		Rotated            string `json:"rotated"`
		HasPrevious        bool   `json:"has_previous"`
		PreviousValidUntil string `json:"previous_valid_until"`
		PreviousActive     bool   `json:"previous_active"`
		Orphaned           string `json:"orphaned"`
	}
	if err = clientsCall(f, "show", map[string]any{"client": f.id}, &m); err != nil {
		return err
	}
	if !m.Exists {
		_, _ = fmt.Fprintf(stdout, "%s has no stored secret (generated in the policy: %t)\n", f.id, m.Generated)
		return nil
	}
	line := func(name, value string) {
		if value == "" {
			value = "-"
		}
		_, _ = fmt.Fprintf(stdout, "%-22s %s\n", name, value)
	}
	line("client", f.id)
	line("generated in policy", fmt.Sprint(m.Generated))
	line("created", m.Created)
	line("rotated", m.Rotated)
	line("has previous", fmt.Sprint(m.HasPrevious))
	line("previous valid until", m.PreviousValidUntil)
	line("previous still valid", fmt.Sprint(m.PreviousActive))
	line("orphaned since", m.Orphaned)
	return nil
}

func clientsPurge(args []string) error {
	f, err := parseClientsFlags("purge", args, false)
	if err != nil {
		return err
	}
	if err = clientsCall(f, "purge", map[string]any{"client": f.id}, &struct{}{}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "deleted the stored secret of %s\n", f.id)
	_, _ = fmt.Fprintf(stdout, "still to do by hand: delete the input secret clients/%s/secret if it is still there, "+
		"and the exported copy at the target the removed export wrote to\n", f.id)
	return nil
}

// clientsCall posts to the issuer's operator endpoint as the signed-in person.
func clientsCall(f clientsFlags, action string, body any, into any) error {
	cfg, err := loadConfig(f.issuer, f.client)
	if err != nil {
		return err
	}
	ctx := context.Background()
	token, err := refresh(ctx, cfg)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.Issuer, "/")+"/.access/client-secrets/"+action, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build the request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %w", errUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()
	said, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	switch response.StatusCode {
	case http.StatusOK:
		if err = json.Unmarshal(said, into); err != nil {
			return fmt.Errorf("parse the issuer's answer: %w", err)
		}
		return nil
	case http.StatusUnauthorized:
		return errNotSignedIn
	case http.StatusForbidden:
		return fmt.Errorf("%w: %s", errNotGranted, strings.TrimSpace(string(said)))
	case http.StatusBadRequest:
		return badUsage("%s", strings.TrimSpace(string(said)))
	case http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusConflict:
		return errors.New(strings.TrimSpace(string(said)))
	default:
		return fmt.Errorf("the issuer answered %s: %s", response.Status, strings.TrimSpace(string(said)))
	}
}
