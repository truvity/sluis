//! The limits of `entry` (ADR 0036, "Inside sluis", and its amendment of 2026-11-18, decision H7): a token bucket per client and
//! a token bucket per source address on `/token` and `/authorize`, and a token bucket per source address on the recovery
//! routes. A bucket is an item of the temp kind `RateWindow` (`sluis_store::ratewindow`), refilled by time.
//!
//! - **What a bucket is keyed by.** A client is its id, when the installation declares the client (`InstallationClient`): every
//!   other client id shares one bucket per limit, so a request cannot make a bucket by naming a client. A source address is a
//!   keyed hash made by key@'s `Mac` over the boundary's `mac` key (REQ-SEC-027), so the item holds no address. An address
//!   that is an IPv6 address is taken as its /64.
//! - **Where the address comes from.** `entry` serves one peer, the forwarder (`serve.rs`: any other uid is dropped before a
//!   byte is read), and the forwarder writes the address of the person into `X-Forwarded-For`. The last address of the header
//!   is the one the forwarder itself added; what a client wrote before it is never read. A request with no usable address is
//!   not counted against an address: a shared bucket for such requests would be a lock-out of everyone by anyone
//!   (REQ-SEC-017).
//! - **The address is counted before the client.** A source past its own bound is refused before it is charged to a client, so
//!   one address cannot empty the bucket of a client for the others.
//! - **A refusal** is `RATE_LIMITED`: HTTP 429 with the fixed body and `Retry-After`, the same for every limit and every
//!   subject. The word of the limit that refused goes to the audit record only (`status_code`).
//! - **One write per admit.** The first state of a bucket is created, and each admit after it is one conditional update. A
//!   request refused for an empty bucket writes nothing: the bucket is read from what this process last saw, which can only be
//!   fuller than the store's (every other instance only empties it), so a refusal made from it is never wrong. When a write finds
//!   the bucket moved, it reads the bucket and tries again, a few times; a bucket that stays contended refuses.
//! - **A store that fails fails closed:** the request is refused with 503, as the discovery document is, and never let through
//!   unbounded.

use std::collections::{HashMap, HashSet};
use std::net::{IpAddr, Ipv6Addr};
use std::sync::Mutex as StdMutex;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use base64::Engine as _;
use http::HeaderMap;
use sluis_kit::{Client, dial};
use sluis_proto::sluis::config::v2alpha1::InstallationClient;
use sluis_proto::sluis::key::v2alpha1::{KeyServiceClient, MacRequest};
use sluis_store::ratebound::{Limits, Rate, RateBoundKind};
use sluis_store::ratewindow::{Conflict, RateWindows};
use sluis_store::{Kind, Store};
use tokio::sync::Mutex;

use crate::Sockets;
use crate::refuse::Refuse;

/// The label of the keyed hash of a source address. key@ binds it into the input, so no other use of the `mac` key makes
/// the same hash of the same bytes.
const ADDRESS_LABEL: &str = "sluis-rate-address-v1";

/// How long the limits and the declared clients that were read are kept: a tightened bound takes effect within it.
const KEEP: Duration = Duration::from_secs(5);
/// How long a read of the store, or a call of key@, may take.
const READ: Duration = Duration::from_secs(5);
/// How many times an admit reads a bucket that moved and tries again.
const ATTEMPTS: usize = 5;
/// The most buckets this process remembers.
const SEEN_MAX: usize = 8192;
/// The most pages of clients that are read: a list that does not end is a store that does not answer as it should.
const PAGES_MAX: usize = 100;
/// The longest client id taken from a request: the store's longest id part.
const CLIENT_ID_MAX: usize = 128;

/// The limits, by the word the audit record gives for each.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Limit {
    TokenClient,
    TokenAddress,
    AuthorizeClient,
    AuthorizeAddress,
    RecoveryAddress,
}

