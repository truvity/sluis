//! The limits of `entry` (ADR 0036, "Inside sluis" and the amendment of 2026-11-18): the built-in default and the built-in
//! maximum of each, and the reading of the config kind `RateBound` into the limits `entry` applies.
//!
//! The kind is optional. With no document every limit is its default, and a document that names some limits changes only
//! those. The loader of store@ refuses a document that goes past a maximum ([`check`]), so a signed document tightens or
//! loosens within what the release allows and never past it. `entry` applies [`Limits::from_bound`] to what it read and
//! clamps again ([`Rate::clamped`]), so a value that got past the loader (an older store@) still cannot lift a bound.

use std::time::Duration;

use sluis_proto::sluis::config::v2alpha1::{RateBound, RateLimit};

use crate::Kind;

/// A limit as the generated message holds it.
type Field = buffa::MessageField<RateLimit, buffa::Inline<RateLimit>>;

/// The kind `RateBound`: one item, `default`.
pub struct RateBoundKind;

impl Kind for RateBoundKind {
    type Id = &'static str;
    type Value = RateBound;
    const NAME: &'static str = "RateBound";
}

/// The id of the one item of the kind.
pub const ID: &str = "default";

/// The most requests one limit may admit in a window.
pub const LIMIT_MAX: u32 = 100_000;
/// The shortest window.
pub const WINDOW_MIN: Duration = Duration::from_secs(1);
/// The longest window: the longest a `RateWindow` item lives (kinds.yaml, `ttl.max`).
pub const WINDOW_MAX: Duration = Duration::from_secs(3600);

/// One limit: `limit` requests in a burst, refilled in `window`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Rate {
    /// The size of the bucket, in requests.
    pub limit: u32,
    /// The time an empty bucket takes to fill.
    pub window: Duration,
}

impl Rate {
    /// A limit of `limit` requests per minute.
    #[must_use]
    pub const fn per_minute(limit: u32) -> Self {
        Self {
            limit,
            window: Duration::from_secs(60),
        }
    }

    /// Whether the limit is inside the bounds the release allows.
    #[must_use]
    pub fn in_bounds(&self) -> bool {
        (1..=LIMIT_MAX).contains(&self.limit) && (WINDOW_MIN..=WINDOW_MAX).contains(&self.window)
    }

    /// The limit with each part held to its bound.
    #[must_use]
    pub fn clamped(self) -> Self {
        Self {
            limit: self.limit.clamp(1, LIMIT_MAX),
            window: self.window.clamp(WINDOW_MIN, WINDOW_MAX),
        }
    }

    fn from_message(m: &RateLimit) -> Option<Self> {
        let w = m.window.as_option()?;
        let secs = u64::try_from(w.seconds).ok()?;
        let nanos = u32::try_from(w.nanos).ok()?;
        Some(Self {
            limit: m.limit,
            window: Duration::new(secs, nanos),
        })
    }
}

/// The five limits of `entry`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Limits {
    /// `/token`, per client.
    pub token_client: Rate,
    /// `/token`, per source address.
    pub token_address: Rate,
    /// `/authorize`, per client.
    pub authorize_client: Rate,
    /// `/authorize`, per source address.
    pub authorize_address: Rate,
    /// `/passkeys/add` and `/passkeys/register`, per source address.
    pub recovery_address: Rate,
}

impl Default for Limits {
    /// The defaults of the release. They bound cost and are not quota: a source address may make five requests a second
    /// on `/token` and `/authorize`, and half a request a second on the recovery routes, for as long as it likes, and a
    /// client may make twenty a second. The conformance suites and a workforce behind one address stay well inside them.
    fn default() -> Self {
        Self {
            token_client: Rate::per_minute(1200),
            token_address: Rate::per_minute(300),
            authorize_client: Rate::per_minute(1200),
            authorize_address: Rate::per_minute(300),
            recovery_address: Rate::per_minute(30),
        }
    }
}

impl Limits {
    /// The limits a `RateBound` item states: a limit it names replaces the default, one it leaves out keeps it. Each is
    /// clamped to the bounds of the release.
    #[must_use]
    pub fn from_bound(b: &RateBound) -> Self {
        let d = Self::default();
        let pick = |m: &Field, d: Rate| {
            m.as_option()
                .and_then(Rate::from_message)
                .map_or(d, Rate::clamped)
        };
        Self {
            token_client: pick(&b.token_client, d.token_client),
            token_address: pick(&b.token_address, d.token_address),
            authorize_client: pick(&b.authorize_client, d.authorize_client),
            authorize_address: pick(&b.authorize_address, d.authorize_address),
            recovery_address: pick(&b.recovery_address, d.recovery_address),
        }
    }
}

