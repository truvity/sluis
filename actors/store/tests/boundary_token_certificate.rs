#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure
//! `token_certificate` of the Boundary document (decision X1 A of 2026-11-18, ADR 0006 "Amendment of 2026-11-18"): the switch
//! that turns the token certificate on for an installation. It is off when the key is absent, it is a plain boolean, and the
//! payload reader and the generated schema agree on that.

use buffa::Message as _;
use sluis_proto::sluis::config::v2alpha1 as pb;
use store::config::payload;

fn kind() -> &'static sluis_registry::Kind {
    sluis_registry::kinds()
        .iter()
        .find(|k| k.name == "Boundary")
        .unwrap()
}

fn doc(extra: &str) -> String {
    format!(
        "default:\n  name: dev\n  kind: environment\n  level: L2\n  environment: development\n  issuer: https://sluis.dev.local.test\n  uid_block: 0\n  default_tenant: acme\n{extra}"
    )
}

fn boundary(extra: &str) -> Result<pb::Boundary, String> {
    payload::decode(kind(), doc(extra).as_bytes())
        .map(|items| pb::Boundary::decode_from_slice(&items[0].1).unwrap())
        .map_err(|e| e.to_string())
}

/// The refusal of the decoder alone and of the service (schema first), which must name the same place.
fn refused(extra: &str) -> String {
    let decoder = payload::decode_unvalidated(kind(), doc(extra).as_bytes())
        .expect_err("the decoder must refuse it")
        .0;
    let service = payload::decode(kind(), doc(extra).as_bytes())
        .expect_err("the service, schema first, must refuse it too")
        .0;
    let place = |e: &str| {
        e.split_once(": ")
            .map(|(p, _)| p.to_owned())
            .unwrap_or_default()
    };
    assert_eq!(place(&decoder), place(&service), "{decoder:?} {service:?}");
    decoder
}

/// verifies: REQ-PKI-059
#[test]
fn the_token_certificate_is_off_unless_the_boundary_turns_it_on() {
    assert!(!boundary("").unwrap().token_certificate);
    assert!(
        !boundary("  token_certificate: false\n")
            .unwrap()
            .token_certificate
    );
    assert!(
        boundary("  token_certificate: true\n")
            .unwrap()
            .token_certificate
    );
}

/// verifies: REQ-PKI-059
#[test]
fn the_switch_is_a_plain_boolean_and_nothing_else() {
    // The strict profile reads only a plain true or false as a boolean: the other spellings of YAML 1.1 are strings, and null,
    // a number and a quoted word are not a switch.
    for bad in [
        "null", "~", "1", "0", "yes", "on", "True", "TRUE", "\"true\"", "[]",
    ] {
        let e = refused(&format!("  token_certificate: {bad}\n"));
        assert!(e.starts_with("default.token_certificate: "), "{bad}: {e}");
    }
}

/// verifies: REQ-PKI-059
#[test]
fn the_switch_is_a_key_of_the_boundary_once() {
    let e = payload::decode_unvalidated(
        kind(),
        doc("  token_certificate: true\n  token_certificate: false\n").as_bytes(),
    );
    assert!(e.is_err(), "a duplicate key is refused");
}