impl Limit {
    /// The word of the limit in a bucket id.
    const fn word(self) -> &'static str {
        match self {
            Self::TokenClient => "token-client",
            Self::TokenAddress => "token-address",
            Self::AuthorizeClient => "authorize-client",
            Self::AuthorizeAddress => "authorize-address",
            Self::RecoveryAddress => "recovery-address",
        }
    }

    /// The refusal of a request past the limit: 429 with the fixed body, and the word of the limit for the audit record.
    const fn over(self) -> Refuse {
        Refuse::new(
            429,
            match self {
                Self::TokenClient => "rate_token_client",
                Self::TokenAddress => "rate_token_address",
                Self::AuthorizeClient => "rate_authorize_client",
                Self::AuthorizeAddress => "rate_authorize_address",
                Self::RecoveryAddress => "rate_recovery_address",
            },
        )
    }

    fn rate(self, l: &Limits) -> Rate {
        match self {
            Self::TokenClient => l.token_client,
            Self::TokenAddress => l.token_address,
            Self::AuthorizeClient => l.authorize_client,
            Self::AuthorizeAddress => l.authorize_address,
            Self::RecoveryAddress => l.recovery_address,
        }
    }
}

/// What the bucket of one limit holds after an admit, by the arithmetic below.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Level {
    pub milli_tokens: u64,
    pub at_ms: i64,
}

/// A token, in the thousandths that a level counts.
const TOKEN: u64 = 1000;

/// The level of a bucket at `now_ms`: the tokens it held at its last state, plus the refill since, and never more than the
/// size of the bucket. A bucket that is not there is full, and so is one whose last state is a whole window old. A clock that
/// went back adds nothing.
pub(crate) fn refilled(state: Option<Level>, rate: Rate, now_ms: i64) -> u64 {
    let cap = u64::from(rate.limit) * TOKEN;
    let Some(s) = state else { return cap };
    let elapsed = u128::try_from(now_ms.saturating_sub(s.at_ms)).unwrap_or(0);
    let window = rate.window.as_millis().max(1);
    // limit tokens per window, in thousandths: elapsed ms * limit * 1000 / window ms.
    let add = elapsed.saturating_mul(u128::from(cap)) / window;
    let level = u128::from(s.milli_tokens).saturating_add(add);
    u64::try_from(level.min(u128::from(cap))).unwrap_or(cap)
}

/// Takes one token. `None` when the bucket holds less than one.
pub(crate) fn take(state: Option<Level>, rate: Rate, now_ms: i64) -> Option<Level> {
    let level = refilled(state, rate, now_ms);
    let left = level.checked_sub(TOKEN)?;
    // The time of the new state never goes back, so an instance whose clock runs behind cannot refill a bucket by writing it.
    let at_ms = state.map_or(now_ms, |s| s.at_ms.max(now_ms));
    Some(Level {
        milli_tokens: left,
        at_ms,
    })
}

/// The time of the bucket: milliseconds since the Unix epoch, by the clock of this process.
fn now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_millis()).unwrap_or(i64::MAX))
}

struct ClientKind;
impl Kind for ClientKind {
    type Id = String;
    type Value = InstallationClient;
    const NAME: &'static str = "InstallationClient";
}

/// What was read from the store: the limits in force and the clients the installation declares.
#[derive(Clone)]
struct Settings {
    limits: Limits,
    clients: std::sync::Arc<HashSet<String>>,
}

/// What this process last saw of a bucket, and when the item is gone from the store.
#[derive(Clone, Copy)]
struct Seen {
    level: Level,
    version: u64,
    until_ms: i64,
}

/// Why a request was not admitted.
pub(crate) type Denied = Refuse;

pub(crate) struct Limiter {
    windows: Option<RateWindows>,
    bound: Option<Store<RateBoundKind>>,
    clients: Option<Store<ClientKind>>,
    keys: KeyServiceClient<sluis_kit::Transport>,
    settings: Mutex<Option<(Instant, Settings)>>,
    seen: StdMutex<HashMap<String, Seen>>,
}

