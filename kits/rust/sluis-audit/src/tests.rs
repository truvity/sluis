#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure
//! The shared vectors of `kits/conformance/audit` that are sluis's own, through the emitter of
//! `audit-emitter` configured as sluis configures it. The vectors of the emitter itself (the
//! canonical form, the ids, the pseudonym's form, the note of lost facts) are truvity/audit's, and
//! its crate passes them.

use std::cell::Cell;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use base64::Engine as _;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use serde_json::{Map, Value};

use crate::{
    Action, Config, Decision, Emitter, Entity, Event, Mac, MacFuture, MacRequest, PolicyRef,
    Pseudonyms, Status, Subject,
};

const RECORDS: &str = include_str!("../../../conformance/audit/record-vectors.json");

pub(crate) fn parse(s: &str) -> Value {
    serde_json::from_str(s).unwrap()
}

pub(crate) fn arr<'a>(v: &'a Value, k: &str) -> &'a Vec<Value> {
    v[k].as_array()
        .unwrap_or_else(|| panic!("{k} is not a list"))
}

pub(crate) fn text<'a>(v: &'a Value, k: &str) -> &'a str {
    v[k].as_str().unwrap_or("")
}

pub(crate) fn refusal_of(e: &connectrpc::ConnectError) -> (String, String) {
    let r = atoom_common::refusal::detail(e).expect("a Refusal detail");
    (r.reason, r.field_path)
}

thread_local! {
    /// The clock of the emitters of these tests, in Unix milliseconds: a `fn` cannot capture.
    static NOW_MS: Cell<u64> = const { Cell::new(0) };
}

fn vector_now() -> SystemTime {
    UNIX_EPOCH + Duration::from_millis(NOW_MS.with(Cell::get))
}

/// A `Mac` that answers with fixed bytes and keeps every request.
struct FixedMac {
    mac: Vec<u8>,
    asked: Mutex<Vec<MacRequest>>,
}

impl Mac for FixedMac {
    fn mac(&self, r: MacRequest) -> MacFuture<'_> {
        self.asked.lock().unwrap().push(r);
        let mac = self.mac.clone();
        Box::pin(async move { Ok(mac) })
    }
}

/// The emitter of sluis for the actor and boundary given, with a `Mac` in place of key@.
fn emitter(actor: &str, boundary: &str, release: &str, mac: Arc<FixedMac>) -> Emitter {
    let cfg = Config {
        actor: actor.into(),
        boundary: boundary.into(),
        release: release.into(),
        transport_socket: "/nonexistent/emit.sock".into(),
        key_socket: None,
        now: Some(vector_now),
    };
    Emitter::with_pseudonyms(
        cfg,
        Some(Pseudonyms {
            mac,
            label: crate::PSEUDONYM_LABEL.into(),
        }),
    )
    .unwrap()
}

/// Runs a future of the emitter on this thread, so that the clock of the thread is the emitter's.
fn on_this_thread<F: std::future::Future>(f: F) -> F::Output {
    tokio::runtime::Builder::new_current_thread()
        .build()
        .unwrap()
        .block_on(f)
}

/// The record the emitter of sluis builds from the inputs of a vector of `action-vectors.json`: the actor, the boundary
/// and the release of the vector, its clock, its correlation id, and a `Mac` that gives its pseudonym. The emitter sets
/// its own `metadata.uid`, so the vector's uid takes its place in the bytes.
pub(crate) fn built_record(c: &Value) -> String {
    let mut ev = event_of(&c["event"]);
    ev.correlation_id = c.get("correlation").and_then(Value::as_str).map(Into::into);
    let mac = match c.get("pseudonym").and_then(Value::as_str) {
        Some(p) => {
            let b64 = p.rsplit('.').next().unwrap();
            URL_SAFE_NO_PAD.decode(b64).unwrap()
        }
        None => vec![0; 16],
    };
    let mac = Arc::new(FixedMac {
        mac,
        asked: Mutex::default(),
    });
    let e = emitter(
        text(c, "actor"),
        text(c, "boundary"),
        text(c, "release"),
        mac,
    );
    NOW_MS.with(|n| n.set(c["time_ms"].as_u64().unwrap()));
    let (rec, uid) =
        on_this_thread(e.record(ev)).unwrap_or_else(|err| panic!("{}: {err:?}", text(c, "name")));
    let rec = String::from_utf8(rec).unwrap();
    assert_eq!(rec.matches(&uid).count(), 1, "{}", text(c, "name"));
    rec.replace(&uid, text(c, "uid"))
}

