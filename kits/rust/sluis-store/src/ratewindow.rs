//! Reading and writing the temp kind `RateWindow` (ADR 0036, "Inside sluis"), for `entry`, which is its one writer.
//!
//! A bucket belongs to no tenant (the address carries `-`), so the client is the one with an empty tenant, as for
//! `KeyRevocation`; the kind is tenant-scoped in the registry because the limits of a tenant's own clients are buckets of
//! that tenant, and no such client is made yet. The id is `<limit>/<subject>` and the value holds counts and a time.
//!
//! An item that is absent reads as a full bucket. The client never reads one that it does not know is there: a `Get` of
//! an absent item is a refusal that store@ audits (ADR 0005), and the first request of every new source would make one.
//! It creates the first state, and reads only when a create or an update finds the item has moved.

use std::time::Duration;

use sluis_kit::Client;
use sluis_proto::sluis::entry::v2alpha1::RateWindow;

use crate::{Error, Kind, Store, WriteOpts};

/// The kind `RateWindow`.
pub struct RateWindowKind;

impl Kind for RateWindowKind {
    type Id = String;
    type Value = RateWindow;
    const NAME: &'static str = "RateWindow";
}

/// A bucket as the store holds it: its state and the version a conditional update names.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Bucket {
    /// The tokens, in thousandths of a token.
    pub milli_tokens: u64,
    /// The time of the state, in milliseconds since the Unix epoch.
    pub at_ms: i64,
    /// The version of the item.
    pub version: u64,
}

/// Why a write of a bucket did not land.
#[derive(Debug)]
pub enum Conflict {
    /// A create found the bucket there, or an update found it changed: read it again.
    Moved,
    /// The store could not take the write.
    Failed(Error),
}

/// The client of the kind.
pub struct RateWindows {
    store: Store<RateWindowKind>,
}

impl RateWindows {
    /// A client over store@'s socket.
    ///
    /// # Errors
    ///
    /// When the kind is not in the registry.
    pub fn new(client: &Client) -> Result<Self, Error> {
        Ok(Self {
            store: Store::new_optional_tenant(client.clone())?,
        })
    }

    /// The bucket, or `None` when there is none (a full one).
    ///
    /// # Errors
    ///
    /// [`Error::Address`] for an id that is not a store id; the store's refusal otherwise.
    pub async fn get(&self, id: &str) -> Result<Option<Bucket>, Error> {
        match self.store.get("", id).await {
            Ok(i) => Ok(Some(Bucket {
                milli_tokens: i.value.milli_tokens,
                at_ms: i.value.at_ms,
                version: i.version,
            })),
            Err(Error::NotFound) => Ok(None),
            Err(e) => Err(e),
        }
    }

    /// Writes the first state of a bucket that is not there. The item lives for `ttl`, the time it takes to fill again.
    ///
    /// # Errors
    ///
    /// [`Conflict::Moved`] when the bucket is there after all.
    pub async fn create(
        &self,
        id: &str,
        milli_tokens: u64,
        at_ms: i64,
        ttl: Duration,
    ) -> Result<Bucket, Conflict> {
        let v = RateWindow {
            milli_tokens,
            at_ms,
            ..RateWindow::default()
        };
        let w = WriteOpts {
            ttl: Some(ttl),
            ..WriteOpts::default()
        };
        match self.store.create("", id, &v, w).await {
            Ok(i) => Ok(Bucket {
                milli_tokens,
                at_ms,
                version: i.version,
            }),
            Err(Error::Exists) => Err(Conflict::Moved),
            Err(e) => Err(Conflict::Failed(e)),
        }
    }

    /// Writes the next state of a bucket, if it is still at `version`.
    ///
    /// # Errors
    ///
    /// [`Conflict::Moved`] when it has changed since (or has expired).
    pub async fn update(
        &self,
        id: &str,
        milli_tokens: u64,
        at_ms: i64,
        version: u64,
        ttl: Duration,
    ) -> Result<Bucket, Conflict> {
        let v = RateWindow {
            milli_tokens,
            at_ms,
            ..RateWindow::default()
        };
        let w = WriteOpts {
            ttl: Some(ttl),
            ..WriteOpts::default()
        };
        match self.store.update("", id, &v, version, w).await {
            Ok(i) => Ok(Bucket {
                milli_tokens,
                at_ms,
                version: i.version,
            }),
            // An update of an item that is no longer there (it expired) is as much a move as a changed version.
            Err(Error::Version | Error::NotFound) => Err(Conflict::Moved),
            Err(e) => Err(Conflict::Failed(e)),
        }
    }
}