impl Limiter {
    pub(crate) fn new(s: &Sockets) -> Self {
        let store: Client = dial(&s.store);
        let key: Client = dial(&s.key);
        Self {
            windows: RateWindows::new(&store).ok(),
            bound: Store::new(store.clone()).ok(),
            clients: Store::new(store).ok(),
            keys: KeyServiceClient::new(key.transport(), key.config()),
            settings: Mutex::new(None),
            seen: StdMutex::new(HashMap::new()),
        }
    }

    /// Counts a request against the source address of `limit`. A request that carries no usable address is not counted.
    ///
    /// # Errors
    ///
    /// The refusal of the request: 429 past the limit, 503 when a bucket cannot be read or written.
    pub(crate) async fn address(&self, limit: Limit, headers: &HeaderMap) -> Result<(), Denied> {
        let Some(addr) = forwarded_address(headers) else {
            return Ok(());
        };
        let settings = self.settings().await?;
        let hash = self.hash(&addr).await?;
        self.admit(limit, &hash, limit.rate(&settings.limits)).await
    }

    /// Counts a request against the client it names (`client_id` of the request, or the user of a Basic header).
    ///
    /// # Errors
    ///
    /// As [`Limiter::address`].
    pub(crate) async fn client(&self, limit: Limit, client_id: &str) -> Result<(), Denied> {
        let settings = self.settings().await?;
        let subject = if settings.clients.contains(client_id) {
            format!("c/{client_id}")
        } else {
            // An undeclared client is refused by the actor behind, whatever it names: all of them share one bucket, so a
            // request cannot make a bucket by naming a client, and cannot spend the bucket of a client that is declared.
            "other".to_owned()
        };
        self.admit(limit, &subject, limit.rate(&settings.limits))
            .await
    }

    async fn settings(&self) -> Result<Settings, Denied> {
        // The lock is held across the read: concurrent callers wait for one read, not make many.
        let mut kept = self.settings.lock().await;
        if let Some((at, s)) = kept.as_ref()
            && at.elapsed() < KEEP
        {
            return Ok(s.clone());
        }
        match self.read_settings().await {
            Ok(s) => {
                *kept = Some((Instant::now(), s.clone()));
                Ok(s)
            }
            // A store that fails after it answered once: the last answer stands, and the read is tried again after the
            // time it is kept. With no answer at all there is nothing to bound by, and the request is refused.
            Err(e) => match kept.as_mut() {
                Some((at, s)) => {
                    *at = Instant::now();
                    Ok(s.clone())
                }
                None => Err(e),
            },
        }
    }

    async fn read_settings(&self) -> Result<Settings, Denied> {
        let down = Refuse::new(503, "rate_store");
        let (Some(bound), Some(clients)) = (&self.bound, &self.clients) else {
            return Err(down);
        };
        let limits = async {
            // The kind is optional: a boundary that declares none has no item, and the defaults stand. A listing is an
            // answer in either case; a read of an item that is not there would be a refusal that store@ audits.
            let (items, _) = tokio::time::timeout(READ, bound.list("", None))
                .await
                .map_err(|_| down)?
                .map_err(|_| down)?;
            Ok::<_, Refuse>(
                items
                    .first()
                    .map_or_else(Limits::default, |i| Limits::from_bound(&i.value)),
            )
        };
        let declared = async {
            let mut ids = HashSet::new();
            let mut page = None;
            for _ in 0..PAGES_MAX {
                let (items, next) = tokio::time::timeout(READ, clients.list("", page))
                    .await
                    .map_err(|_| down)?
                    .map_err(|_| down)?;
                ids.extend(items.into_iter().map(|i| i.id));
                match next {
                    Some(n) => page = Some(n),
                    None => return Ok(ids),
                }
            }
            Err(down)
        };
        let (limits, clients) = tokio::join!(limits, declared);
        Ok(Settings {
            limits: limits?,
            clients: std::sync::Arc::new(clients?),
        })
    }

