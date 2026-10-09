# Catalogue reference

One catalogue belongs to one application, and an installation admits one application. Format: [catalogue.schema.json](../../../audit/sdk/schemas/catalogue.schema.json). Example: [common.yaml](../../../audit/sdk/catalogue/common.yaml).

Applications share the format and the [framework profiles](../../../audit/profiles/README.md), never a catalogue. Decided in [0053](../../decisions/0053-one-installation-per-service-or-product.md).

## Where it lives

| Step | Detail |
|---|---|
| Location | Next to the emitting code, under its version control |
| CI | Validated in that repository against this repository's validator |
| Delivery | Embedded in the binary or mounted by the chart |
| Registration | At start-up over `RegistryService`, which the receiver serves |
| Archive | The receiver copies every version into the archive on first use |

## Fields

| Field | Content |
|---|---|
| `source`, `version`, `locales` | Identity and languages |
| `actor_kinds` | name: category (internal, external, machine), attributes schema |
| `target_types` | name: `is_person`, attributes schema |
| `context_areas` | name: schema |
| `meters` | name: kind (count, gauge), unit, dimensions schema |
| `actions` | `source.resource.verb`: summary, operation, categories, profiles, capture level, delivery, target types, data schema and version, message per locale, meter with quantity path and counted outcomes (success only by default) |

## Delivery

The action declares its delivery, so a catalogue behaves the same in direct and stream mode. Decided in [0054](../../decisions/0054-two-deliveries-and-a-durable-ack.md) and [0059](../../decisions/0059-sink-durability-and-transports.md).

| `delivery` | The call returns | Receiver down | For |
|---|---|---|---|
| `block` | When the receiver acknowledges durability | The action fails | Privileged sign-in, key destruction, billable operation |
| `async` (default) | At once | The record waits in a bounded in-memory queue and is retried with backoff | Everything else |

An `async` record is dropped only when the queue overflows. The emitter counts it as `audit.emit.records.dropped` and logs it.

The loader refuses the retired values `outbox` (use `block` or `async`) and `best_effort` (use `async`), naming the replacement.

## Categories

Profiles require categories, never actions. The application's catalogue and the component's own together must cover a profile's required categories. `audit validate --deployment` reports a gap in CI. The receiver logs one after each registration. Neither refuses an application.

| Categories |
|---|
| `authentication`, `privileged_access`, `account_lifecycle`, `authorization_decision`, `configuration_change`, `data_access`, `data_change`, `key_lifecycle`, `credential_lifecycle`, `log_access`, `logging_control`, `clock`, `billing` |

## Message templates

Each action has one ICU MessageFormat template per declared locale. The viewer renders them in the browser with FormatJS; exports render none yet. The validator refuses a template that names an argument the record lacks. It also refuses a dotted name, and a data schema where two properties map to one name (`/a_b` and `/a/b` are both `data_a_b`). An argument a record does not carry renders as a gap.

An argument names a record field with an underscore for each step, such as `{targets_0_id}` or `{data_address_city}`.

| Argument | Value |
|---|---|
| `id`, `source`, `action`, `operation`, `tenant`, `profile` | Core fields |
| `occurred_at`, `recorded_at` | Timestamps |
| `actor`, `actor_id`, `actor_kind` | The actor; `actor` alone is its id |
| `subject`, `subject_id`, `subject_kind` | The subject |
| `outcome`, `outcome_result`, `outcome_reason`, `outcome_code` | `outcome` is the result word: `success`, `failure` or `denied` |
| `observer_id`, `observer_instance` | Who reported it |
| `targets_N_id`, `targets_N_name`, `targets_N_type` | The first four targets, N in 0..3 |
| `data_<property>` | Any property of the action's data schema; nested names join with underscores |
| `meter_name`, `meter_quantity`, `meter_unit` | When the action is metered |

## Validation in CI

```
audit validate ./catalogue.yaml
audit check-emitters ./ --catalogue ./catalogue.yaml
```

| Command | Checks |
|---|---|
| `validate` | The document and every `.json` schema beside it. With `--deployment <file>`, also the categories a profile requires that nothing emits |
| `check-emitters` | String literals in a source tree against the catalogue's actions. It cannot see a name assembled at run time |
