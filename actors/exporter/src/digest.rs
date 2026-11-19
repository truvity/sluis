//! The SHA-256 digest that the engine names and compares documents by: lower-case hex, the form the destination port reports
//! (`Written::sha256`, `Listed::sha256`). The port crate has no digest (it needs no more than `std`), so the actor takes its own.

use aws_lc_rs::digest::{SHA256, digest};

/// The SHA-256 of `body`, lower-case hex.
#[must_use]
pub fn sha256_hex(body: &[u8]) -> String {
    atoom_destination::hex(digest(&SHA256, body).as_ref())
}

#[cfg(test)]
mod tests {
    use super::sha256_hex;

    #[test]
    fn the_digest_is_sha256_in_lower_case_hex() {
        assert_eq!(
            sha256_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
        assert_eq!(sha256_hex(b"").len(), 64);
    }
}