/// verifies: REQ-AUDIT-091
pub(crate) fn event_of(v: &Value) -> Event {
    let v = &expand(v);
    let a = &v["action"];
    Event {
        action: Action {
            name: text(a, "name").into(),
            class_uid: u32::try_from(a["class_uid"].as_u64().unwrap()).unwrap(),
            activity_id: u32::try_from(a["activity_id"].as_u64().unwrap()).unwrap(),
            activity_name: text(a, "activity_name").into(),
        },
        status: if v["status"] == 2 {
            Status::Failure
        } else {
            Status::Success
        },
        status_code: text(v, "status_code").into(),
        tenant: text(v, "tenant").into(),
        subject: v.get("subject").map(|s| Subject {
            tenant: text(s, "tenant").into(),
            kind: text(s, "kind").into(),
            value: text(s, "value").into(),
        }),
        policy: v.get("policy").map(|p| PolicyRef {
            decision: match p.get("decision").and_then(Value::as_i64) {
                Some(1) => Some(Decision::Allowed),
                Some(2) => Some(Decision::Denied),
                _ => None,
            },
            decision_id: text(p, "decision_id").into(),
            digest: text(p, "digest").into(),
            policy_ids: p
                .get("policy_ids")
                .and_then(Value::as_array)
                .map(|l| l.iter().map(|s| s.as_str().unwrap().into()).collect())
                .unwrap_or_default(),
            ..PolicyRef::default()
        }),
        // The vectors keep the member's old name, `sluis`: it is the product object, unmapped.sluis.
        product_object: object_of(v.get("sluis")),
        correlation_id: None,
        entity: v.get("entity").map(|e| Entity {
            kind: text(e, "type").into(),
            name: text(e, "name").into(),
            uid: text(e, "uid").into(),
            version: text(e, "version").into(),
        }),
        subject_best_effort: false,
        caller: text(v, "caller").into(),
        operation: text(v, "operation").into(),
    }
}

/// `{"$repeat": "a", "times": n}` is a string of n copies, so a vector can hold a long string.
fn expand(v: &Value) -> Value {
    match v {
        Value::Object(m) => {
            if let Some(Value::String(s)) = m.get("$repeat") {
                return Value::String(
                    s.repeat(usize::try_from(m["times"].as_u64().unwrap()).unwrap()),
                );
            }
            Value::Object(m.iter().map(|(k, e)| (k.clone(), expand(e))).collect())
        }
        Value::Array(a) => Value::Array(a.iter().map(expand).collect()),
        other => other.clone(),
    }
}

fn object_of(v: Option<&Value>) -> Map<String, Value> {
    match v.map(expand) {
        Some(Value::Object(m)) => m,
        _ => Map::new(),
    }
}

/// The emitter asks key@ for the pseudonym with sluis's label and the boundary as the first part of the input, as the
/// vectors of the input say (ADR 0029 "Pseudonyms"): the pseudonyms sluis writes are the ones it always wrote.
#[test]
fn the_pseudonym_input_and_label_match_the_vectors() {
    let f = parse(RECORDS);
    assert_eq!(text(&f, "mac_label"), crate::PSEUDONYM_LABEL);
    assert!(!arr(&f, "mac_inputs").is_empty());
    let mut seen = std::collections::HashSet::new();
    for v in arr(&f, "mac_inputs") {
        let mac = Arc::new(FixedMac {
            mac: vec![0; 16],
            asked: Mutex::default(),
        });
        let e = emitter(
            "session",
            text(v, "boundary"),
            "0.0.0-test",
            Arc::clone(&mac),
        );
        let ev = Event {
            action: Action {
                name: "session.signin".into(),
                class_uid: 3002,
                activity_id: 1,
                activity_name: String::new(),
            },
            status: Status::Failure,
            status_code: "grant".into(),
            subject: Some(Subject {
                tenant: text(v, "tenant").into(),
                kind: text(v, "kind").into(),
                value: text(v, "value").into(),
            }),
            ..Event::default()
        };
        NOW_MS.with(|n| n.set(1_793_750_400_000));
        // The record may be refused by its schema for the shape of a vector's subject; the request to key@ is made first.
        let _ = on_this_thread(e.record(ev));
        let asked = mac.asked.lock().unwrap();
        assert_eq!(asked.len(), 1);
        assert_eq!(asked[0].label, crate::PSEUDONYM_LABEL);
        assert_eq!(asked[0].purpose, crate::PSEUDONYM_PURPOSE);
        assert_eq!(asked[0].tenant, text(v, "tenant"));
        let got: String = asked[0].input.iter().map(|b| format!("{b:02x}")).collect();
        assert_eq!(got, text(v, "input_hex"));
        assert!(seen.insert(got), "two vectors give the same input");
    }
}

/// The emitter refuses the events of the vectors before anything else, with the reason and path of the vector; the
/// product object is `unmapped.sluis`.
#[test]
fn event_refusals_match_the_vectors() {
    let f = parse(RECORDS);
    assert!(!arr(&f, "event_refusals").is_empty());
    for r in arr(&f, "event_refusals") {
        let mac = Arc::new(FixedMac {
            mac: vec![0; 16],
            asked: Mutex::default(),
        });
        let e = emitter("key", "dev", "0.0.0-test", mac);
        let err = on_this_thread(e.record(event_of(&r["event"]))).expect_err(text(r, "name"));
        let (reason, path) = refusal_of(&err);
        assert_eq!(reason, text(r, "reason"), "{}", text(r, "name"));
        assert_eq!(path, text(r, "path"), "{}", text(r, "name"));
    }
}
