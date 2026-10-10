package sluispulumi

import (
	"errors"
	"fmt"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// PortsArgs is what the `ports:` block of the processes' configuration needs of
// the infrastructure: where the blobs, optionally the sealed credentials and, on DynamoDB,
// the State are kept. Every name is an input of the components, so none waits
// for a resource to exist.
type PortsArgs struct {
	// Adapter is the State adapter. Default: "dynamodb" when TableName is set,
	// and otherwise none is written (`legacy`, the schema's default). Only
	// "dynamodb" is rendered here; the NATS adapter's block is the identity
	// stack's and is merged in by the caller.
	Adapter string

	// Region is the region of the bucket, the key and the table. Optional:
	// empty is the SDK's own resolution (AWS_REGION), which Pod Identity sets.
	Region string

	// BucketName is the blob bucket (Storage's BucketName). Required.
	BucketName string
	// BlobPrefix is a key prefix inside the bucket, for an installation that
	// shares it. Optional.
	BlobPrefix string

	// KeyID is ignored: sealing is retired and `ports.sealer` is refused, so
	// nothing renders it. The field stays until the library drops its sealer key.
	KeyID string

	// TableName is the State table (State's TableName). Required with the
	// "dynamodb" adapter.
	TableName string

	// Tables are the per-module tables (States.Grant() names them; ModuleSet.TableName
	// is the default): `ports.dynamodb.tables`, layout v5. Exclusive with TableName,
	// and it needs `secrets.layout: v5` in the document. The block is only valid
	// with that layout, so render it when the layout is switched, not before.
	Tables map[Module]string
}

// RenderPorts renders the `ports:` block as a map: the value under the `ports`
// key of the service document, which the controllers it runs share (schemas/config/*.schema.json).
//
//	ports:
//	  adapter: dynamodb
//	  dynamodb: {table: ..., region: ...}
//	  blob: {adapter: s3, s3: {bucket: ..., region: ...}}
//
// `create` is never rendered: the table is the infrastructure's, and the
// adapter then binds to it and checks it with DescribeTable. Credentials are
// the platform's and are never configured.
func RenderPorts(p PortsArgs) (map[string]any, error) {
	var errs []error
	if p.BucketName == "" {
		errs = append(errs, errors.New("BucketName is required"))
	}
	adapter := p.Adapter
	if adapter == "" && (p.TableName != "" || len(p.Tables) > 0) {
		adapter = "dynamodb"
	}
	switch adapter {
	case "":
	case "dynamodb":
		switch {
		case p.TableName != "" && len(p.Tables) > 0:
			errs = append(errs, errors.New("TableName and Tables are both set: layout v4 and v5 do not mix"))
		case p.TableName == "" && len(p.Tables) == 0:
			errs = append(errs, errors.New("the dynamodb adapter needs TableName or Tables"))
		}
		for m, n := range p.Tables {
			if !validModule(m) {
				errs = append(errs, fmt.Errorf("the Tables name an unknown module %q", m))
			}
			if n == "" {
				errs = append(errs, fmt.Errorf("the Tables entry of module %q has no name", m))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("adapter %q is not rendered here: only dynamodb is (a NATS block belongs to the identity stack)", adapter))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("sluispulumi: RenderPorts: %w", err)
	}

	withRegion := func(m map[string]any) map[string]any {
		if p.Region != "" {
			m["region"] = p.Region
		}
		return m
	}
	s3 := withRegion(map[string]any{"bucket": p.BucketName})
	if p.BlobPrefix != "" {
		s3["prefix"] = strings.Trim(p.BlobPrefix, "/")
	}
	ports := map[string]any{
		"blob": map[string]any{"adapter": "s3", "s3": s3},
	}
	if adapter == "dynamodb" {
		ports["adapter"] = "dynamodb"
		if len(p.Tables) > 0 {
			tables := map[string]any{}
			for m, n := range p.Tables {
				tables[string(m)] = n
			}
			ports["dynamodb"] = withRegion(map[string]any{"tables": tables})
		} else {
			ports["dynamodb"] = withRegion(map[string]any{"table": p.TableName})
		}
	}
	return map[string]any{"ports": ports}, nil
}

// RenderPortsYAML is RenderPorts as the YAML to put under a chart's `config:`.
func RenderPortsYAML(p PortsArgs) (string, error) {
	doc, err := RenderPorts(p)
	if err != nil {
		return "", err
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("sluispulumi: RenderPortsYAML: %w", err)
	}
	return string(raw), nil
}
