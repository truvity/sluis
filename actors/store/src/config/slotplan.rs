//! The card slot map check (ADR 0006, "The slot plan"; docs/reference/keys.md, "Board slot plan"): which PIV slot each
//! enabled key purpose of a KeyPlan lives in. It is the Rust twin of `kits/go/slotplan`, which key@ applies when it
//! starts and in its check command, and the two read the same vectors (`kits/go/slotplan/testdata/vectors.json`), so
//! that `sluis config check`, store@ at load and key@ apply the same slot map rules (REQ-PKI-037, REQ-PKI-038, REQ-PKI-042). These are the rules common to all stores; on a card key@ adds limits of the card driver (`CheckCard`: no override of a default slot, no card-key purpose outside the default map) that only key@'s check applies.
//!
//! The map is the default one (version 2) with the installation's overrides: a purpose names its slot in the KeyPlan, or
//! takes the default. A purpose the KeyPlan does not list is not enabled, needs no slot and gets no key. A refusal names
//! the entry and a word, never a value.

use std::collections::{BTreeMap, BTreeSet};

/// The default map, version 2 (decided 2026-11-18, maintainer decision S1 A): the slot of each purpose that has one. Version 2
/// changed only slots that version 1 left spare, so the first ten rows are version 1's. A purpose that is listed without a slot
/// takes its slot from here; the slots 90 to 95 are the rotation spares and have no default holder, and a purpose the map does
/// not hold is named in a KeyPlan with one of them; slot 9e is not used (docs/architecture/keys.yaml, `slot_plan`).
pub const DEFAULT: [(&str, &str); 17] = [
    ("x509-intermediate", "9c"),
    ("token-signing", "9a"),
    ("wrapping", "9d"),
    ("ssh-user", "82"),
    ("ssh-host", "83"),
    ("audit-log", "84"),
    ("release-signing", "85"),
    ("workload-issuer", "86"),
    ("pin-release", "87"),
    ("token-signing-rs256", "88"),
    ("issuance-log", "89"),
    ("enrolment", "8a"),
    ("token-signing-ps256", "8b"),
    ("token-signing-es256", "8c"),
    ("token-signing-eddsa", "8d"),
    ("witness", "8e"),
    ("audit-log-second", "8f"),
];

/// One entry of a KeyPlan: a purpose, the algorithm of its key and the slot it names (empty for the default).
pub struct Purpose<'a> {
    pub name: &'a str,
    pub algorithm: &'a str,
    pub slot: &'a str,
}

/// Whether the algorithm is one of an asymmetric key that lives in a card slot. A data key purpose names AES-256-GCM
/// and has no slot. RS256 is listed: a purpose that is left out here skips every slot check, so two purposes could be
/// planned in one slot. Listed, it needs a slot of its own like the others. PS256 and EdDSA are not listed, as in the Go kit:
/// no key@ holds a key for them yet, and the keys of NATS and of the cloud federations are not card keys.
fn on_card(algorithm: &str) -> bool {
    matches!(
        algorithm,
        "ES384" | "ES256" | "RS256" | "ECDH-P384" | "ECDH-P-384"
    )
}

/// Whether `s` is a PIV slot a board uses: 9a, 9c, 9d, or a retired slot 82 to 95, in lower-case hex. 9e, the
/// card-authentication slot, is not one (it signs without a PIN by default).
fn valid_slot(s: &str) -> bool {
    if s.len() != 2 || s != s.to_ascii_lowercase() {
        return false;
    }
    if matches!(s, "9a" | "9c" | "9d") {
        return true;
    }
    u8::from_str_radix(s, 16).is_ok_and(|n| (0x82..=0x95).contains(&n))
}