    /// The id of the bucket of a source address: `<hex of the first 128 bits of the keyed hash>`. key@ makes it, and it holds
    /// the address in no form.
    async fn hash(&self, addr: &[u8]) -> Result<String, Denied> {
        let down = Refuse::new(503, "rate_key");
        let resp = tokio::time::timeout(
            READ,
            self.keys.mac(MacRequest {
                purpose: "mac".into(),
                label: ADDRESS_LABEL.into(),
                input: addr.to_vec(),
                ..MacRequest::default()
            }),
        )
        .await
        .map_err(|_| down)?
        .map_err(|_| down)?
        .into_owned();
        let mac: &[u8] = &resp.mac;
        if mac.len() < 16 {
            return Err(down);
        }
        Ok(mac[..16].iter().map(|b| format!("{b:02x}")).collect())
    }

    async fn admit(&self, limit: Limit, subject: &str, rate: Rate) -> Result<(), Denied> {
        let (Some(windows), id) = (&self.windows, format!("{}/{subject}", limit.word())) else {
            return Err(Refuse::new(503, "rate_store"));
        };
        let down = Refuse::new(503, "rate_store");
        let rate = rate.clamped();
        let now = now_ms();
        let ttl = rate.window;
        let mut known = self.recall(&id, now);
        for _ in 0..ATTEMPTS {
            let Some(next) = take(known.map(|s| s.level), rate, now) else {
                return Err(limit.over());
            };
            let written = match known {
                None => {
                    windows
                        .create(&id, next.milli_tokens, next.at_ms, ttl)
                        .await
                }
                Some(s) => {
                    windows
                        .update(&id, next.milli_tokens, next.at_ms, s.version, ttl)
                        .await
                }
            };
            match written {
                Ok(b) => {
                    self.remember(
                        &id,
                        Seen {
                            level: next,
                            version: b.version,
                            until_ms: now
                                .saturating_add(i64::try_from(ttl.as_millis()).unwrap_or(0)),
                        },
                        now,
                    );
                    return Ok(());
                }
                Err(Conflict::Failed(_)) => return Err(down),
                Err(Conflict::Moved) => {
                    // The bucket is not as this process had it: read it, and take the token from what it holds.
                    known = match windows.get(&id).await {
                        Ok(Some(b)) => Some(Seen {
                            level: Level {
                                milli_tokens: b.milli_tokens,
                                at_ms: b.at_ms,
                            },
                            version: b.version,
                            until_ms: now
                                .saturating_add(i64::try_from(ttl.as_millis()).unwrap_or(0)),
                        }),
                        Ok(None) => None,
                        Err(_) => return Err(down),
                    };
                    if let Some(s) = known {
                        self.remember(&id, s, now);
                    } else {
                        self.forget(&id);
                    }
                }
            }
        }
        // A bucket that stays contended is a flood on one subject.
        Err(limit.over())
    }

    fn recall(&self, id: &str, now: i64) -> Option<Seen> {
        let seen = self.seen.lock().ok()?;
        seen.get(id).copied().filter(|s| s.until_ms > now)
    }

    fn remember(&self, id: &str, s: Seen, now: i64) {
        let Ok(mut seen) = self.seen.lock() else {
            return;
        };
        if seen.len() >= SEEN_MAX && !seen.contains_key(id) {
            seen.retain(|_, v| v.until_ms > now);
            if seen.len() >= SEEN_MAX {
                return;
            }
        }
        seen.insert(id.to_owned(), s);
    }

    fn forget(&self, id: &str) {
        if let Ok(mut seen) = self.seen.lock() {
            seen.remove(id);
        }
    }
}

