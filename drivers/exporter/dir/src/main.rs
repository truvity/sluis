//! The host of the `directory` driver of exporter@.
//!
//! The driver is atoom's (github.com/truvity/atoom, `destination/dir`, pinned by commit: `third_party/atoom/README.md`): the destination port
//! over a local directory that a forwarder serves. This file is what is sluis's: the socket with the peer credentials of its caller, the owner
//! check (only exporter@ may open it), the command line (`serve`, `check`, `conformance`, `version`) and `--socket-gid`.
//!
//! ```text
//! driver-exporter-dir serve       --socket PATH --root DIR --state-dir DIR [--socket-gid GID]
//! driver-exporter-dir check       --root DIR --state-dir DIR
//! driver-exporter-dir conformance        # the destination port suite over temporary directories
//! driver-exporter-dir version
//! ```
//!
//! `--socket` answers exporter@ alone. `--root` is the directory a forwarder serves; `--state-dir` is the driver's
//! own, where it records what it wrote.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, bail};
use atoom_destination_conformance::rpc::Service;
use atoom_destination_dir::{Directory, suite};
use clap::Arg;
use sluis_kit::cli::{Commands, Flags, Refused, shutdown_signal};
use sluis_kit::{Allow, ServerConfig};

/// The registry name of this driver's user.
const NAME: &str = "driver-exporter-dir";
/// The registry name of its one caller.
const OWNER: &str = "exporter";

struct Driver;

fn dir(f: &Flags, flag: &str) -> anyhow::Result<PathBuf> {
    match f.matches.get_one::<String>(flag) {
        Some(r) if !r.is_empty() => Ok(PathBuf::from(r)),
        _ => bail!("--{flag} is required"),
    }
}

fn open(f: &Flags) -> anyhow::Result<Directory> {
    Directory::open(&dir(f, "root")?, &dir(f, "state-dir")?)
}

impl Commands for Driver {
    async fn serve(&self, f: Flags) -> anyhow::Result<()> {
        if f.socket.as_os_str().is_empty() {
            bail!("--socket is required");
        }
        let gid = match f.matches.get_one::<String>("socket-gid") {
            None => None,
            Some(g) => match g.parse::<u32>() {
                Ok(g) if g != 0 => Some(g),
                _ => bail!("--socket-gid must be a group number other than 0"),
            },
        };
        // The directories are checked before a socket exists: no directory, no service.
        let d = open(&f)?;
        let swept = d.sweep().context("sweep the temporary files")?;
        if swept > 0 {
            tracing::warn!(swept, "removed temporary files a crash left behind");
        }
        let router = Service(Arc::new(d)).router();
        sluis_kit::serve(
            ServerConfig {
                actor: NAME,
                boundary: f.boundary.clone(),
                socket: f.socket.clone(),
                mode: gid.map_or(0o600, |_| 0o660),
                socket_gid: gid,
                allow: Allow::OwnerOnly(OWNER),
                default_deadline: Duration::from_secs(30),
            },
            router,
            shutdown_signal(),
        )
        .await
    }

    async fn check(&self, f: Flags) -> Option<anyhow::Result<()>> {
        Some(
            open(&f)
                .map(|_| ())
                .map_err(|e| Refused(format!("{e:#}")).into()),
        )
    }

    async fn conformance(&self, _f: Flags) -> Option<anyhow::Result<()>> {
        Some(
            suite::run_temp()
                .await
                .context("the destination port suite"),
        )
    }
}

fn main() {
    sluis_kit::cli::main(NAME, Driver, |c| {
        c.arg(
            Arg::new("root")
                .long("root")
                .value_name("DIR")
                .global(true)
                .help("The directory a forwarder serves"),
        )
        .arg(
            Arg::new("state-dir")
                .long("state-dir")
                .value_name("DIR")
                .global(true)
                .help("The driver's own directory, where it records what it wrote"),
        )
        .arg(
            Arg::new("socket-gid")
                .long("socket-gid")
                .value_name("GID")
                .global(true)
                .help("Open --socket (mode 0660) to this group; the default is 0600"),
        )
    });
}
