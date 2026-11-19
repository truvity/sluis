//! The client of a destination driver's socket: the [`Destination`] that the engine publishes through.
//!
//! The trait, the documents and the rules are atoom's (`atoom-destination-conformance`, pinned by commit: `third_party/atoom/README.md`), and so is
//! a client of its own, but that one dials with atoom's plain socket. This one dials with sluis's kit, which hands the caller's deadline and
//! correlation id down (contracts.md "Every call"), and speaks the contract through `sluis-proto`'s copy of the vendored messages. The two
//! clients map every code and every refusal the same way.

use atoom_common::refusal::reason as refusal_reason;
use atoom_destination_conformance::{
    Destination, Document, Error, Fetched, Listed, Page, Purged, Written, check_document,
};
use connectrpc::ConnectError;
use connectrpc::error::ErrorCode;
use sluis_kit::{Client as Dial, Transport};
use sluis_proto::atoom::destination::v1alpha1::{
    DeleteRequest, DestinationServiceClient, ListRequest, PurgeRequest, PurgeResult, PutRequest,
    ReadBackRequest,
};

/// The [`Destination`] behind a driver's socket.
#[derive(Clone)]
pub struct Client {
    inner: DestinationServiceClient<Transport>,
}

impl Client {
    /// A client over the driver's socket (dialled at the first call).
    #[must_use]
    pub fn new(dial: &Dial) -> Self {
        Self {
            inner: DestinationServiceClient::new(dial.transport(), dial.config()),
        }
    }
}

fn error_of(e: &ConnectError) -> Error {
    match e.code {
        ErrorCode::Unimplemented => Error::Unsupported,
        ErrorCode::InvalidArgument => Error::Invalid("remote"),
        _ => Error::Unavailable(format!(
            "{} ({})",
            e.code.as_str(),
            refusal_reason(e).unwrap_or_else(|| "-".into())
        )),
    }
}

impl Destination for Client {
    async fn put(&self, doc: Document) -> Result<Written, Error> {
        // The same rule as the server's, so a document the wire would not take is refused here with its field.
        check_document(&doc)?;
        let r = self
            .inner
            .put(PutRequest {
                address: doc.address,
                content_type: doc.content_type,
                cache_control: doc.cache_control,
                body: doc.body,
                ..PutRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(Written {
            version: r.version,
            sha256: r.sha256,
        })
    }

    async fn delete(&self, address: &str) -> Result<bool, Error> {
        let r = self
            .inner
            .delete(DeleteRequest {
                address: address.to_owned(),
                ..DeleteRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(r.removed)
    }

    async fn list(&self, prefix: &str, page: Option<&str>, size: usize) -> Result<Page, Error> {
        let r = self
            .inner
            .list(ListRequest {
                prefix: prefix.to_owned(),
                page_token: page.unwrap_or_default().to_owned(),
                page_size: i32::try_from(size).unwrap_or(i32::MAX),
                ..ListRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(Page {
            documents: r
                .documents
                .into_iter()
                .map(|d| Listed {
                    address: d.address,
                    sha256: d.sha256,
                    content_type: d.content_type,
                    cache_control: d.cache_control,
                    size: d.size,
                })
                .collect(),
            next: (!r.next_page_token.is_empty()).then_some(r.next_page_token),
        })
    }

    async fn purge(&self, addresses: &[String]) -> Result<Purged, Error> {
        let r = self
            .inner
            .purge(PurgeRequest {
                addresses: addresses.to_vec(),
                ..PurgeRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        match r.result.as_known() {
            Some(PurgeResult::PURGE_RESULT_NONE) => Ok(Purged::None),
            Some(PurgeResult::PURGE_RESULT_PURGED) => Ok(Purged::Purged),
            _ => Err(Error::Unavailable("purge: an answer with no result".into())),
        }
    }

    async fn read_back(&self, address: &str) -> Result<Fetched, Error> {
        let r = self
            .inner
            .read_back(ReadBackRequest {
                address: address.to_owned(),
                ..ReadBackRequest::default()
            })
            .await
            .map_err(|e| error_of(&e))?
            .into_owned();
        Ok(Fetched {
            status: u16::try_from(r.status).unwrap_or(0),
            cache_control: r.cache_control,
            content_type: r.content_type,
            age: r.age,
            cache_state: r.cache_state,
            body: r.body,
        })
    }
}
