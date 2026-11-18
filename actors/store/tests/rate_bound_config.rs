#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure
//! The payload reader of the config kind `RateBound` (ADR 0036 "Inside sluis"): the limits of `entry`. What it accepts, what it
//! refuses with the path of the fault and never the value, and that no limit gets past the maximum the release builds in.

use buffa::Message as _;
use sluis_proto::sluis::config::v2alpha1 as pb;
use sluis_store::ratebound::{self, Limits};
use store::config::payload;

fn kind() -> &'static sluis_registry::Kind {
    sluis_registry::kinds()
        .iter()
        .find(|k| k.name == "RateBound")
        .unwrap()
}

fn bound(doc: &str) -> Result<pb::RateBound, String> {
    payload::decode(kind(), doc.as_bytes())
        .map(|items| {
            assert_eq!(items.len(), 1);
            assert_eq!(items[0].0, "default");
            pb::RateBound::decode_from_slice(&items[0].1).unwrap()
        })
        .map_err(|e| e.to_string())
}

/// What the decoder alone says: its own words. The service applies the generated schema first, which refuses the same
/// shapes in other words; the maximum is the decoder's, because a schema states a form and not a range.
fn refused_by_the_decoder(doc: &str) -> String {
    payload::decode_unvalidated(kind(), doc.as_bytes())
        .expect_err("the decoder must refuse it")
        .0
}

const FULL: &str = "default:
  token_client: { limit: 600, window: 1m }
  token_address: { limit: 120, window: 1m }
  authorize_client: { limit: 600, window: 1m }
  authorize_address: { limit: 120, window: 1m }
  recovery_address: { limit: 10, window: 15m }
";

/// verifies: REQ-SEC-047
#[test]
fn a_rate_bound_loads_and_is_read_back_whole() {
    let b = bound(FULL).unwrap();
    let l = Limits::from_bound(&b);
    assert_eq!(l.token_client.limit, 600);
    assert_eq!(l.token_address.limit, 120);
    assert_eq!(l.recovery_address.limit, 10);
    assert_eq!(l.recovery_address.window.as_secs(), 15 * 60);
    assert_eq!(l.authorize_client.window.as_secs(), 60);
    assert_eq!(ratebound::check(&b), Ok(()));
}

/// verifies: REQ-SEC-047
#[test]
fn a_limit_that_the_document_leaves_out_keeps_its_default() {
    let b = bound("default:\n  recovery_address: { limit: 3, window: 1h }\n").unwrap();
    let l = Limits::from_bound(&b);
    assert_eq!(l.recovery_address.limit, 3);
    assert_eq!(l.recovery_address.window.as_secs(), 3600);
    assert_eq!(l.token_client, Limits::default().token_client);
    assert_eq!(l.token_address, Limits::default().token_address);
    // A document with no limit at all is every default.
    let none = bound("default: {}\n").unwrap();
    assert_eq!(Limits::from_bound(&none), Limits::default());
}

/// verifies: REQ-SEC-046
///
/// The bounds the release builds in: a limit of 1 to 100000 requests, in a window of 1 second to 1 hour. The ends load and the
/// next value past each is refused, by the path of the field.
#[test]
fn a_limit_past_the_maximum_the_release_builds_in_is_refused() {
    for doc in [
        "default:\n  token_client: { limit: 1, window: 1s }\n",
        "default:\n  token_client: { limit: 100000, window: 1h }\n",
        "default:\n  token_client: { limit: 100000, window: 3600s }\n",
    ] {
        bound(doc).unwrap_or_else(|e| panic!("{doc}: {e}"));
    }
    for (doc, path, rule) in [
        (
            "default:\n  token_client: { limit: 100001, window: 1m }\n",
            "default.token_client.limit",
            "must be between 1 and 100000",
        ),
        (
            "default:\n  authorize_address: { limit: 0, window: 1m }\n",
            "default.authorize_address.limit",
            "must be between 1 and 100000",
        ),
        (
            "default:\n  token_address: { limit: 5, window: 3601s }\n",
            "default.token_address.window",
            "must be between 1 second and 1 hour",
        ),
        (
            "default:\n  recovery_address: { limit: 5, window: 2h }\n",
            "default.recovery_address.window",
            "must be between 1 second and 1 hour",
        ),
        (
            "default:\n  recovery_address: { limit: 5, window: 0s }\n",
            "default.recovery_address.window",
            "must be above zero",
        ),
    ] {
        let said = refused_by_the_decoder(doc);
        assert_eq!(said, format!("{path}: {rule}"), "{doc}");
        // The service refuses it too, in the decoder's words (the schema states a form, and these are forms it allows).
        assert_eq!(bound(doc).unwrap_err(), said, "{doc}");
    }
}

/// verifies: REQ-SEC-046
///
/// The shapes a limit cannot have: a part missing, an unknown key, a name that is not one of the five. An error names a place
/// and never a value.
#[test]
fn a_limit_that_is_not_made_of_a_count_and_a_window_is_refused_by_place() {
    for (doc, path) in [
        (
            "default:\n  token_client: { window: 1m }\n",
            "default.token_client.limit",
        ),
        (
            "default:\n  token_client: { limit: 5 }\n",
            "default.token_client.window",
        ),
        (
            "default:\n  token_client: { limit: 5, window: 1m, burst: 9 }\n",
            "default.token_client.<key>",
        ),
        (
            "default:\n  token_everyone: { limit: 5, window: 1m }\n",
            "default.<key>",
        ),
        ("default:\n  token_client: 5\n", "default.token_client"),
        (
            "default:\n  token_client: { limit: -1, window: 1m }\n",
            "default.token_client.limit",
        ),
        (
            "default:\n  token_client: { limit: 5, window: 90 }\n",
            "default.token_client.window",
        ),
    ] {
        let said = refused_by_the_decoder(doc);
        assert!(said.starts_with(&format!("{path}: ")), "{doc}: {said}");
        let service = bound(doc).unwrap_err();
        assert!(
            service.starts_with(&format!("{path}: "))
                || service.starts_with("default.token_client: "),
            "{doc}: {service}"
        );
        assert!(!service.contains("everyone"), "{service}");
    }
}

/// verifies: REQ-SEC-046
///
/// A kind with one item: a second item, or none, is refused.
#[test]
fn a_rate_bound_is_a_singleton_kind() {
    assert!(bound("{}\n").is_err());
    assert!(bound("default: {}\nother: {}\n").is_err());
    assert!(bound("other: {}\n").is_err());
}
