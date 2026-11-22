//! The server half of the kit: sluis's call graph as the rule of atoom's socket guard. The socket, the peer
//! credentials, the deadline and the correlation id are atoom's (`atoom_socket::serve`); what is sluis's is who a
//! uid is (the registry), the boundary of the caller, and the call graph.

use std::future::Future;
use std::path::PathBuf;
use std::time::Duration;

use connectrpc::interceptor::{
    Interceptor, Next, NextStream, PayloadStream, StreamRequest, StreamResponse, UnaryRequest,
    UnaryResponse, async_trait,
};
use connectrpc::{ConnectError, RequestContext, Router};

use crate::identity::{Identities, Peer};

/// The reason of a refusal of a caller the registry does not know, or of another boundary's uid block.
const REASON_CALLER: &str = atoom_socket::REASON_CALLER;
/// The reason of a refusal of a known caller that the call graph does not allow.
const REASON_CALL_GRAPH: &str = "call_graph";

/// Who may call this server.
#[derive(Clone, Debug)]
pub enum Allow {
    /// The call graph of the registry: `may_call(caller, actor)`.
    CallGraph,
    /// Only the named owner: a driver's socket answers its owner and nobody else.
    OwnerOnly(&'static str),
    /// Only the named caller: one of `key@`'s per-caller sockets.
    Exact(&'static str),
}

/// The configuration of one server on one socket.
#[derive(Clone, Debug)]
pub struct ServerConfig {
    /// The callee's registry name, e.g. `store`.
    pub actor: &'static str,
    /// The boundary the server belongs to.
    pub boundary: String,
    /// The absolute path of the socket (PLAN.md "Socket layout").
    pub socket: PathBuf,
    /// The mode of the socket file: 0o660 for an actor, 0o600 for a driver.
    pub mode: u32,
    /// The group of the socket file, or `None` to leave the one the kernel gave. A driver whose owner is
    /// another uid sets it together with mode 0o660, so that the members of the group, and only they, can
    /// connect. The process must be a member of the group. It changes who may connect and nothing else:
    /// the guard still decides, from the peer's uid, who may call. A gid of 0 is refused.
    pub socket_gid: Option<u32>,
    /// Who may call.
    pub allow: Allow,
    /// The deadline of a call that carries none (contracts.md "Every call").
    pub default_deadline: Duration,
}

/// Serves `router` on `cfg.socket` until `shutdown` resolves, then drains for at most 5 s.
///
/// The socket is made fresh: a stale socket file is replaced, a symlink or any other file is
/// refused. Each connection's peer credentials are read once, and every call passes the guard
/// before its message is decoded: a caller the registry does not know, or from another boundary's
/// uid block, is `permission_denied` with the reason `caller`; one the call graph does not allow
/// is `permission_denied` with `call_graph`.
///
/// # Errors
///
/// Fails when the socket cannot be made, or when the process does not run as a uid of the
/// registry (it could not tell its own boundary).
pub async fn serve(
    cfg: ServerConfig,
    router: Router,
    shutdown: impl Future<Output = ()> + Send,
) -> anyhow::Result<()> {
    serve_inner(cfg, router, shutdown, Identities::registry()?).await
}

/// [`serve`] with a table of uids of a test's own choosing. Test and example builds only.
///
/// # Errors
///
/// As [`serve`], except for the uid of the process.
#[cfg(feature = "test-identities")]
pub async fn serve_with(
    cfg: ServerConfig,
    router: Router,
    shutdown: impl Future<Output = ()> + Send,
    identities: Identities,
) -> anyhow::Result<()> {
    serve_inner(cfg, router, shutdown, identities).await
}

async fn serve_inner(
    cfg: ServerConfig,
    router: Router,
    shutdown: impl Future<Output = ()> + Send,
    identities: Identities,
) -> anyhow::Result<()> {
    let rule = Rule {
        actor: cfg.actor,
        allow: cfg.allow,
        identities,
    };
    let (check, namer, observer) = (rule.clone(), rule.clone(), rule);
    let mut socket = atoom_socket::ServerConfig::new(cfg.socket)
        .with_allow(atoom_socket::Allow::check(move |p, _| check.decide(p.uid)))
        .with_interceptor(Namer(namer))
        .with_on_refused(move |r| observer.log(r));
    socket.mode = cfg.mode;
    socket.socket_gid = cfg.socket_gid;
    socket.default_deadline = cfg.default_deadline;
    tracing::info!(actor = cfg.actor, boundary = %cfg.boundary, "listening");
    atoom_socket::serve(socket, router, shutdown).await
}

/// The call graph of one server (contracts.md "Errors"; 0003 "The call graph").
#[derive(Clone)]
struct Rule {
    actor: &'static str,
    allow: Allow,
    identities: Identities,
}

impl Rule {
    /// The registry name and uid block of a caller of this boundary, or `None`.
    fn name(&self, uid: u32) -> Option<(&'static str, u32)> {
        self.identities
            .name(uid)
            .filter(|(_, block)| *block == self.identities.own_block())
    }

    /// The decision for a caller: admitted, or the reason of the refusal.
    fn decide(&self, uid: u32) -> Result<(), &'static str> {
        let Some((name, _)) = self.name(uid) else {
            return Err(REASON_CALLER);
        };
        let allowed = match &self.allow {
            Allow::CallGraph => sluis_registry::may_call(name, self.actor),
            Allow::OwnerOnly(owner) => name == *owner,
            Allow::Exact(only) => name == *only,
        };
        if allowed {
            Ok(())
        } else {
            Err(REASON_CALL_GRAPH)
        }
    }

    /// The journal line of a refusal, naming the callee and, when the registry knows it, the caller.
    fn log(&self, r: &atoom_socket::Refused) {
        let uid = r.peer.map(|p| p.uid);
        let caller = uid.and_then(|u| self.identities.name(u)).map(|(n, _)| n);
        tracing::info!(
            callee = self.actor, caller, caller_uid = uid, reason = r.reason,
            procedure = %r.procedure, "refused: {}",
            if r.reason == REASON_CALL_GRAPH { "call graph" } else { "caller" }
        );
    }
}

/// Puts the named caller in the request's extensions, for [`crate::peer`]. It runs after the guard, so only for a
/// call the rule admitted.
struct Namer(Rule);

impl Namer {
    fn name(&self, ctx: &mut RequestContext) -> Result<(), ConnectError> {
        let conn = atoom_socket::peer(ctx.extensions()).copied();
        let named = conn.and_then(|c| self.0.name(c.uid).map(|n| (c, n)));
        // The guard admitted the call, so the caller has a name; a table that changed under it is refused all the same.
        let Some((conn, (name, block))) = named else {
            return Err(crate::refusal(
                connectrpc::error::ErrorCode::PermissionDenied,
                REASON_CALLER,
                "",
            ));
        };
        ctx.extensions_mut().insert(Peer {
            uid: conn.uid,
            gid: conn.gid,
            pid: conn.pid.unwrap_or(0),
            name,
            uid_block: block,
        });
        Ok(())
    }
}

#[async_trait]
impl Interceptor for Namer {
    async fn intercept_unary(
        &self,
        mut req: UnaryRequest,
        next: Next<'_>,
    ) -> Result<UnaryResponse, ConnectError> {
        self.name(&mut req.ctx)?;
        next.run(req).await
    }

    async fn intercept_streaming(
        &self,
        mut req: StreamRequest,
        inbound: PayloadStream,
        next: NextStream<'_>,
    ) -> Result<StreamResponse, ConnectError> {
        self.name(&mut req.ctx)?;
        next.run(req, inbound).await
    }
}