/// The bytes that stand for the source address in the keyed hash, from the last address of `X-Forwarded-For`: the one the
/// forwarder wrote. A tag byte names the family, so no IPv4 address makes the bytes of an IPv6 one. An IPv6 address counts as
/// its /64, which is what one subscriber is given, and an IPv4-mapped IPv6 address as the IPv4 address. `None` for a request
/// with no such header or one that holds no address.
pub(crate) fn forwarded_address(h: &HeaderMap) -> Option<Vec<u8>> {
    let last = h
        .get_all("x-forwarded-for")
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|v| v.split(','))
        .map(str::trim)
        .rfind(|s| !s.is_empty())?;
    let text = last
        .strip_prefix('[')
        .and_then(|r| r.strip_suffix(']'))
        .unwrap_or(last);
    Some(match text.parse::<IpAddr>().ok()? {
        IpAddr::V4(a) => [&[4u8][..], &a.octets()].concat(),
        IpAddr::V6(a) => match a.to_ipv4_mapped() {
            Some(v4) => [&[4u8][..], &v4.octets()].concat(),
            None => [&[6u8][..], &prefix64(a)].concat(),
        },
    })
}

fn prefix64(a: Ipv6Addr) -> [u8; 8] {
    let o = a.octets();
    [o[0], o[1], o[2], o[3], o[4], o[5], o[6], o[7]]
}

/// The client id of a token or authorization request: the `client_id` parameter, or when there is none the user of a Basic
/// `Authorization` header (RFC 6749 section 2.3.1: form-urlencoded). Only the part before the colon is read, and it is
/// bounded; what follows it is never taken. An id that is not a usable string is empty, which is the bucket of the clients the
/// installation does not declare.
pub(crate) fn client_of(param: &str, authorization: &str) -> String {
    if !param.is_empty() {
        return bounded(param);
    }
    let Some(b64) = authorization
        .get(..6)
        .filter(|p| p.eq_ignore_ascii_case("basic "))
        .and_then(|_| authorization.get(6..))
    else {
        return String::new();
    };
    let Ok(raw) = base64::engine::general_purpose::STANDARD.decode(b64.trim()) else {
        return String::new();
    };
    let user = raw.split(|b| *b == b':').next().unwrap_or(&[]);
    let Some(user) = form_decode(user) else {
        return String::new();
    };
    bounded(&user)
}

fn bounded(s: &str) -> String {
    if s.len() > CLIENT_ID_MAX {
        String::new()
    } else {
        s.to_owned()
    }
}