/// Why a `RateBound` item is refused: the name of the field, and the rule. Never a value.
#[derive(Debug, PartialEq, Eq)]
pub struct Refused {
    /// The field of the item, such as `token_client.limit`.
    pub field: &'static str,
    /// What is wrong.
    pub rule: &'static str,
}

/// Checks a `RateBound` item against the bounds of the release: each limit it names is between 1 and [`LIMIT_MAX`]
/// requests, in a window between [`WINDOW_MIN`] and [`WINDOW_MAX`] (the longest a `RateWindow` item lives).
///
/// # Errors
///
/// [`Refused`], naming the first field that is out of bounds.
pub fn check(b: &RateBound) -> Result<(), Refused> {
    let fields: [(&Field, [&'static str; 2]); 5] = [
        (
            &b.token_client,
            ["token_client.limit", "token_client.window"],
        ),
        (
            &b.token_address,
            ["token_address.limit", "token_address.window"],
        ),
        (
            &b.authorize_client,
            ["authorize_client.limit", "authorize_client.window"],
        ),
        (
            &b.authorize_address,
            ["authorize_address.limit", "authorize_address.window"],
        ),
        (
            &b.recovery_address,
            ["recovery_address.limit", "recovery_address.window"],
        ),
    ];
    for (m, [limit, window]) in fields {
        let Some(m) = m.as_option() else { continue };
        if !(1..=LIMIT_MAX).contains(&m.limit) {
            return Err(Refused {
                field: limit,
                rule: "must be between 1 and 100000",
            });
        }
        let ok =
            Rate::from_message(m).is_some_and(|r| (WINDOW_MIN..=WINDOW_MAX).contains(&r.window));
        if !ok {
            return Err(Refused {
                field: window,
                rule: "must be between 1 second and 1 hour",
            });
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use buffa::MessageField;
    use buffa_types::google::protobuf::Duration as PbDuration;

    fn limit(n: u32, secs: i64) -> Field {
        MessageField::some(RateLimit {
            limit: n,
            window: MessageField::some(PbDuration {
                seconds: secs,
                nanos: 0,
                ..PbDuration::default()
            }),
            ..RateLimit::default()
        })
    }

    #[test]
    fn a_limit_a_document_names_replaces_the_default_and_the_rest_keep_it() {
        let b = RateBound {
            token_address: limit(7, 10),
            ..RateBound::default()
        };
        let l = Limits::from_bound(&b);
        assert_eq!(
            l.token_address,
            Rate {
                limit: 7,
                window: Duration::from_secs(10)
            }
        );
        assert_eq!(l.token_client, Limits::default().token_client);
        assert_eq!(Limits::from_bound(&RateBound::default()), Limits::default());
    }

    #[test]
    fn the_defaults_are_inside_the_bounds() {
        let d = Limits::default();
        for r in [
            d.token_client,
            d.token_address,
            d.authorize_client,
            d.authorize_address,
            d.recovery_address,
        ] {
            assert!(r.in_bounds(), "{r:?}");
        }
    }

    #[test]
    fn a_value_past_a_maximum_is_refused_by_the_check_and_clamped_by_the_reader() {
        for (b, field) in [
            (
                RateBound {
                    token_client: limit(LIMIT_MAX + 1, 60),
                    ..RateBound::default()
                },
                "token_client.limit",
            ),
            (
                RateBound {
                    authorize_address: limit(0, 60),
                    ..RateBound::default()
                },
                "authorize_address.limit",
            ),
            (
                RateBound {
                    recovery_address: limit(5, 3601),
                    ..RateBound::default()
                },
                "recovery_address.window",
            ),
            (
                RateBound {
                    token_address: limit(5, 0),
                    ..RateBound::default()
                },
                "token_address.window",
            ),
        ] {
            assert_eq!(check(&b).unwrap_err().field, field);
            // The reader does not trust the loader: no field of the result is past a bound.
            let l = Limits::from_bound(&b);
            for r in [
                l.token_client,
                l.token_address,
                l.authorize_client,
                l.authorize_address,
                l.recovery_address,
            ] {
                assert!(r.in_bounds(), "{r:?}");
            }
        }
        assert!(
            check(&RateBound {
                token_client: limit(LIMIT_MAX, 3600),
                token_address: limit(1, 1),
                ..RateBound::default()
            })
            .is_ok()
        );
    }
}
