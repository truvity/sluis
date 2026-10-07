package dynamodb

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddb "github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/truvity/sluis/internal/port"
)

// iterate calls fn with every live State item whose key has the prefix and sorts
// after `after`, in key order, until fn returns false. hint is how many items
// the caller expects to want, to size a Query's pages.
//
// A prefix that lies in one kind (internal/port/keys.go) is a Query on its
// partition. Any other names no one partition, so it is a Scan of the table,
// sorted here.
func (s *Store) iterate(ctx context.Context, prefix, after string, hint int, fn func(item) bool) error {
	if pk, skPrefix, ok := port.LocatePrefix(prefix); ok {
		return s.query(ctx, pk, skPrefix, prefix, after, hint, fn)
	}
	var all []item
	err := s.scan(ctx, scanFilterState(prefix), func(it item) bool {
		if strings.HasPrefix(it.key, prefix) && it.key > after && !s.dead(it) {
			all = append(all, it)
		}
		return true
	})
	if err != nil {
		return err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].key < all[j].key })
	for _, it := range all {
		if !fn(it) {
			return nil
		}
	}
	return nil
}

func scanFilterState(prefix string) *ddb.ScanInput {
	in := &ddb.ScanInput{ConsistentRead: aws.Bool(true), FilterExpression: aws.String(filterAll)}
	if prefix != "" {
		in.FilterExpression = aws.String(scanState)
		in.ExpressionAttributeValues = map[string]types.AttributeValue{":p": strAttr(prefix)}
	}
	return in
}

func (s *Store) scan(ctx context.Context, in *ddb.ScanInput, fn func(item) bool) error {
	in.TableName = &s.table
	for {
		pctx, cancel := withTimeout(ctx)
		out, err := s.api.Scan(pctx, in)
		cancel()
		if err != nil {
			return unavailable(err)
		}
		for _, m := range out.Items {
			if it, ok := parseItem(m); ok && !fn(it) {
				return nil
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// query pages one partition's items under the prefix, in key order.
func (s *Store) query(ctx context.Context, pk, skPrefix, prefix, after string, hint int, fn func(item) bool) error {
	in := &ddb.QueryInput{
		TableName:        &s.table,
		ConsistentRead:   aws.Bool(true),
		FilterExpression: aws.String(filterAll),
	}
	in.KeyConditionExpression = aws.String(keyCondSK)
	in.ExpressionAttributeValues = map[string]types.AttributeValue{":pk": strAttr(pk), ":p": strAttr(skPrefix)}
	if skPrefix == "" {
		// begins_with takes no empty string: the whole partition is the pk alone.
		in.KeyConditionExpression = aws.String(keyCond)
		in.ExpressionAttributeValues = map[string]types.AttributeValue{":pk": strAttr(pk)}
	}
	if hint > 0 {
		in.Limit = aws.Int32(int32(min(hint, 1000)))
	}
	if after != "" && after >= prefix {
		if _, sk, err := locate(after); err == nil {
			in.ExclusiveStartKey = keyOf(pk, sk)
		}
	}
	return s.queryPages(ctx, in, fn)
}

func (s *Store) queryPages(ctx context.Context, in *ddb.QueryInput, fn func(item) bool) error {
	for {
		pctx, cancel := withTimeout(ctx)
		out, err := s.api.Query(pctx, in)
		cancel()
		if err != nil {
			return unavailable(err)
		}
		for _, m := range out.Items {
			it, ok := parseItem(m)
			if !ok || s.dead(it) {
				continue
			}
			if !fn(it) {
				return nil
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			return nil
		}
		in.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// List implements [port.State].
func (s *Store) List(ctx context.Context, prefix, page string, limit int) (port.Page, error) {
	after, err := port.PageStart(prefix, page)
	if err != nil {
		return port.Page{}, err
	}
	if limit <= 0 {
		limit = port.DefaultPage
	}
	var out port.Page
	err = s.iterate(ctx, prefix, after, limit+1, func(it item) bool {
		if len(out.Records) == limit {
			out.Next = port.PageToken(prefix, out.Records[limit-1].Key)
			return false
		}
		out.Records = append(out.Records, port.Record{Key: it.key, Value: it.value, Revision: it.rev})
		return true
	})
	if err != nil {
		return port.Page{}, err
	}
	return out, nil
}

// ExportState implements [port.StateExporter]: the live records under the
// prefix with what is left of each lifetime, which `expires` holds.
func (s *Store) ExportState(ctx context.Context, prefix string, fn func(port.Exported) error) error {
	var ferr error
	err := s.iterate(ctx, prefix, "", 0, func(it item) bool {
		ferr = fn(port.Exported{Key: it.key, Value: slices.Clone(it.value), TTL: s.remaining(it)})
		return ferr == nil
	})
	if err != nil {
		return err
	}
	return ferr
}
