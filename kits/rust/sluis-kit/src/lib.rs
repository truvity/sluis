//! The Rust kit of sluis: the same surface as the Go kit (`kits/go/rpc`, `cli`, `refusal`).
//!
//! - [`serve`] serves on a Unix socket through atoom's socket transport (`atoom_socket::serve`), with
//!   sluis's call graph as the rule of its guard: a caller outside the call graph is refused before the
//!   message is decoded, and the handlers read the named caller with [`peer`].
//! - [`dial`] returns a Connect client over a Unix socket (h2c), atoom's. The deadline left and the
//!   correlation id of the call being served travel on every call made from its handler.
//! - [`refusal`] builds an error with the `atoom.common.v1alpha1.Refusal` detail.
//! - [`cli::main`] is the one command shape: `serve | check | version`, with atoom's restart back-off.
//!
//! The Protobuf runtime is buffa, the one `connectrpc` 0.9 generates for (PLAN.md D-23).

mod identity;
mod server;

pub mod cli;

pub use atoom_common::refusal::{new as refusal, reason as refusal_reason};
pub use atoom_socket::{Client, HEADER_CORRELATION, Transport, dial};
pub use identity::Peer;
pub use server::{Allow, ServerConfig, serve};

#[cfg(feature = "test-identities")]
pub use identity::Identities;
#[cfg(feature = "test-identities")]
pub use server::serve_with;

/// The caller of the call being served, set by the guard once the call passed it. `None` outside a
/// handler of a server built by [`serve`].
#[must_use]
pub fn peer(ext: &http::Extensions) -> Option<&Peer> {
    ext.get::<Peer>()
}

/// The correlation id of a call (contracts.md "Every call"), atoom's (`atoom_socket::call`).
pub mod correlation {
    use std::future::Future;

    /// The correlation id of the call being served, from any code that runs inside its handler
    /// (the server sets it around the handler, and the client passes it on). `None` outside a handler,
    /// or when the caller sent none or an invalid one. The audit emitter reads it for
    /// `metadata.correlation_uid`.
    #[must_use]
    pub fn current() -> Option<String> {
        atoom_socket::call::correlation()
    }

    /// The correlation id in a handler's request extensions, set by the server from the header the
    /// caller sent. `None` when the caller sent none.
    #[must_use]
    pub fn get(ext: &http::Extensions) -> Option<String> {
        atoom_socket::call::correlation_of(ext)
    }

    /// Runs `fut` as if it were the handler of a call with this correlation id. A test uses it to
    /// show that an id reaches the calls a handler makes; the server does the same for each call.
    pub async fn scope<F: Future>(correlation: Option<String>, fut: F) -> F::Output {
        let call = correlation.map_or_else(atoom_socket::Call::new, |id| {
            atoom_socket::Call::new().with_correlation(id)
        });
        atoom_socket::call::scope(call, fut).await
    }
}
