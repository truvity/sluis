#![allow(clippy::unwrap_used, clippy::expect_used)] // a failed unwrap in a test is the failure
//! The memory driver of atoom (library `atoom-store-memory`, pinned by release: `third_party/atoom/README.md`) under sluis's host: the
//! engine behind a socket that sluis's own guarded server serves, with this process's uid as the owner `store`. atoom's item kit and the
//! tests that atoom keeps for the driver itself run in atoom's CI; here is one smoke test, and the tests that prove, in this repository's
//! CI, what the records below say of the engine that store@'s own tests run on.

use std::path::PathBuf;
use std::time::Duration;

use atoom_store::atoom::store::v1alpha1::{
    ItemServiceClient, Put, QueryRequest, TailLogRequest, TransactRequest,
};
use atoom_store_memory::MemoryEngine;
use connectrpc::error::ErrorCode;
use sluis_kit::{Allow, Identities, ServerConfig, dial, refusal_reason};

/// Serves `engine` on `sock` behind sluis's guarded server, with this process's uid as the owner `store`, until `stop` is dropped or sent.
fn serve(
    engine: &MemoryEngine,
    sock: PathBuf,
) -> (
    tokio::sync::oneshot::Sender<()>,
    tokio::task::JoinHandle<anyhow::Result<()>>,
) {
    let me = rustix::process::geteuid().as_raw();
    let ids = Identities::with(0, move |uid| (uid == me).then_some(("store", 0)));
    let (stop, stopped) = tokio::sync::oneshot::channel::<()>();
    let task = tokio::spawn(sluis_kit::serve_with(
        ServerConfig {
            actor: "memory",
            boundary: "dev".into(),
            socket: sock,
            mode: 0o600,
            socket_gid: None,
            allow: Allow::OwnerOnly("store"),
            default_deadline: Duration::from_secs(30),
        },
        engine.clone().router(),
        async {
            let _ = stopped.await;
        },
        ids,
    ));
    (stop, task)
}

async fn wait_for(sock: &std::path::Path) {
    for _ in 0..100 {
        if sock.exists() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    panic!("{} did not appear", sock.display());
}

/// The smoke test of this host. atoom proves the in-memory driver against the item contract in its own CI; this proves that store@'s
/// engine starts behind sluis's guarded socket and that one write and one read come back (`just atoom-smoke`).
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_atoom_driver_smoke_one_write_and_one_read_through_the_guarded_socket() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("durable.sock");
    let (stop, server) = serve(&MemoryEngine::new(), socket.clone());
    wait_for(&socket).await;
    let c = dial(&socket);
    let rpc = ItemServiceClient::new(c.transport(), c.config());
    rpc.transact(TransactRequest {
        puts: vec![Put {
            key: b"smoke/one".to_vec(),
            value: b"v".to_vec(),
            ..Put::default()
        }],
        ..TransactRequest::default()
    })
    .await
    .unwrap();
    let got = rpc
        .get(atoom_store::atoom::store::v1alpha1::ItemGetRequest {
            key: b"smoke/one".to_vec(),
            linearizable: true,
            ..Default::default()
        })
        .await
        .unwrap()
        .into_owned();
    assert_eq!(got.item.value, b"v");
    let _ = stop.send(());
    server.await.unwrap().unwrap();
}

/// verifies: REQ-DATA-114, REQ-DATA-115
/// A log made again (a reset, or a restore into a new engine) has another epoch, and a position saved before it is refused after the
/// new log has grown past the saved sequence: the same number there names another position. A stream that is open when the log is made
/// again ends with the same refusal.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_log_made_again_has_another_epoch_and_a_saved_position_is_refused() {
    let dir = tempfile::tempdir().unwrap();
    let socket = dir.path().join("durable.sock");
    let engine = MemoryEngine::new();
    let (_stop, _server) = serve(&engine, socket.clone());
    wait_for(&socket).await;
    let c = dial(&socket);
    let rpc = ItemServiceClient::new(c.transport(), c.config());
    let write = |key: String| {
        let rpc = rpc.clone();
        async move {
            rpc.transact(TransactRequest {
                puts: vec![Put {
                    key: key.into_bytes(),
                    value: b"v".to_vec(),
                    log: true,
                    ..Put::default()
                }],
                ..TransactRequest::default()
            })
            .await
            .unwrap();
        }
    };
    let position = || {
        let rpc = rpc.clone();
        async move {
            let r = rpc
                .query(QueryRequest {
                    prefix: b"epoch/".to_vec(),
                    page_size: 1,
                    ..QueryRequest::default()
                })
                .await
                .unwrap()
                .into_owned();
            (r.log_head, r.log_epoch)
        }
    };
    let tail = |from_sequence: u64, log_epoch: u64| {
        let rpc = rpc.clone();
        async move {
            let mut s = rpc
                .tail_log(TailLogRequest {
                    from_sequence,
                    log_epoch,
                    ..TailLogRequest::default()
                })
                .await?;
            tokio::time::timeout(Duration::from_millis(400), s.message())
                .await
                .map_or(Ok(()), |m| m.map(|_| ()))
        }
    };
    for i in 0..5 {
        write(format!("epoch/a{i}")).await;
    }
    let (head, e1) = position().await;
    assert!(e1 != 0 && head >= 6);
    assert!(tail(head, e1).await.is_ok());
    let mut open = rpc
        .tail_log(TailLogRequest {
            from_sequence: head,
            log_epoch: e1,
            ..TailLogRequest::default()
        })
        .await
        .unwrap();

    engine.reset();
    for i in 0..10 {
        write(format!("epoch/b{i}")).await;
    }
    let (now, e2) = position().await;
    assert_ne!(e2, e1, "a log made again has another epoch");
    assert!(now >= head, "the new log has grown past the saved sequence");
    for epoch in [e1, 0] {
        let err = tail(head, epoch)
            .await
            .expect_err("a position of another log");
        assert_eq!(err.code, ErrorCode::FailedPrecondition);
        assert_eq!(refusal_reason(&err).as_deref(), Some("sequence_expired"));
    }
    assert!(
        tail(head, e2).await.is_ok(),
        "the same number in the new log is its own"
    );
    let end = tokio::time::timeout(Duration::from_secs(5), open.message())
        .await
        .expect("the old stream ends")
        .map(|_| ())
        .expect_err("the old stream is refused");
    assert_eq!(refusal_reason(&end).as_deref(), Some("sequence_expired"));
}
