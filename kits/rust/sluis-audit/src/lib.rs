//! sluis's side of the audit emitter. The emitter itself (the record, its canonical form, the
//! schemas and their policy, the ids, the pseudonym's form, the count of lost facts and the
//! transport) is the crate `audit-emitter` of truvity/audit, and this crate re-exports it. What
//! stays here is what is sluis's own:
//!
//! - the action table of sluis, generated from `docs/architecture/audit-actions.yaml` by
//!   `tools/gen/audit` into `generated.rs` ([`SluisTable::sluis`]);
//! - the `Mac` of the `pseudonym` purpose over key@, with sluis's domain label
//!   [`PSEUDONYM_LABEL`];
//! - sluis's names for audit's neutral model: the product is [`PRODUCT`], an actor is a
//!   component, a boundary is the scope ([`Config`]).
//!
//! A person is never named in a record: the emitter asks key@ for `Mac` under the tenant's
//! pseudonym key before the record leaves the process (ADR 0029 "Pseudonyms"). The product's own
//! facts are `unmapped.sluis`, which an event carries in [`Event::product_object`].
//!
//! The Go package `kits/go/audit` is the same wrapper over the Go module of truvity/audit, and the
//! vectors of `kits/conformance/audit` hold both to the same bytes.

#[rustfmt::skip]
mod generated;

use std::ops::Deref;
use std::path::{Path, PathBuf};
use std::sync::{Arc, OnceLock};
use std::time::SystemTime;

use sluis_kit::{Transport, dial};
use sluis_proto::sluis::key::v2alpha1::KeyServiceClient;

pub use audit_emitter::{
    Action, Activity, DEFS_NAME, Decided, Decision, Entity, Event, Mac, MacFuture, MacRequest,
    OCSF_VERSION, POLICY_VERSION, PSEUDONYM_PURPOSE, PolicyRef, Pseudonyms, RECORD_MAX,
    RESULTS_MAX, Result, Row, STRING_MAX, Status, Subject, Table, canonical, check_documents,
    check_policy, max_cost, new_uuid_v7, valid_release,
};

/// The product every record of sluis names: `metadata.product.name`, and the product object
/// `unmapped.sluis`.
pub const PRODUCT: &str = "sluis";

/// sluis's domain label of a pseudonym (ADR 0029 "Pseudonyms"), which key@ binds into the MAC. A
/// changed label changes every pseudonym.
pub const PSEUDONYM_LABEL: &str = "sluis-pseudonym-v1";

/// The action table of sluis itself.
pub trait SluisTable {
    /// The table of sluis, generated from `docs/architecture/audit-actions.yaml`, built once per
    /// process and shared.
    fn sluis() -> Arc<Table>;
}

impl SluisTable for Table {
    fn sluis() -> Arc<Table> {
        static SLUIS: OnceLock<Arc<Table>> = OnceLock::new();
        Arc::clone(SLUIS.get_or_init(|| {
            #[allow(clippy::expect_used)]
            // a broken generated table is a build error, caught by the tests
            Arc::new(
                Table::new(generated::ROWS, generated::SCHEMAS)
                    .expect("the generated action table is broken"),
            )
        }))
    }
}

/// What an [`Emitter`] of sluis needs, in sluis's words.
#[derive(Clone, Debug)]
pub struct Config {
    /// The emitting actor's registry name (or a product's service name): audit's component.
    pub actor: String,
    /// The boundary: audit's scope.
    pub boundary: String,
    /// The release (`sluis_version`).
    pub release: String,
    /// The emit socket: `/run/sluis/audit/<boundary>/emit.sock`.
    pub transport_socket: PathBuf,
    /// key@'s socket for this caller; `None` for an actor with no pseudonym rule, which then cannot
    /// send a [`Subject`].
    pub key_socket: Option<PathBuf>,
    /// The clock; `None` is the system clock. A test injects one.
    pub now: Option<fn() -> SystemTime>,
}

/// The emitter of `audit-emitter`, configured for sluis. It is cheap to clone and safe to share;
/// the clones share one process name and one set of totals of lost facts. Every method of the
/// emitter is reached through `Deref`.
#[derive(Clone)]
pub struct Emitter(audit_emitter::Emitter);

impl Emitter {
    /// Returns an emitter with the table of sluis, the product `sluis`, the actor as the
    /// component, the boundary as the scope, and key@'s `Mac` when a key socket is given. Nothing
    /// is dialled until the first call, so a transport that is not up yet does not fail
    /// construction.
    ///
    /// # Errors
    ///
    /// `invalid_argument` when the actor (`component`), the boundary (`scope`) or the release is
    /// empty or too long.
    pub fn new(cfg: Config) -> Result<Self> {
        let pseudonyms = cfg.key_socket.as_deref().map(|p| Pseudonyms {
            mac: Arc::new(KeyMac::dial(p)),
            label: PSEUDONYM_LABEL.into(),
        });
        Self::with_pseudonyms(cfg, pseudonyms)
    }

    /// [`Emitter::new`] with the pseudonyms given, in place of key@'s: the tests of this crate give a `Mac` of their own.
    pub(crate) fn with_pseudonyms(cfg: Config, pseudonyms: Option<Pseudonyms>) -> Result<Self> {
        audit_emitter::Emitter::new(audit_emitter::Config {
            product: PRODUCT.into(),
            release: cfg.release,
            scope: cfg.boundary,
            component: cfg.actor,
            table: Table::sluis(),
            transport_socket: cfg.transport_socket,
            pseudonyms,
            correlation: Some(sluis_kit::correlation::current),
            now: cfg.now,
        })
        .map(Self)
    }

    /// Uses the action table of another project's release instead of the table of sluis (ADR 0030:
    /// a product's release has a catalogue of its own).
    #[must_use]
    pub fn with_table(self, table: Arc<Table>) -> Self {
        Self(self.0.with_table(table))
    }

    /// The emitter of `audit-emitter` behind this one.
    #[must_use]
    pub fn into_inner(self) -> audit_emitter::Emitter {
        self.0
    }
}

impl Deref for Emitter {
    type Target = audit_emitter::Emitter;

    fn deref(&self) -> &Self::Target {
        &self.0
    }
}

/// The `Mac` of the `pseudonym` purpose over key@ (`KeyService.Mac`): key@ holds the tenant's
/// pseudonym key and binds the label in; the emitter holds no key.
struct KeyMac(KeyServiceClient<Transport>);

impl KeyMac {
    fn dial(socket: &Path) -> Self {
        let k = dial(socket);
        Self(KeyServiceClient::new(k.transport(), k.config()))
    }
}

impl Mac for KeyMac {
    fn mac(&self, r: MacRequest) -> MacFuture<'_> {
        Box::pin(async move {
            let resp = self
                .0
                .mac(sluis_proto::sluis::key::v2alpha1::MacRequest {
                    purpose: r.purpose,
                    tenant: r.tenant,
                    label: r.label,
                    input: r.input,
                    epoch: r.epoch,
                    ..Default::default()
                })
                .await?
                .into_owned();
            Ok(resp.mac.to_vec())
        })
    }
}

#[cfg(test)]
mod table_tests;
#[cfg(test)]
mod tests;
