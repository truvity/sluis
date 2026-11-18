#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure
//! The kinds of the limits of `entry` (ADR 0036): `RateBound`, which a boundary may leave undeclared (its listing is then empty,
//! where every other config kind with no document is unavailable), and `RateWindow`, which `entry` writes with no tenant and no grant.

mod common;

use common::F;
use sluis_proto::sluis::store::v2alpha1::ListRequest;

fn list(kind: &str) -> ListRequest {
    ListRequest {
        kind: kind.into(),
        ..ListRequest::default()
    }
}

/// verifies: REQ-SEC-047
#[tokio::test]
async fn an_undeclared_rate_bound_lists_as_empty_and_a_declared_one_lists_its_document() {
    let f = F::new().await;
    f.as_("entry");
    let r = f.list(list("rate-bound")).await.expect("an empty listing");
    assert!(r.items.is_empty());

    f.docs
        .items
        .lock()
        .unwrap()
        .insert(("rate-bound".into(), "default".into()), vec![1, 2, 3]);
    f.docs
        .versions
        .lock()
        .unwrap()
        .insert("rate-bound".into(), 4);
    let r = f.list(list("rate-bound")).await.unwrap();
    assert_eq!(r.items.len(), 1);
    assert_eq!(r.items[0].address.id, "default");
    assert_eq!(r.items[0].version, 4);
}

/// verifies: REQ-SEC-047
///
/// Only the kind that is listed as optional is: a kind that the installation must declare is unavailable when it has no
/// document, so a reader that fails closed on it does.
#[tokio::test]
async fn any_other_config_kind_with_no_document_is_unavailable() {
    let f = F::new().await;
    f.as_("entry");
    for kind in ["installation-client", "front-host"] {
        let e = f.list(list(kind)).await.expect_err(kind);
        assert_eq!(e.code.as_str(), "unavailable", "{kind}");
    }
}

use buffa::Message as _;
use common::{op, with_ttl};
use sluis_proto::sluis::entry::v2alpha1::RateWindow;
use sluis_proto::sluis::store::v2alpha1::{GetRequest, Verb};
use sluis_proto::sluis::r#type::v2alpha1::Address;

fn bucket(milli: u64) -> Vec<u8> {
    RateWindow {
        milli_tokens: milli,
        at_ms: 1_700_000_000_000,
        ..RateWindow::default()
    }
    .encode_to_vec()
}

/// verifies: REQ-SEC-027
///
/// `entry` writes the bucket of a source address with no tenant and no grant, creates it, updates it at its version and reads
/// it; the item is the message of the kind and its level is bounded; no other actor writes the kind; a bucket of a tenant
/// needs a grant like any item of a tenant.
#[tokio::test]
async fn entry_writes_a_bucket_that_is_no_tenants_without_a_grant_and_nobody_else_does() {
    let f = F::new().await;
    f.as_("entry");
    let id = "token-address/00112233445566778899aabbccddeeff";
    f.write(
        vec![with_ttl(
            op(Verb::CREATE, "rate-window", "", id, &bucket(2000)),
            60,
        )],
        &[],
    )
    .await
    .expect("a create with no tenant and no grant");
    let got = f
        .get_req(GetRequest {
            address: Address {
                kind: "rate-window".into(),
                id: id.into(),
                ..Address::default()
            }
            .into(),
            ..GetRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(got.item.version, 1);
    assert_eq!(
        RateWindow::decode_from_slice(&got.item.value)
            .unwrap()
            .milli_tokens,
        2000
    );
    let mut update = op(Verb::UPDATE, "rate-window", "", id, &bucket(1000));
    update.if_version = 1;
    f.write(vec![with_ttl(update, 60)], &[])
        .await
        .expect("an update at its version");
    // A second create finds it there, and an update at an old version loses.
    let again = f
        .write(
            vec![with_ttl(
                op(Verb::CREATE, "rate-window", "", id, &bucket(2000)),
                60,
            )],
            &[],
        )
        .await
        .unwrap_err();
    assert_eq!(again.code.as_str(), "already_exists");
    let mut stale = op(Verb::UPDATE, "rate-window", "", id, &bucket(0));
    stale.if_version = 1;
    let stale = f.write(vec![with_ttl(stale, 60)], &[]).await.unwrap_err();
    assert_eq!(stale.code.as_str(), "aborted");
    // Not the message of the kind, or a level past the largest bucket of the release.
    for bad in [vec![0xff, 0xff, 0xff], bucket(100_000 * 1000 + 1)] {
        let e = f
            .write(
                vec![with_ttl(
                    op(Verb::CREATE, "rate-window", "", "x/y", &bad),
                    60,
                )],
                &[],
            )
            .await
            .unwrap_err();
        assert_eq!(e.code.as_str(), "invalid_argument");
    }
    // A bucket of a tenant is a tenant's item and needs the tenant's grant.
    let e = f
        .write(
            vec![with_ttl(
                op(Verb::CREATE, "rate-window", "acme", "x/y", &bucket(1)),
                60,
            )],
            &[],
        )
        .await
        .unwrap_err();
    assert_eq!(e.code.as_str(), "permission_denied");
    // No other actor writes the kind, and none writes one with no tenant by this exception.
    for who in ["session", "exchange", "console", "signer"] {
        f.as_(who);
        let e = f
            .write(
                vec![with_ttl(
                    op(Verb::CREATE, "rate-window", "", "a/b", &bucket(1)),
                    60,
                )],
                &[],
            )
            .await
            .unwrap_err();
        assert_eq!(e.code.as_str(), "permission_denied", "{who}");
    }
}