/// Checks the card-key purposes of a list against the default map and the overrides they name. It refuses, with the path
/// under `at` and a word and never a value, a slot that is no board slot, a purpose with no slot (not named and no
/// default), two purposes in one slot and a purpose listed twice. The refusals are collected and sorted, as key@ does.
///
/// # Errors
///
/// The refusals, joined, behind `card slot map: `.
pub fn check(at: &str, purposes: &[Purpose<'_>]) -> Result<(), String> {
    let mut listed: BTreeSet<&str> = BTreeSet::new();
    let mut by_slot: BTreeMap<String, &str> = BTreeMap::new();
    let mut problems: Vec<String> = Vec::new();
    for (i, p) in purposes.iter().enumerate() {
        if !on_card(p.algorithm) {
            continue;
        }
        let where_ = format!("{at}[{i}]");
        if listed.contains(p.name) {
            problems.push(format!("{where_}: listed twice"));
            continue;
        }
        let slot = if p.slot.is_empty() {
            let Some((_, def)) = DEFAULT.iter().find(|(n, _)| *n == p.name) else {
                problems.push(format!(
                    "{where_}: no slot (name one of the spare slots 90 to 95)"
                ));
                continue;
            };
            (*def).to_owned()
        } else {
            p.slot.to_owned()
        };
        if !valid_slot(&slot) {
            problems.push(format!("{where_}.slot: not a slot of a board"));
            continue;
        }
        if let Some(other) = by_slot.get(&slot) {
            problems.push(format!("{where_}.slot: shared with {other}"));
            continue;
        }
        listed.insert(p.name);
        by_slot.insert(slot, p.name);
    }
    if problems.is_empty() {
        return Ok(());
    }
    problems.sort();
    Err(format!("card slot map: {}", problems.join("; ")))
}

#[cfg(test)]
mod tests {
    use super::*;

    const VECTORS: &str = include_str!("../../../../kits/go/slotplan/testdata/vectors.json");
    const KEYS_YAML: &str = include_str!("../../../../docs/architecture/keys.yaml");

    /// verifies: REQ-KEYS-278
    ///
    /// The default map is the registry's slot plan, version 2, slot for slot (the Go kit holds the same table and tests it too).
    #[test]
    fn the_default_map_is_the_registrys_slot_plan() {
        let plan = KEYS_YAML
            .split("\nslot_plan:\n")
            .nth(1)
            .and_then(|t| t.split("\n  slots:\n").nth(1))
            .and_then(|t| t.split("\n  policy:").next())
            .expect("the slot plan of keys.yaml");
        let rows: Vec<(&str, &str)> = plan
            .lines()
            .filter_map(|l| l.trim().split_once(": "))
            .map(|(slot, name)| (name, slot.trim_matches('\'')))
            .collect();
        assert_eq!(rows.len(), DEFAULT.len(), "{rows:?}");
        for (name, slot) in &rows {
            assert!(
                DEFAULT.contains(&(*name, *slot)),
                "{name}: keys.yaml slot {slot}, not in the default map"
            );
        }
        assert!(KEYS_YAML.contains("slot_plan:\n  version: 2\n"));
    }

    /// verifies: REQ-PKI-037, REQ-PKI-038, REQ-PKI-042, REQ-KEYS-268, REQ-KEYS-279
    ///
    /// The vectors key@ reads in Go (`kits/go/slotplan`): each case is accepted, or refused with the text it holds.
    #[test]
    fn the_shared_vectors_are_accepted_and_refused_as_key_does() {
        let v: serde_json::Value = serde_json::from_str(VECTORS).unwrap();
        let cases = v["cases"].as_array().unwrap();
        assert!(
            cases.len() >= 28,
            "{} cases: a sweep over nothing",
            cases.len()
        );
        let mut refused = 0;
        for c in cases {
            let name = c["name"].as_str().unwrap();
            let list: Vec<Purpose<'_>> = c["check"]
                .as_array()
                .unwrap()
                .iter()
                .map(|p| Purpose {
                    name: p["purpose"].as_str().unwrap(),
                    algorithm: p["algorithm"].as_str().unwrap(),
                    slot: p["slot"].as_str().unwrap(),
                })
                .collect();
            let want = c["error"].as_str().unwrap();
            match check("purposes", &list) {
                Ok(()) => assert_eq!(want, "", "{name}: accepted, the vector wants {want:?}"),
                Err(e) => {
                    refused += 1;
                    assert!(
                        !want.is_empty(),
                        "{name}: refused ({e}), the vector accepts"
                    );
                    assert!(e.contains(want), "{name}: {e:?} does not hold {want:?}");
                    assert!(e.starts_with("card slot map: purposes["), "{name}: {e}");
                }
            }
        }
        assert!(refused >= 18, "{refused} refusals");
    }
}
