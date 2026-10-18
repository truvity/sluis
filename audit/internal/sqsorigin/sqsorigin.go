// Package sqsorigin stamps a record that arrived on a queue with what the queue
// knows about it, and nothing the sender wrote.
//
// SQS delivers the body its sender chose. A stamp inside it (observer,
// recorded_at, origin_hash) is therefore a claim, and a writer that kept it
// would let any principal allowed to send record under another's name and at
// another's time. Two things about a message are not the sender's to choose,
// because SQS sets them: the SenderId (the unique id of the IAM principal, and
// for a role the session) and the SentTimestamp. This stamps from those.
package sqsorigin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/truvity/sluis/audit/sdk/record"
)

// The system attributes SQS sets on a message.
const (
	SenderID      = "SenderId"
	SentTimestamp = "SentTimestamp"
)

// Origin makes the stamp of a record that arrived on a queue.
type Origin struct {
	// Senders names the principals allowed to send, by the unique id SQS
	// reports (AROA... for a role, AIDA... for a user), as the observer the
	// trail should name. A SenderId is matched whole first, then by what is
	// before the first colon, which is the id of the role without the session.
	// A sender not named is stamped with no observer id: the writer's own
	// attribution, as before.
	Senders map[string]string
	// Version and Instance name this writer on the records it stamps.
	Version  string
	Instance string
	// Now is the clock for a message with no SentTimestamp. Default time.Now.
	Now func() time.Time
}

// Stamp replaces whatever stamp r carries with one made from the message's
// system attributes: the observer the sender maps to, and the time SQS took the
// message as recorded_at.
func (o *Origin) Stamp(r *record.Record, attrs map[string]string) error {
	at := o.now()
	if ms, err := strconv.ParseInt(attrs[SentTimestamp], 10, 64); err == nil && ms > 0 {
		at = time.UnixMilli(ms)
	}
	return record.Stamp(r, &record.Observer{Id: o.observer(attrs[SenderID]), Version: o.Version, Instance: o.Instance}, at)
}

func (o *Origin) observer(sender string) string {
	if sender == "" {
		return ""
	}
	if name, ok := o.Senders[sender]; ok {
		return name
	}
	if id, _, found := strings.Cut(sender, ":"); found {
		return o.Senders[id]
	}
	return ""
}

func (o *Origin) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

type trustKey struct{}

// Trust marks a context as carrying records that [Origin.Stamp] has stamped, so
// that the writer keeps their stamps. Only code that has stamped every record of
// the batch itself may call it.
func Trust(ctx context.Context) context.Context { return context.WithValue(ctx, trustKey{}, true) }

// Trusted reports whether [Trust] marked the context.
func Trusted(ctx context.Context) bool {
	ok, _ := ctx.Value(trustKey{}).(bool)
	return ok
}