/// `application/x-www-form-urlencoded` of one value: `+` is a space and `%XX` a byte. `None` for a bad escape or text that is
/// not UTF-8.
fn form_decode(b: &[u8]) -> Option<String> {
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'+' => out.push(b' '),
            b'%' => {
                let h = b.get(i + 1..i + 3)?;
                let h = std::str::from_utf8(h).ok()?;
                out.push(u8::from_str_radix(h, 16).ok()?);
                i += 2;
            }
            c => out.push(c),
        }
        i += 1;
    }
    String::from_utf8(out).ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::HeaderValue;

    const R: Rate = Rate {
        limit: 3,
        window: Duration::from_secs(60),
    };

    fn lv(milli: u64, at: i64) -> Option<Level> {
        Some(Level {
            milli_tokens: milli,
            at_ms: at,
        })
    }

    #[test]
    fn a_bucket_that_is_not_there_is_full_and_takes_a_token() {
        assert_eq!(refilled(None, R, 5), 3000);
        assert_eq!(take(None, R, 5), lv(2000, 5));
    }

    #[test]
    fn a_bucket_gives_its_limit_and_then_refuses_until_it_refills() {
        let mut s = None;
        for i in 0..3 {
            s = take(s, R, 1000 + i);
            assert!(s.is_some(), "token {i}");
        }
        assert_eq!(take(s, R, 1003), None);
        // 3 tokens per 60 s is a token every 20 s.
        assert_eq!(take(s, R, 1000 + 19_999), None);
        let s = take(s, R, 1002 + 20_000).expect("a token has come back");
        assert!(s.milli_tokens < TOKEN);
    }

    #[test]
    fn a_bucket_never_holds_more_than_its_size() {
        assert_eq!(refilled(lv(1500, 0), R, 10 * 60_000), 3000);
        // A limit lowered under a fuller bucket cuts it to the new size.
        assert_eq!(refilled(lv(50_000, 0), R, 0), 3000);
    }

    #[test]
    fn a_clock_that_went_back_adds_nothing_and_does_not_move_the_state_back() {
        let s = lv(500, 10_000);
        assert_eq!(refilled(s, R, 5_000), 500);
        assert_eq!(take(s, R, 5_000), None);
        let s = lv(2500, 10_000);
        let t = take(s, R, 5_000).unwrap();
        assert_eq!(t.at_ms, 10_000);
        assert_eq!(t.milli_tokens, 1500);
    }

    #[test]
    fn a_long_window_and_a_big_limit_do_not_overflow() {
        let r = Rate {
            limit: 100_000,
            window: Duration::from_secs(3600),
        };
        assert_eq!(refilled(lv(0, 0), r, i64::MAX), 100_000_000);
        assert_eq!(refilled(lv(u64::MAX, 0), r, i64::MAX), 100_000_000);
    }

    fn headers(v: &[&str]) -> HeaderMap {
        let mut h = HeaderMap::new();
        for x in v {
            h.append("x-forwarded-for", HeaderValue::from_str(x).unwrap());
        }
        h
    }

    #[test]
    fn the_address_is_the_last_one_the_forwarder_wrote() {
        let a = forwarded_address(&headers(&["203.0.113.9"])).unwrap();
        assert_eq!(a, [4, 203, 0, 113, 9]);
        // What a client wrote in front is never read.
        let b = forwarded_address(&headers(&["198.51.100.1, 203.0.113.9"])).unwrap();
        assert_eq!(a, b);
        let c = forwarded_address(&headers(&["198.51.100.1", "203.0.113.9"])).unwrap();
        assert_eq!(a, c);
        assert_eq!(forwarded_address(&headers(&["203.0.113.9, "])).unwrap(), a);
    }

    #[test]
    fn an_ipv6_address_counts_as_its_64_and_a_mapped_one_as_ipv4() {
        let a = forwarded_address(&headers(&["2001:db8:1:2:aaaa:bbbb:cccc:dddd"])).unwrap();
        let b = forwarded_address(&headers(&["[2001:db8:1:2::1]"])).unwrap();
        assert_eq!(a, b);
        assert_eq!(a, [6, 0x20, 0x01, 0x0d, 0xb8, 0, 1, 0, 2]);
        let c = forwarded_address(&headers(&["2001:db8:1:3::1"])).unwrap();
        assert_ne!(a, c);
        assert_eq!(
            forwarded_address(&headers(&["::ffff:203.0.113.9"])).unwrap(),
            [4, 203, 0, 113, 9]
        );
    }

    #[test]
    fn a_request_with_no_usable_address_has_none() {
        assert_eq!(forwarded_address(&HeaderMap::new()), None);
        for bad in [
            "",
            "unknown",
            "203.0.113.9:443",
            "203.0.113",
            "a, b",
            "999.1.1.1",
        ] {
            assert_eq!(forwarded_address(&headers(&[bad])), None, "{bad:?}");
        }
    }

    #[test]
    fn the_client_is_the_parameter_or_the_user_of_a_basic_header() {
        assert_eq!(client_of("sluis-cli", ""), "sluis-cli");
        // "sluis-cli:secret"
        assert_eq!(client_of("", "Basic c2x1aXMtY2xpOnNlY3JldA=="), "sluis-cli");
        assert_eq!(client_of("", "basic c2x1aXMtY2xpOnNlY3JldA=="), "sluis-cli");
        // The parameter wins.
        assert_eq!(
            client_of("other", "Basic c2x1aXMtY2xpOnNlY3JldA=="),
            "other"
        );
        // "my%2Dclient:s" and a user with a plus
        assert_eq!(client_of("", "Basic bXklMkRjbGllbnQ6cw=="), "my-client");
        // Not Basic, not base64, no colon, too long: no client.
        assert_eq!(client_of("", "Bearer abc"), "");
        assert_eq!(client_of("", "Basic !!!"), "");
        assert_eq!(client_of("", ""), "");
        assert_eq!(client_of(&"a".repeat(129), ""), "");
    }
}
